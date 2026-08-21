package main

import (
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
}

func newDaemon(expectedExtID string) *Daemon {
	return &Daemon{
		expectedExtID: expectedExtID,
		exts:          make(map[string]*extConn),
		pending:       make(map[uint64]chan Reply),
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
	mux.HandleFunc("/call", d.handleCall)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
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

	// Keepalive: push an application-level ping every pingInterval. Received WS
	// messages reset Chrome's MV3 service-worker idle timer (Chrome 116+), so
	// this keeps the extension's worker from being terminated; the extension
	// answers each ping, and that reply refreshes our read deadline below. If
	// either side dies, no replies arrive, the deadline fires, and we tear the
	// stale connection down instead of letting commands hang.
	done := make(chan struct{})
	go d.pingLoop(ec, done)

	_ = conn.SetReadDeadline(time.Now().Add(d.readTimeout))
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			log.Printf("aglink-web: extension read ended: %v", err)
			break
		}
		_ = conn.SetReadDeadline(time.Now().Add(d.readTimeout))
		var rep Reply
		if err := json.Unmarshal(data, &rep); err != nil {
			log.Printf("aglink-web: bad reply frame: %v", err)
			continue
		}
		if rep.ID == 0 {
			continue // keepalive ack — nothing is waiting on id 0
		}
		d.mu.Lock()
		ch := d.pending[rep.ID]
		delete(d.pending, rep.ID)
		d.mu.Unlock()
		if ch != nil {
			ch <- rep // buffered cap 1, sole sender for this id — never blocks
		}
	}

	close(done)
	d.mu.Lock()
	if d.exts[account] == ec {
		delete(d.exts, account)
	}
	// Fail only the calls parked on THIS connection, instead of making them wait
	// out the full call timeout. Another profile's pending calls are still
	// perfectly answerable and must not be collateral damage.
	for id := range ec.waiting {
		if ch := d.pending[id]; ch != nil {
			ch <- Reply{ID: id, Error: "extension connection lost"}
			delete(d.pending, id)
		}
	}
	d.mu.Unlock()
	_ = conn.Close()
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

	d.mu.Lock()
	ec, err := d.resolve(profile)
	if err != nil {
		d.mu.Unlock()
		return CallResult{Error: err.Error()}
	}
	d.nextID++
	id := d.nextID
	ch := make(chan Reply, 1)
	d.pending[id] = ch
	ec.waiting[id] = struct{}{}
	d.mu.Unlock()

	defer func() {
		d.mu.Lock()
		delete(d.pending, id)
		delete(ec.waiting, id)
		d.mu.Unlock()
	}()

	req := Request{ID: id, Method: method, Params: params}
	data, err := json.Marshal(req)
	if err != nil {
		return CallResult{Error: fmt.Sprintf("marshal request: %v", err)}
	}

	ec.writeMu.Lock()
	err = ec.conn.WriteMessage(websocket.TextMessage, data)
	ec.writeMu.Unlock()
	if err != nil {
		return CallResult{Error: fmt.Sprintf("send to extension: %v", err)}
	}
	log.Printf("aglink-web: → %s #%d %s", ec.account, id, method)

	select {
	case rep := <-ch:
		if !rep.OK {
			log.Printf("aglink-web: ← ext #%d error: %s", id, rep.Error)
			return CallResult{Error: rep.Error}
		}
		log.Printf("aglink-web: ← ext #%d ok (%d bytes)", id, len(rep.Text))
		return CallResult{OK: true, Text: rep.Text}
	case <-time.After(callTimeout):
		log.Printf("aglink-web: ✗ ext #%d %s timed out", id, method)
		return CallResult{Error: "browser did not respond within timeout"}
	}
}

// listProfiles renders the connected profiles, oldest first — the same order
// resolve() uses to pick the default, so the first line is always where an
// unspecified call goes. Locks d.mu itself, unlike resolve().
func (d *Daemon) listProfiles() CallResult {
	d.mu.Lock()
	defer d.mu.Unlock()

	if len(d.exts) == 0 {
		return CallResult{OK: true, Text: "no Chrome profiles connected — sign in to Chrome and load the aglink-web extension"}
	}

	ecs := make([]*extConn, 0, len(d.exts))
	for _, ec := range d.exts {
		ecs = append(ecs, ec)
	}
	sort.Slice(ecs, func(i, j int) bool { return ecs[i].seq < ecs[j].seq })

	var b strings.Builder
	for i, ec := range ecs {
		fmt.Fprintf(&b, "%s | connected %s ago", ec.account, time.Since(ec.since).Round(time.Second))
		if i == 0 {
			b.WriteString(" | default")
		}
		b.WriteString("\n")
	}
	return CallResult{OK: true, Text: strings.TrimRight(b.String(), "\n")}
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
