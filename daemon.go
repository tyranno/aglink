package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// callTimeout bounds how long the daemon waits for the extension to answer a
// single browser command before giving up.
const callTimeout = 30 * time.Second

// Daemon is the persistent process (`aglink-web serve`). It holds the single
// live Chrome-extension WebSocket, assigns correlation IDs to outbound
// commands, and blocks each POST /call until the matching Reply arrives.
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
	ext     *websocket.Conn
	writeMu sync.Mutex // serializes writes to ext (gorilla conns are not write-safe)
	nextID  uint64
	pending map[uint64]chan Reply

	upgrader websocket.Upgrader
}

func newDaemon(expectedExtID string) *Daemon {
	return &Daemon{
		expectedExtID: expectedExtID,
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

	conn, err := d.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // upgrader already wrote the error
	}

	d.mu.Lock()
	if d.ext != nil {
		_ = d.ext.Close() // newest connection wins
	}
	d.ext = conn
	d.mu.Unlock()
	log.Printf("aglink-web: extension registered")

	// Keepalive: push an application-level ping every pingInterval. Received WS
	// messages reset Chrome's MV3 service-worker idle timer (Chrome 116+), so
	// this keeps the extension's worker from being terminated; the extension
	// answers each ping, and that reply refreshes our read deadline below. If
	// either side dies, no replies arrive, the deadline fires, and we tear the
	// stale connection down instead of letting commands hang.
	done := make(chan struct{})
	go d.pingLoop(conn, done)

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
	if d.ext == conn {
		d.ext = nil
	}
	// Fail any in-flight calls now instead of making them wait out the full
	// call timeout — the connection they were parked on is gone.
	for id, ch := range d.pending {
		ch <- Reply{ID: id, Error: "extension connection lost"}
		delete(d.pending, id)
	}
	d.mu.Unlock()
	_ = conn.Close()
	log.Printf("aglink-web: extension disconnected")
}

// pingMethod is the reserved keepalive method (id 0). The extension replies with
// {"id":0,"ok":true}, which the read loop drops but uses to refresh the deadline.
const pingMethod = "__ping"

func (d *Daemon) pingLoop(conn *websocket.Conn, done <-chan struct{}) {
	t := time.NewTicker(d.pingInterval)
	defer t.Stop()
	ping, _ := json.Marshal(Request{ID: 0, Method: pingMethod})
	for {
		select {
		case <-done:
			return
		case <-t.C:
			d.writeMu.Lock()
			err := conn.WriteMessage(websocket.TextMessage, ping)
			d.writeMu.Unlock()
			if err != nil {
				_ = conn.Close() // unblocks ReadMessage → triggers cleanup
				return
			}
		}
	}
}

// call sends one command to the extension and waits for its reply.
func (d *Daemon) call(method string, params map[string]any) CallResult {
	d.mu.Lock()
	conn := d.ext
	if conn == nil {
		d.mu.Unlock()
		return CallResult{Error: "Chrome extension not connected — open Chrome with the aglink-web extension loaded"}
	}
	d.nextID++
	id := d.nextID
	ch := make(chan Reply, 1)
	d.pending[id] = ch
	d.mu.Unlock()

	defer func() {
		d.mu.Lock()
		delete(d.pending, id)
		d.mu.Unlock()
	}()

	req := Request{ID: id, Method: method, Params: params}
	data, err := json.Marshal(req)
	if err != nil {
		return CallResult{Error: fmt.Sprintf("marshal request: %v", err)}
	}

	d.writeMu.Lock()
	err = conn.WriteMessage(websocket.TextMessage, data)
	d.writeMu.Unlock()
	if err != nil {
		return CallResult{Error: fmt.Sprintf("send to extension: %v", err)}
	}
	log.Printf("aglink-web: → ext #%d %s", id, method)

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
	writeJSON(w, d.call(body.Method, body.Params))
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
