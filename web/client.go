package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
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
	port, err := ensureDaemon()
	if err != nil {
		return CallResult{Error: err.Error()}
	}
	if profile == "" {
		if cwd, err := os.Getwd(); err == nil {
			profile = findProjectAccount(cwd)
		}
	}
	body, _ := json.Marshal(callRequest{Method: method, Params: params, Profile: profile, Env: bridgeEnv(),
		Session: bridgeSession.key, SessionLabel: bridgeSession.label})
	req, err := http.NewRequest(http.MethodPost, daemonBaseURL(port)+"/call", bytes.NewReader(body))
	if err != nil {
		return CallResult{Error: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	addTokenHeader(req) // the daemon may require it (authtoken.go)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return CallResult{Error: fmt.Sprintf("browser daemon unavailable: %v", err)}
	}
	defer resp.Body.Close()
	var out CallResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return CallResult{Error: fmt.Sprintf("bad daemon response: %v", err)}
	}
	return out
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
	if remoteRouted() {
		status, body, err := probeDaemon(port)
		switch {
		case err != nil:
			return 0, fmt.Errorf("aglink-web: nothing answers at %s: %w. If this is a VS Code SSH bridge, reopen the agent from the current window or a new integrated terminal to refresh its port and token", daemonBaseURL(port), err)
		case status != http.StatusOK:
			return 0, fmt.Errorf("aglink-web: %s/health answered %d: %s", daemonBaseURL(port), status, body)
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
	if remoteRouted() {
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
