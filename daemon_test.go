package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestOriginAllowed(t *testing.T) {
	anyExt := newDaemon("")
	if anyExt.originAllowed("https://evil.example") {
		t.Fatal("webpage origin must be rejected")
	}
	if !anyExt.originAllowed("chrome-extension://whatever") {
		t.Fatal("any chrome-extension origin should pass when unpinned")
	}

	pinned := newDaemon("abcdef")
	if pinned.originAllowed("chrome-extension://other") {
		t.Fatal("mismatched pinned id must be rejected")
	}
	if !pinned.originAllowed("chrome-extension://abcdef") {
		t.Fatal("matching pinned id should pass")
	}
}

func TestCallWithoutExtension(t *testing.T) {
	d := newDaemon("")
	res := d.call("list_tabs", nil)
	if res.OK || !strings.Contains(res.Error, "not connected") {
		t.Fatalf("expected not-connected error, got %+v", res)
	}
}

func TestCallRoundTrip(t *testing.T) {
	d := newDaemon("")
	srv := httptest.NewServer(d.handler())
	defer srv.Close()

	// Fake extension that answers keepalive pings and echoes each command as
	// "pong:<method>", i.e. behaves like a healthy background.js.
	conn := dialFakeExtension(t, srv)
	defer conn.Close()
	go runFakeExtension(conn, true /* replyToPings */)

	waitForExtension(t, d)

	// Exercise the full HTTP /call boundary the bridge uses.
	body, _ := json.Marshal(callRequest{Method: "list_tabs"})
	resp, err := http.Post(srv.URL+"/call", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /call: %v", err)
	}
	defer resp.Body.Close()
	var out CallResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.OK || out.Text != "pong:list_tabs" {
		t.Fatalf("unexpected call result: %+v", out)
	}
}

// TestDeadExtensionDetected reproduces the reported bug: an extension whose
// service worker stops responding (never answers keepalive pings) must be
// detected via the read deadline and dropped, so later calls report the honest
// "not connected" instead of hanging for the full call timeout.
func TestDeadExtensionDetected(t *testing.T) {
	d := newDaemon("")
	d.pingInterval = 20 * time.Millisecond
	d.readTimeout = 80 * time.Millisecond
	srv := httptest.NewServer(d.handler())
	defer srv.Close()

	conn := dialFakeExtension(t, srv)
	defer conn.Close()
	// replyToPings=false → simulates a suspended/terminated MV3 worker: the
	// socket lingers but nothing answers.
	go runFakeExtension(conn, false)

	waitForExtension(t, d)

	// After the read deadline elapses with no ping replies, the daemon should
	// clear the stale connection.
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.ext == nil
	})

	res := d.call("list_tabs", nil)
	if res.OK || !strings.Contains(res.Error, "not connected") {
		t.Fatalf("expected not-connected after dead extension dropped, got %+v", res)
	}
}

func dialFakeExtension(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ext"
	hdr := http.Header{}
	hdr.Set("Origin", "chrome-extension://fake")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, hdr)
	if err != nil {
		t.Fatalf("fake extension dial: %v", err)
	}
	return conn
}

// runFakeExtension mimics background.js: it echoes commands as "pong:<method>"
// and, when replyToPings is true, answers keepalive pings (id 0) so the daemon
// keeps the connection alive.
func runFakeExtension(conn *websocket.Conn, replyToPings bool) {
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var req Request
		if json.Unmarshal(data, &req) != nil {
			continue
		}
		if req.Method == pingMethod {
			if !replyToPings {
				continue
			}
			b, _ := json.Marshal(Reply{ID: 0, OK: true})
			if conn.WriteMessage(websocket.TextMessage, b) != nil {
				return
			}
			continue
		}
		b, _ := json.Marshal(Reply{ID: req.ID, OK: true, Text: "pong:" + req.Method})
		if conn.WriteMessage(websocket.TextMessage, b) != nil {
			return
		}
	}
}

func waitForExtension(t *testing.T, d *Daemon) {
	t.Helper()
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.ext != nil
	})
}

func TestHealth(t *testing.T) {
	d := newDaemon("")
	srv := httptest.NewServer(d.handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status: got %d", resp.StatusCode)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}
