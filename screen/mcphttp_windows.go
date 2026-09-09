//go:build windows

package main

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/server"
)

// aglink-screen serve — the SAME screen tools as the stdio server, exposed over
// MCP streamable HTTP so a session that cannot spawn a process on this machine
// can still drive it. The intended path is an SSH reverse tunnel: the remote
// box forwards its own 127.0.0.1:48220 here, and registers
//
//	claude mcp add --transport http aglink-screen-remote http://127.0.0.1:48220/mcp
//
// so its tools appear as mcp__aglink-screen-remote__* and never collide with a
// locally spawned aglink-screen.
//
// Two things differ from stdio and both are handled here, not in the tools:
//
//   - There is no process boundary. stdio's RunMCPScreen returns the user to
//     their original virtual desktop when the pipe closes (= the worker's turn
//     ended); a long-lived server never gets that signal, so it arms an idle
//     timer instead (returnDesktopWhenIdle).
//   - Control ownership is coarser. screen_lease_windows.go identifies the
//     owner by PID, and every remote session shares this one process, so remote
//     sessions do NOT lock each other out — only local-vs-remote is arbitrated.
//     That is the accepted policy for a single-operator desktop; if two remote
//     sessions ever need to be kept apart, the lease has to grow a session id.
const (
	defaultRemoteAddr = "127.0.0.1:48220"

	// defaultRemoteIdleMS is how long the server may sit with no request before
	// it returns the user to the desktop they were on. Long enough that normal
	// think-time between tool calls never trips it, short enough that a session
	// which simply walks away doesn't strand the user on another desktop.
	defaultRemoteIdleMS = 60000
	minRemoteIdleMS     = 5000
)

// remoteToken is the optional shared secret. Unset means "loopback is the only
// gate", which matches how aglink-web's /mcp ships; the difference is that this
// endpoint drives the keyboard, mouse and screen, so startup says so out loud
// rather than letting an unauthenticated desktop-control port pass unnoticed.
func remoteToken() string { return strings.TrimSpace(os.Getenv("AGLINK_SCREEN_REMOTE_TOKEN")) }

func resolveRemoteIdle() time.Duration {
	ms := defaultRemoteIdleMS
	if v := strings.TrimSpace(os.Getenv("AGLINK_SCREEN_REMOTE_IDLE_MS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= minRemoteIdleMS {
			ms = n
		}
	}
	return time.Duration(ms) * time.Millisecond
}

// idleWatch calls returnToOriginDesktop once the server has been quiet for the
// idle window. poke() restarts the clock; it is called on every request, so an
// active session never fires it.
type idleWatch struct {
	mu    sync.Mutex
	timer *time.Timer
	after time.Duration
}

func newIdleWatch(after time.Duration) *idleWatch {
	w := &idleWatch{after: after}
	w.timer = time.AfterFunc(after, w.fire)
	return w
}

func (w *idleWatch) poke() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.timer.Reset(w.after)
}

func (w *idleWatch) fire() {
	msg, err := returnToOriginDesktop()
	switch {
	case err != nil:
		fmt.Fprintf(os.Stderr, "aglink-screen: warning: idle return_desktop failed: %v\n", err)
	case msg != "":
		fmt.Fprintf(os.Stderr, "aglink-screen: idle %s\n", msg)
	}
}

// authorized reports whether the request may drive the screen. With no token
// configured every request passes — the listener is loopback-only, so reaching
// it already means shell access to this machine (or its SSH tunnel).
func authorized(r *http.Request, token string) bool {
	if token == "" {
		return true
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got)), []byte(token)) == 1
}

// remoteHandler wraps the MCP handler with the token gate and the idle poke.
func remoteHandler(inner http.Handler, token string, watch *idleWatch) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, token) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="aglink-screen"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if watch != nil {
			watch.poke()
		}
		inner.ServeHTTP(w, r)
	})
}

// newRemoteMux builds the served routes. Split out of RunScreenRemote so the
// tests exercise the real handler stack — MCP server, token gate and idle poke
// wired exactly as they ship — instead of a stand-in that could drift from it.
//
// Stateless, like aglink-web's /mcp: a dropped tunnel strands no server-side
// session, so the remote client reconnects and keeps working instead of hanging
// on a session id this process no longer has.
func newRemoteMux(token string, watch *idleWatch) http.Handler {
	mcpHTTP := server.NewStreamableHTTPServer(
		newScreenMCPServer(),
		server.WithStateLess(true),
	)
	mux := http.NewServeMux()
	mux.Handle("/mcp", remoteHandler(mcpHTTP, token, watch))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	return mux
}

// RunScreenRemote serves the screen tools over MCP streamable HTTP at /mcp.
func RunScreenRemote(addr string) error {
	if strings.TrimSpace(addr) == "" {
		addr = defaultRemoteAddr
	}
	token := remoteToken()
	mux := newRemoteMux(token, newIdleWatch(resolveRemoteIdle()))

	fmt.Fprintf(os.Stderr, "aglink-screen: serving MCP at http://%s/mcp\n", addr)
	if token == "" {
		fmt.Fprintln(os.Stderr, "aglink-screen: WARNING: no AGLINK_SCREEN_REMOTE_TOKEN set — anything that can reach this port controls the keyboard, mouse and screen")
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return srv.ListenAndServe()
}
