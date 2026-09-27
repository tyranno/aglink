package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mark3labs/mcp-go/server"
)

// callTimeout bounds how long the daemon waits for the extension to answer a
// single browser command before giving up.
const callTimeout = 30 * time.Second

// extConn is one connected Chrome profile. The daemon holds one per signed-in
// account, so two profiles no longer evict each other.
type extConn struct {
	conn    *websocket.Conn
	account string    // lowercased; the map key and the routing name
	since   time.Time // registration time, for display only

	// seq is a strictly increasing registration counter and the real ordering
	// key for "oldest connection wins". since cannot do that job: Windows'
	// clock resolution is coarse enough (~15ms) that two extensions
	// reconnecting together — after a daemon restart, say — get identical
	// timestamps, and the default profile would then fall to Go's randomized
	// map iteration order.
	seq uint64

	// writeMu serializes writes to conn. Per-connection, not per-daemon:
	// gorilla conns are not write-safe, but two different profiles' sockets can
	// be written concurrently.
	writeMu sync.Mutex

	// waiting is the set of request ids parked on this connection. On
	// disconnect it lets us fail exactly this profile's in-flight calls instead
	// of every profile's — one browser dying must not make another's caller
	// wait out the full callTimeout.
	waiting map[uint64]struct{}

	// gone is set (under d.mu) once the connection's loop has ended, so a call
	// that resolved it a moment earlier fails at once instead of parking on a
	// connection nothing will ever answer from.
	gone bool
}

// Daemon is the persistent process (`aglink-web serve`). It holds one live
// Chrome-extension WebSocket per signed-in account, assigns correlation IDs to
// outbound commands, and blocks each POST /call until the matching Reply
// arrives.
type Daemon struct {
	// expectedExtID pins the accepted extension origin. When "" (unset), any
	// chrome-extension:// origin is accepted (a warning is logged). Set via
	// AGLINK_WEB_EXT_ID once the unpacked extension's ID is known (see README).
	expectedExtID string

	// Keepalive timing. pingInterval must stay well under Chrome's ~30s MV3
	// service-worker idle limit so our pushed pings keep the worker alive;
	// readTimeout is how long we tolerate silence (missed ping replies) before
	// declaring the connection dead. Fields (not consts) so tests can shrink them.
	pingInterval time.Duration
	readTimeout  time.Duration

	mu      sync.Mutex
	exts    map[string]*extConn // account (lowercased) → live connection
	nextSeq uint64              // registration counter; see extConn.seq
	nextID  uint64
	pending map[uint64]chan Reply

	upgrader websocket.Upgrader

	// Electron/Wails windows reached over CDP (profile app:… / cdp:…). Kept
	// apart from exts: an app never goes through the extension. discover is
	// a field so tests need not knock on real ports.
	apps     appState
	discover func(context.Context) []appInfo

	// VS Code windows (profile vscode:…), one connection each from the
	// aglink-vscode extension. Separate from exts so a window is never the
	// default Chrome profile.
	vscodes    map[string]*extConn
	vscodeInfo map[string]vscodeWindow
}

func newDaemon(expectedExtID string) *Daemon {
	return &Daemon{
		expectedExtID: expectedExtID,
		exts:          make(map[string]*extConn),
		pending:       make(map[uint64]chan Reply),
		apps:          appState{conns: make(map[string]*appTarget)},
		discover:      defaultDiscover,
		vscodes:       make(map[string]*extConn),
		vscodeInfo:    make(map[string]vscodeWindow),
		pingInterval:  10 * time.Second,
		readTimeout:   25 * time.Second,
		upgrader: websocket.Upgrader{
			// Origin is validated by checkOrigin below, not the default
			// same-origin policy (which is meaningless for a native server).
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

// handler wires the daemon's HTTP surface. Kept separate from serve() so tests
// can mount it on an httptest server.
func (d *Daemon) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ext", d.handleExt)
	mux.HandleFunc("/vscode", d.handleVSCode)
	mux.HandleFunc("/call", d.handleCall)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/mcp", d.mcpHandler())
	return mux
}

// mcpHandler serves MCP over streamable HTTP at /mcp, so a client that cannot
// spawn this binary — a Claude session on a remote machine reaching the daemon
// through an SSH reverse tunnel — still gets the full browser tool set. The
// stdio bridge remains the local path; this is the same server on a second
// transport.
//
// Stateless: each request stands alone, so a remote client reconnecting (a
// dropped tunnel, a restarted editor) never strands a session on this side.
//
// Tools dispatch straight into d.call rather than back through callDaemon,
// which would POST to this very process. One consequence: the .aglink-web
// project pin that callDaemon resolves from the caller's working directory
// cannot apply here, since the caller's directory lives on another machine.
// A remote call with no explicit 'profile' therefore lands on the daemon's
// default profile; name the profile in the call to target another.
func (d *Daemon) mcpHandler() http.Handler {
	return server.NewStreamableHTTPServer(
		newMCPServer(d.call),
		server.WithStateLess(true),
	)
}

// originAllowed reports whether a WS handshake Origin belongs to our extension.
// Webpages carry an http(s):// origin and are always rejected; only
// chrome-extension:// origins pass, optionally pinned to a specific ID.
func (d *Daemon) originAllowed(origin string) bool {
	if !strings.HasPrefix(origin, "chrome-extension://") {
		return false
	}
	if d.expectedExtID == "" {
		return true
	}
	return origin == "chrome-extension://"+d.expectedExtID
}

func (d *Daemon) handleExt(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if !d.originAllowed(origin) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		log.Printf("aglink-web: rejected extension connection from origin %q", origin)
		return
	}
	if d.expectedExtID == "" {
		log.Printf("aglink-web: extension connected from %q (AGLINK_WEB_EXT_ID unset — accepting any extension; pin it for production)", origin)
	}

	// The signed-in Chrome account is this profile's identity: it is the map key
	// and the name callers route by. Without it the connection would be
	// unaddressable, so refuse at the handshake rather than accept a slot
	// nothing can reach. A pre-multi-profile extension looks exactly like this.
	account := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("account")))
	if account == "" {
		http.Error(w, "missing account", http.StatusBadRequest)
		log.Printf("aglink-web: rejected extension with no account — sign in to Chrome, then reload the extension")
		return
	}

	conn, err := d.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // upgrader already wrote the error
	}

	ec := &extConn{
		conn:    conn,
		account: account,
		since:   time.Now(),
		waiting: make(map[uint64]struct{}),
	}
	d.mu.Lock()
	if old := d.exts[account]; old != nil {
		_ = old.conn.Close() // newest connection for THIS account wins
	}
	d.nextSeq++
	ec.seq = d.nextSeq
	d.exts[account] = ec
	n := len(d.exts)
	d.mu.Unlock()
	log.Printf("aglink-web: extension registered for %s (%d profile(s) connected)", account, n)

	d.serveConn(ec, "extension", func() {
		if d.exts[account] == ec {
			delete(d.exts, account)
		}
	})
	log.Printf("aglink-web: extension disconnected (%s)", account)
}

// pingMethod is the reserved keepalive method (id 0). The extension replies with
// {"id":0,"ok":true}, which the read loop drops but uses to refresh the deadline.
const pingMethod = "__ping"

// listProfilesMethod is the one command the daemon answers itself. Everything
// else is relayed to an extension; this one describes the extensions, so routing
// it would be circular — and it has to work when nothing is connected, which is
// exactly when you most want to ask.
const listProfilesMethod = "list_profiles"

func (d *Daemon) pingLoop(ec *extConn, done <-chan struct{}) {
	t := time.NewTicker(d.pingInterval)
	defer t.Stop()
	ping, _ := json.Marshal(Request{ID: 0, Method: pingMethod})
	for {
		select {
		case <-done:
			return
		case <-t.C:
			ec.writeMu.Lock()
			err := ec.conn.WriteMessage(websocket.TextMessage, ping)
			ec.writeMu.Unlock()
			if err != nil {
				_ = ec.conn.Close() // unblocks ReadMessage → triggers cleanup
				return
			}
		}
	}
}

// resolve picks the connection a call goes to. name is the caller's preference:
// an exact account, a unique prefix of one, or "" for the default.
//
// The caller must already hold d.mu — resolve reads d.exts and does not lock.
//
// The default is the longest-connected profile. It is recomputed on every call
// rather than stored, so when the default disconnects the next-oldest takes over
// with no re-election step.
func (d *Daemon) resolve(name string) (*extConn, error) {
	if len(d.exts) == 0 {
		return nil, fmt.Errorf("Chrome extension not connected — open Chrome with the aglink-web extension loaded")
	}

	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		var best *extConn
		for _, ec := range d.exts {
			if best == nil || ec.seq < best.seq {
				best = ec
			}
		}
		if len(d.exts) > 1 {
			log.Printf("aglink-web: no profile given, using default %s (connected: %s)", best.account, strings.Join(d.accountsLocked(), ", "))
		}
		return best, nil
	}

	if ec := d.exts[name]; ec != nil {
		return ec, nil
	}

	var matched []string
	for account := range d.exts {
		if strings.HasPrefix(account, name) {
			matched = append(matched, account)
		}
	}
	sort.Strings(matched)
	switch len(matched) {
	case 1:
		return d.exts[matched[0]], nil
	case 0:
		return nil, fmt.Errorf("profile %q not connected — connected: %s", name, strings.Join(d.accountsLocked(), ", "))
	default:
		// Picking one here would silently drive the wrong browser, which is the
		// exact failure this whole feature exists to prevent.
		return nil, fmt.Errorf("profile %q is ambiguous — matches: %s", name, strings.Join(matched, ", "))
	}
}

// accountsLocked returns the connected accounts, sorted. Caller holds d.mu.
func (d *Daemon) accountsLocked() []string {
	out := make([]string, 0, len(d.exts))
	for a := range d.exts {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// call sends one command to the extension and waits for its reply.
func (d *Daemon) call(method string, params map[string]any, profile string) CallResult {
	if method == listProfilesMethod {
		return d.listProfiles()
	}
	if strings.HasPrefix(method, "vscode_") {
		return d.callVSCode(method, params, profile)
	}
	if isVSCodeProfile(profile) {
		return CallResult{Error: fmt.Sprintf("%s works on web pages; for a VS Code window use the vscode_* tools", method)}
	}
	if isAppProfile(profile) {
		return d.callApp(method, params, profile)
	}
	if appOnlyMethods[method] {
		return appOnlyRefusal(method)
	}

	d.mu.Lock()
	ec, err := d.resolve(profile)
	d.mu.Unlock()
	if err != nil {
		return CallResult{Error: err.Error()}
	}
	if method == "proceed_insecure" {
		return d.proceedChrome(ec, params)
	}
	res := d.roundTrip(ec, method, params, callTimeout, "browser")
	if method == "navigate" && res.OK {
		res = d.autoProceed(ec, params, res)
	}
	return res
}

// listProfiles renders the connected profiles, oldest first — the same order
// resolve() uses to pick the default, so the first line is always where an
// unspecified call goes. Locks d.mu itself, unlike resolve().
//
// App windows with a DevTools port follow the Chrome profiles. Discovery runs
// after d.mu is released: it waits on the network, and holding the lock would
// stall every Chrome call meanwhile.
func (d *Daemon) listProfiles() CallResult {
	lines := d.chromeProfileLines()
	lines = append(lines, d.appProfileLines()...)
	lines = append(lines, d.vscodeProfileLines()...)
	return CallResult{OK: true, Text: strings.Join(lines, "\n")}
}

func (d *Daemon) chromeProfileLines() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	if len(d.exts) == 0 {
		return []string{"no Chrome profiles connected — sign in to Chrome and load the aglink-web extension"}
	}

	ecs := make([]*extConn, 0, len(d.exts))
	for _, ec := range d.exts {
		ecs = append(ecs, ec)
	}
	sort.Slice(ecs, func(i, j int) bool { return ecs[i].seq < ecs[j].seq })

	lines := make([]string, 0, len(ecs))
	for i, ec := range ecs {
		line := fmt.Sprintf("%s | connected %s ago", ec.account, time.Since(ec.since).Round(time.Second))
		if i == 0 {
			line += " | default"
		}
		lines = append(lines, line)
	}
	return lines
}

func (d *Daemon) handleCall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body callRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, CallResult{Error: fmt.Sprintf("bad request: %v", err)})
		return
	}
	if body.Method == "" {
		writeJSON(w, CallResult{Error: "missing method"})
		return
	}
	writeJSON(w, d.call(body.Method, body.Params, body.Profile))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// runDaemon binds the configured port on loopback and serves until killed. It
// writes the live port to the port file so the bridge can find it. Binding is
// exclusive, so a second `serve` fails fast — the single-daemon guarantee.
func runDaemon() error {
	port := configuredPort()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("bind %s (daemon already running?): %w", addr, err)
	}
	if err := writePort(port); err != nil {
		log.Printf("aglink-web: warning: could not write port file: %v", err)
	}
	d := newDaemon(expectedExtID())
	log.Printf("aglink-web daemon listening on %s", addr)
	return http.Serve(ln, d.handler())
}
