package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const extIDEnv = "AGLINK_WEB_EXT_ID"

// expectedExtID returns the pinned Chrome extension ID (from AGLINK_WEB_EXT_ID),
// or "" to accept any chrome-extension:// origin.
func expectedExtID() string {
	return strings.TrimSpace(os.Getenv(extIDEnv))
}

// callDaemon is used by the MCP bridge and the `cmd` fast-path. It ensures the
// daemon is running (auto-spawning it detached if not), then POSTs the command
// to /call and returns the result.
//
// profile is the caller's explicit override. When empty we fall back to the
// account pinned by the nearest .aglink-web/config, so a project directory
// decides which browser it drives without every call having to name it. Empty
// from both means "the daemon's default profile".
func callDaemon(method string, params map[string]any, profile string) CallResult {
	return callDaemonCtx(context.Background(), method, params, profile)
}

// localCallTimeout bounds one call to this machine's daemon, which gives up
// on a call itself well before this; it only catches a daemon that hung.
const localCallTimeout = 11 * time.Minute

// callDaemonCtx is callDaemon for an MCP call: ctx ending stops the wait and
// the retries.
func callDaemonCtx(ctx context.Context, method string, params map[string]any, profile string) CallResult {
	if profile == "" {
		if cwd, err := os.Getwd(); err == nil {
			profile = findProjectAccount(cwd)
		}
	}
	body, _ := json.Marshal(callRequest{Method: method, Params: params, Profile: profile, Env: bridgeEnv(),
		Session: bridgeSession.key, SessionLabel: bridgeSession.label})
	if !remoteDaemon() {
		port, err := ensureDaemon()
		if err != nil {
			return CallResult{Error: err.Error()}
		}
		out, _ := postDaemonCall(ctx, &http.Client{Timeout: localCallTimeout}, port, body)
		return out
	}
	// The daemon is on the PC, behind the VS Code bridge, a tunnel or the
	// network: it can be away for a moment (window reconnecting, PC waking,
	// relay starting the daemon). Keep trying for remoteRetryWindow, but only
	// while the call has certainly not reached the daemon, so no action runs
	// twice.
	client := &http.Client{Timeout: remoteCallTimeout}
	window := remoteRetryWindow
	if remoteGaveUp.recent() {
		// The last call already waited out the whole window (a bridge port
		// that moved, a PC that is off): don't make every call wait 30s again.
		window = remoteShortRetry
	}
	deadline := time.Now().Add(window)
	wait := 500 * time.Millisecond
	for {
		var out CallResult
		retry := false
		port, err := ensureDaemon()
		if err != nil {
			out, retry = CallResult{Error: err.Error()}, !permanentSetupError(err)
		} else {
			out, retry = postDaemonCall(ctx, client, port, body)
		}
		if !retry || time.Now().Add(wait).After(deadline) {
			remoteGaveUp.set(retry)
			if retry {
				out.Error += fmt.Sprintf(" (retried for %s)", window)
			}
			return out
		}
		select {
		case <-ctx.Done():
			return CallResult{Error: "call cancelled while waiting for the PC: " + out.Error}
		case <-time.After(wait):
		}
		// Capped low so the PC coming back is noticed within a second or two.
		if wait < 1500*time.Millisecond {
			wait *= 2
		}
	}
}

const (
	// remoteRetryWindow is how long a call to a remote daemon keeps retrying
	// while nothing answers before it reports the failure.
	remoteRetryWindow = 30 * time.Second
	// remoteShortRetry replaces it right after a call already waited it out.
	remoteShortRetry = 8 * time.Second
	// remoteCallTimeout bounds one remote call; the PC's relay gives up on a
	// call after 600s, so this only catches a link that died silently.
	remoteCallTimeout = 11 * time.Minute
)

// remoteGaveUp remembers a call that used up its whole retry window.
var remoteGaveUp gaveUp

type gaveUp struct {
	mu sync.Mutex
	at time.Time
}

// recent reports a give-up within the last minute with no answer since.
func (g *gaveUp) recent() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return !g.at.IsZero() && time.Since(g.at) < time.Minute
}

// set records a give-up (true) or an answer (false).
func (g *gaveUp) set(v bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if v {
		g.at = time.Now()
	} else {
		g.at = time.Time{}
	}
}

// postDaemonCall POSTs body to /call. retry reports a failure where the call never
// reached the daemon: the connection was refused, or the bridge / relay said
// the PC side is not reachable right now.
func postDaemonCall(ctx context.Context, client *http.Client, port int, body []byte) (out CallResult, retry bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, daemonBaseURL(port)+"/call", bytes.NewReader(body))
	if err != nil {
		return CallResult{Error: err.Error()}, false
	}
	req.Header.Set("Content-Type", "application/json")
	addTokenHeader(req) // the daemon may require it (authtoken.go)
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return CallResult{Error: fmt.Sprintf("call cancelled before the browser daemon at %s answered (it may or may not have run): %v", daemonBaseURL(port), ctx.Err())}, false
		}
		if dialFailed(err) {
			return CallResult{Error: fmt.Sprintf("browser daemon unavailable at %s: %v", daemonBaseURL(port), err)}, true
		}
		// The request was on its way when the link broke: it may have acted.
		return CallResult{Error: fmt.Sprintf("the connection to the browser daemon at %s broke while the call was in progress, so it may or may not have run. Check the page before repeating it: %v", daemonBaseURL(port), err)}, false
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return CallResult{Error: fmt.Sprintf("bad daemon response (%d): %v", resp.StatusCode, err)}, false
	}
	if resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable {
		return out, relayNotReached(out.Error)
	}
	return out, false
}

// dialFailed reports an error from before the request was sent: nothing
// accepted the connection.
func dialFailed(err error) bool {
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return true
	}
	return strings.Contains(err.Error(), "connection refused")
}

// relayNotReached reports a bridge or relay error that means the call never
// got to the daemon. "mid-answer" means it did, and may have acted.
func relayNotReached(msg string) bool {
	if strings.Contains(msg, "mid-answer") {
		return false
	}
	return strings.HasPrefix(msg, "aglink-bridge:") || strings.Contains(msg, "is not reachable") || strings.Contains(msg, "could not be started")
}

// remoteDaemon reports a daemon that is not this machine's: reached through a
// remote-routed loopback port, or at the client PC's address from
// SSH_CONNECTION. Such a daemon is never spawned here.
func remoteDaemon() bool {
	if remoteRouted() {
		return true
	}
	ip := net.ParseIP(daemonHost())
	return ip != nil && !ip.IsLoopback()
}

// permanentError marks a setup failure that retrying cannot cure (a 4xx from
// /health: wrong or old token), so the call reports it at once instead of
// after the whole retry window.
type permanentError struct{ error }

func (e permanentError) Unwrap() error { return e.error }

func permanentSetupError(err error) bool {
	var pe permanentError
	return errors.As(err, &pe) || strings.Contains(err.Error(), "is not set") || strings.Contains(err.Error(), "not a valid port")
}

// ensureDaemon returns the port to call once the daemon answers /health,
// spawning it detached if the current port is dead. A remote-routed session
// (bridgemode.go) never spawns: its daemon is on the PC, and the bridge's relay
// starts it there if needed.
func ensureDaemon() (int, error) {
	port, err := clientPort()
	if err != nil {
		return 0, err
	}
	if remoteDaemon() {
		status, body, err := probeDaemon(port)
		switch {
		case err != nil && remoteRouted():
			return 0, fmt.Errorf("aglink-web: nothing answers at %s: %w. If this is a VS Code SSH bridge, the window may be closed or reconnecting; if it stays this way, reopen the agent from the current window or a new integrated terminal to refresh its port and token", daemonBaseURL(port), err)
		case err != nil:
			return 0, fmt.Errorf("aglink-web: cannot reach the PC's aglink-web at %s (from SSH_CONNECTION): %w. Is the PC on, aglink-web serve running there with a token (aglink-web token) and the port open in its firewall?", daemonBaseURL(port), err)
		case status != http.StatusOK:
			err := fmt.Errorf("aglink-web: %s/health answered %d: %s", daemonBaseURL(port), status, body)
			if status >= 400 && status < 500 {
				// A wrong token or path does not fix itself by waiting.
				return 0, permanentError{err}
			}
			return 0, err
		}
		return port, nil
	}
	if daemonHealthy(port) {
		return port, nil
	}
	self, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("locate self to start daemon: %w", err)
	}
	cmd := detachedCommand(self, "serve")
	if breakawayFromJob(cmd) {
		if err := cmd.Start(); err != nil {
			cmd = detachedCommand(self, "serve")
		}
	}
	if cmd.Process == nil {
		if err := cmd.Start(); err != nil {
			return 0, fmt.Errorf("start daemon: %w", err)
		}
	}
	// Reap the child if it exits while this bridge lives (lost a bind race,
	// crashed); otherwise it stays a zombie under us on Linux for days.
	go cmd.Wait()
	// The daemon writes the port file on bind; poll /health until it is up.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if port := readPort(); daemonHealthy(port) {
			return port, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return 0, fmt.Errorf("browser daemon did not become ready")
}

func daemonHealthy(port int) bool {
	status, _, err := probeDaemon(port)
	return err == nil && status == http.StatusOK
}

// probeDaemon GETs /health, with the token the bridge requires. Through the
// bridge the relay may first have to start the PC's daemon (up to 5s).
func probeDaemon(port int) (int, string, error) {
	client := http.Client{Timeout: 500 * time.Millisecond}
	if remoteDaemon() {
		client.Timeout = 10 * time.Second
	}
	req, err := http.NewRequest(http.MethodGet, daemonBaseURL(port)+"/health", nil)
	if err != nil {
		return 0, "", err
	}
	addTokenHeader(req)
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return resp.StatusCode, strings.TrimSpace(string(b)), nil
}

// detachedCommand builds an *exec.Cmd that keeps running after the bridge exits.
// The OS-specific process-attribute setup lives in proc_windows.go / proc_other.go.
func detachedCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	applyDetach(cmd)
	return cmd
}
