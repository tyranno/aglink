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

// connectProfiles registers fake extensions in the given order and returns the
// daemon. Order matters: the first one connected is the default.
func connectProfiles(t *testing.T, accounts ...string) (*Daemon, *httptest.Server) {
	t.Helper()
	d := newDaemon("")
	srv := httptest.NewServer(d.handler())
	t.Cleanup(srv.Close)
	for _, a := range accounts {
		c := dialFakeExtension(t, srv, a)
		t.Cleanup(func() { c.Close() })
		go runFakeExtension(c, true)
		waitForProfile(t, d, a)
	}
	return d, srv
}

func resolveName(t *testing.T, d *Daemon, name string) (string, error) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	ec, err := d.resolve(name)
	if err != nil {
		return "", err
	}
	return ec.account, nil
}

// An empty name means "the default", which is the longest-connected profile.
// Recomputed per call, so a disconnect promotes the next one with no election.
func TestResolveDefaultsToOldestConnection(t *testing.T) {
	d, _ := connectProfiles(t, "first@example.com", "second@example.com")
	got, err := resolveName(t, d, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "first@example.com" {
		t.Fatalf("default = %q, want first@example.com", got)
	}
}

func TestResolveExactAccount(t *testing.T) {
	d, _ := connectProfiles(t, "first@example.com", "second@example.com")
	got, err := resolveName(t, d, "second@example.com")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "second@example.com" {
		t.Fatalf("got %q, want second@example.com", got)
	}
}

// Typing a full email on every override is tedious, so a prefix works when it
// picks out exactly one profile.
func TestResolveUniquePrefix(t *testing.T) {
	d, _ := connectProfiles(t, "doowon.lab.02@gmail.com", "tyranno1223@gmail.com")
	got, err := resolveName(t, d, "doowon")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "doowon.lab.02@gmail.com" {
		t.Fatalf("got %q, want doowon.lab.02@gmail.com", got)
	}
}

// A prefix matching several profiles must fail loudly rather than pick one:
// guessing here sends commands to the wrong browser.
func TestResolveAmbiguousPrefix(t *testing.T) {
	d, _ := connectProfiles(t, "same.a@gmail.com", "same.b@gmail.com")
	_, err := resolveName(t, d, "same")
	if err == nil {
		t.Fatal("ambiguous prefix must be an error")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("error should say it is ambiguous: %v", err)
	}
	if !strings.Contains(err.Error(), "same.a@gmail.com") || !strings.Contains(err.Error(), "same.b@gmail.com") {
		t.Fatalf("error should list the candidates: %v", err)
	}
}

func TestResolveUnknownListsConnected(t *testing.T) {
	d, _ := connectProfiles(t, "only@example.com")
	_, err := resolveName(t, d, "nobody")
	if err == nil {
		t.Fatal("unknown profile must be an error")
	}
	if !strings.Contains(err.Error(), "only@example.com") {
		t.Fatalf("error should list what IS connected: %v", err)
	}
}

func TestResolveIsCaseInsensitive(t *testing.T) {
	d, _ := connectProfiles(t, "mixed@example.com")
	got, err := resolveName(t, d, "MIXED@Example.COM")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "mixed@example.com" {
		t.Fatalf("got %q, want mixed@example.com", got)
	}
}

// The end-to-end check that routing actually reaches the named browser.
func TestCallRoutesToNamedProfile(t *testing.T) {
	d, _ := connectProfiles(t, "first@example.com", "second@example.com")
	res := d.call("list_tabs", nil, "second@example.com")
	if !res.OK || res.Text != "pong:list_tabs" {
		t.Fatalf("unexpected result: %+v", res)
	}
	res = d.call("list_tabs", nil, "nobody@example.com")
	if res.OK || !strings.Contains(res.Error, "not connected") {
		t.Fatalf("want not-connected error, got %+v", res)
	}
}

// list_profiles is answered by the daemon itself — it never goes out to an
// extension, so it works even with nothing connected.
func TestListProfilesEmpty(t *testing.T) {
	d := newDaemon("")
	res := d.call(listProfilesMethod, nil, "")
	if !res.OK {
		t.Fatalf("list_profiles should succeed with no profiles: %+v", res)
	}
	if !strings.Contains(res.Text, "no Chrome profiles connected") {
		t.Fatalf("unexpected text: %q", res.Text)
	}
}

// The listing is ordered the same way the default is chosen, so reading it tells
// you where an unspecified call will go.
func TestListProfilesMarksDefaultFirst(t *testing.T) {
	d, _ := connectProfiles(t, "first@example.com", "second@example.com")
	res := d.call(listProfilesMethod, nil, "")
	if !res.OK {
		t.Fatalf("list_profiles: %+v", res)
	}
	lines := strings.Split(strings.TrimSpace(res.Text), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(lines), res.Text)
	}
	if !strings.HasPrefix(lines[0], "first@example.com") {
		t.Fatalf("oldest connection should be listed first: %q", lines[0])
	}
	if !strings.Contains(lines[0], "default") {
		t.Fatalf("oldest connection should be marked default: %q", lines[0])
	}
	if strings.Contains(lines[1], "default") {
		t.Fatalf("only one profile may be marked default: %q", lines[1])
	}
}

// A profile argument on list_profiles is meaningless but must not error: the
// command is answered before any routing happens.
func TestListProfilesIgnoresProfileArg(t *testing.T) {
	d, _ := connectProfiles(t, "only@example.com")
	res := d.call(listProfilesMethod, nil, "nobody@example.com")
	if !res.OK {
		t.Fatalf("list_profiles must not route: %+v", res)
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
