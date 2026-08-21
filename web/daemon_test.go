package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	res := d.call("list_tabs", nil, "")
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
	conn := dialFakeExtension(t, srv, "a@b.com")
	defer conn.Close()
	go runFakeExtension(conn, true /* replyToPings */)

	waitForProfile(t, d, "a@b.com")

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

	conn := dialFakeExtension(t, srv, "a@b.com")
	defer conn.Close()
	// replyToPings=false → simulates a suspended/terminated MV3 worker: the
	// socket lingers but nothing answers.
	go runFakeExtension(conn, false)

	waitForProfile(t, d, "a@b.com")

	// After the read deadline elapses with no ping replies, the daemon should
	// clear the stale connection.
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return len(d.exts) == 0
	})

	res := d.call("list_tabs", nil, "")
	if res.OK || !strings.Contains(res.Error, "not connected") {
		t.Fatalf("expected not-connected after dead extension dropped, got %+v", res)
	}
}

// dialFakeExtension connects as a profile signed in as `account`. Passing ""
// omits the query parameter entirely, which is how a pre-multi-profile
// extension behaves.
func dialFakeExtension(t *testing.T, srv *httptest.Server, account string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ext"
	if account != "" {
		wsURL += "?account=" + url.QueryEscape(account)
	}
	hdr := http.Header{}
	hdr.Set("Origin", "chrome-extension://fake")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, hdr)
	if err != nil {
		t.Fatalf("fake extension dial (%s): %v", account, err)
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

func waitForProfile(t *testing.T, d *Daemon, account string) {
	t.Helper()
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.exts[account] != nil
	})
}

func profileCount(d *Daemon) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.exts)
}

// TestTwoProfilesStayConnected is the regression test for this feature's whole
// reason to exist: before the exts map, the daemon kept a single connection and
// closed the old one on every new handshake, so two Chrome profiles fought over
// the bridge and neither could be relied on.
func TestTwoProfilesStayConnected(t *testing.T) {
	d := newDaemon("")
	srv := httptest.NewServer(d.handler())
	defer srv.Close()

	c1 := dialFakeExtension(t, srv, "one@example.com")
	defer c1.Close()
	go runFakeExtension(c1, true)
	waitForProfile(t, d, "one@example.com")

	c2 := dialFakeExtension(t, srv, "two@example.com")
	defer c2.Close()
	go runFakeExtension(c2, true)
	waitForProfile(t, d, "two@example.com")

	// The first profile must still be registered — this is what used to break.
	if n := profileCount(d); n != 2 {
		t.Fatalf("connected profiles = %d, want 2", n)
	}
	d.mu.Lock()
	first := d.exts["one@example.com"]
	d.mu.Unlock()
	if first == nil {
		t.Fatal("first profile was evicted by the second connection")
	}
}

// A profile's own reconnect (MV3 service worker waking up) must replace only
// its own entry and leave every other profile untouched.
func TestReconnectReplacesOnlyItsOwnEntry(t *testing.T) {
	d := newDaemon("")
	srv := httptest.NewServer(d.handler())
	defer srv.Close()

	other := dialFakeExtension(t, srv, "other@example.com")
	defer other.Close()
	go runFakeExtension(other, true)
	waitForProfile(t, d, "other@example.com")

	first := dialFakeExtension(t, srv, "same@example.com")
	go runFakeExtension(first, true)
	waitForProfile(t, d, "same@example.com")
	d.mu.Lock()
	firstEC := d.exts["same@example.com"]
	d.mu.Unlock()

	second := dialFakeExtension(t, srv, "same@example.com")
	defer second.Close()
	go runFakeExtension(second, true)
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.exts["same@example.com"] != nil && d.exts["same@example.com"] != firstEC
	})
	first.Close()

	if n := profileCount(d); n != 2 {
		t.Fatalf("connected profiles = %d, want 2 (the other profile must survive)", n)
	}
	d.mu.Lock()
	survived := d.exts["other@example.com"] != nil
	d.mu.Unlock()
	if !survived {
		t.Fatal("unrelated profile was dropped by another profile's reconnect")
	}
}

// Without an account the daemon cannot route anything to this connection, so it
// is refused at the handshake rather than accepted into an unaddressable slot.
// This is also what a pre-multi-profile extension looks like.
func TestHandshakeWithoutAccountRejected(t *testing.T) {
	d := newDaemon("")
	srv := httptest.NewServer(d.handler())
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ext"
	hdr := http.Header{}
	hdr.Set("Origin", "chrome-extension://fake")
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, hdr)
	if err == nil {
		conn.Close()
		t.Fatal("handshake without account must be rejected")
	}
	if resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want HTTP 400, got resp=%v", resp)
	}
	if n := profileCount(d); n != 0 {
		t.Fatalf("connected profiles = %d, want 0", n)
	}
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
