package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

// navTabFakeExtension answers like background.js for this test: navigate reports
// a fixed tab id, and every other method echoes back the tabId it received so
// the test can assert what the daemon targeted. A method listed in failTabGone
// returns Chrome's "No tab with id" error instead.
func navTabFakeExtension(conn *websocket.Conn, navID int, failGone map[string]bool) {
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
			continue
		}
		var reply Reply
		switch {
		case req.Method == "navigate":
			reply = Reply{ID: req.ID, OK: true, Text: fmt.Sprintf("ok: navigated tab %d — t — u", navID)}
		case failGone[req.Method]:
			reply = Reply{ID: req.ID, OK: false, Error: fmt.Sprintf("No tab with id: %v", req.Params["tabId"])}
		default:
			reply = Reply{ID: req.ID, OK: true, Text: fmt.Sprintf("tab=%v", req.Params["tabId"])}
		}
		b, _ := json.Marshal(reply)
		if conn.WriteMessage(websocket.TextMessage, b) != nil {
			return
		}
	}
}

func TestNavTabTargetsBackgroundTab(t *testing.T) {
	d := newDaemon("")
	srv := httptest.NewServer(d.handler())
	defer srv.Close()
	conn := dialFakeExtension(t, srv, "a@example.com")
	defer conn.Close()
	go navTabFakeExtension(conn, 777, nil)
	waitForProfile(t, d, "a@example.com")

	// Before any navigate, a tabId-less call is not redirected.
	if res := d.call("get_page_text", nil, ""); res.Text != "tab=<nil>" {
		t.Fatalf("pre-navigate should not inject a tab: %q", res.Text)
	}

	// navigate records the background tab.
	if res := d.call("navigate", map[string]any{"url": "https://x"}, ""); !res.OK {
		t.Fatalf("navigate: %+v", res)
	}

	// A later tabId-less page call is aimed at 777.
	if res := d.call("get_page_text", nil, ""); res.Text != "tab=777" {
		t.Errorf("tabId-less call should target the navigated tab: %q", res.Text)
	}
	// An explicit tabId always wins.
	if res := d.call("click", map[string]any{"tabId": 5, "selector": "a"}, ""); res.Text != "tab=5" {
		t.Errorf("explicit tabId must be preserved: %q", res.Text)
	}
	// Connection-level tools are never redirected.
	if res := d.call("list_tabs", nil, ""); res.Text != "tab=<nil>" {
		t.Errorf("list_tabs must not get a tabId: %q", res.Text)
	}
	// close_tab is deliberately excluded (no surprise close of the nav tab).
	if res := d.call("close_tab", nil, ""); res.Text != "tab=<nil>" {
		t.Errorf("close_tab must not be redirected: %q", res.Text)
	}
}

func TestNavTabClearedWhenTabGone(t *testing.T) {
	d := newDaemon("")
	srv := httptest.NewServer(d.handler())
	defer srv.Close()
	conn := dialFakeExtension(t, srv, "a@example.com")
	defer conn.Close()
	// get_page_text reports the tab is gone; everything else echoes.
	go navTabFakeExtension(conn, 777, map[string]bool{"get_page_text": true})
	waitForProfile(t, d, "a@example.com")

	if res := d.call("navigate", map[string]any{"url": "https://x"}, ""); !res.OK {
		t.Fatalf("navigate: %+v", res)
	}
	// This call is aimed at 777 and comes back "No tab with id", clearing navTab.
	if res := d.call("get_page_text", nil, ""); res.OK {
		t.Fatalf("expected tab-gone error, got ok: %+v", res)
	}
	// Next tabId-less call falls back to the active tab (no injection).
	if res := d.call("click", map[string]any{"selector": "a"}, ""); res.Text != "tab=<nil>" {
		t.Errorf("navTab should have been cleared: %q", res.Text)
	}
}

func dialWithBoot(t *testing.T, srv *httptest.Server, account, boot string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ext?account=" + url.QueryEscape(account) + "&boot=" + url.QueryEscape(boot)
	hdr := http.Header{}
	hdr.Set("Origin", "chrome-extension://fake")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, hdr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn
}

// reconnectSame swaps the account's connection for a new one with the given
// boot, the way an MV3 worker restart looks to the daemon.
func reconnectSame(t *testing.T, d *Daemon, srv *httptest.Server, old *websocket.Conn, boot string, navID int) *websocket.Conn {
	t.Helper()
	d.mu.Lock()
	before := d.exts["a@example.com"]
	d.mu.Unlock()
	old.Close()
	c := dialWithBoot(t, srv, "a@example.com", boot)
	go navTabFakeExtension(c, navID, nil)
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		ec := d.exts["a@example.com"]
		return ec != nil && ec != before
	})
	return c
}

func TestNavTabPerSessionSurvivesReconnect(t *testing.T) {
	d := newDaemon("")
	srv := httptest.NewServer(d.handler())
	defer srv.Close()
	c1 := dialWithBoot(t, srv, "a@example.com", "run1")
	go navTabFakeExtension(c1, 111, nil)
	waitForProfile(t, d, "a@example.com")

	s1 := callSession{key: "s1", label: "agent 1"}
	s2 := callSession{key: "s2", label: "agent 2"}
	if res := d.callAs(s1, "navigate", map[string]any{"url": "https://x"}, ""); !res.OK {
		t.Fatalf("navigate: %+v", res)
	}
	// Another session on the same profile does not inherit s1's tab.
	if res := d.callAs(s2, "get_page_text", nil, ""); res.Text != "tab=<nil>" {
		t.Fatalf("s2 must not be steered to s1's tab: %q", res.Text)
	}
	// Nor does a session-less caller.
	if res := d.call("get_page_text", nil, ""); res.Text != "tab=<nil>" {
		t.Fatalf("no-session caller must not be steered to s1's tab: %q", res.Text)
	}

	c2 := reconnectSame(t, d, srv, c1, "run1", 222)
	defer c2.Close()
	if res := d.callAs(s1, "get_page_text", nil, ""); res.Text != "tab=111" {
		t.Fatalf("s1's nav tab should survive the reconnect: %q", res.Text)
	}

	// A new browser run reuses tab ids, so the old ones are forgotten.
	c3 := reconnectSame(t, d, srv, c2, "run2", 333)
	defer c3.Close()
	if res := d.callAs(s1, "get_page_text", nil, ""); res.Text != "tab=<nil>" {
		t.Fatalf("a different boot must clear nav tabs: %q", res.Text)
	}
}

// groupEcho answers navigate by echoing the group params it received.
func groupEcho(conn *websocket.Conn) {
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var req Request
		if json.Unmarshal(data, &req) != nil || req.Method == pingMethod {
			continue
		}
		b, _ := json.Marshal(Reply{ID: req.ID, OK: true, Text: fmt.Sprintf("ok: navigated tab 5 — %v|%v", req.Params["group"], req.Params["groupTitle"])})
		if conn.WriteMessage(websocket.TextMessage, b) != nil {
			return
		}
	}
}

func TestNavigateCarriesSharedGroup(t *testing.T) {
	d := newDaemon("")
	srv := httptest.NewServer(d.handler())
	defer srv.Close()
	c := dialFakeExtension(t, srv, "a@example.com")
	defer c.Close()
	go groupEcho(c)
	waitForProfile(t, d, "a@example.com")

	if res := d.callAs(callSession{key: "k", label: "agent k"}, "navigate", map[string]any{"url": "u"}, ""); !strings.HasSuffix(res.Text, "agent|agent") {
		t.Fatalf("navigate should carry the shared agent group: %+v", res)
	}
	if res := d.callAs(callSession{key: "k2", label: "agent k2"}, "navigate", map[string]any{"url": "u"}, ""); !strings.HasSuffix(res.Text, "agent|agent") {
		t.Fatalf("a different session should carry the same shared group: %+v", res)
	}
	if res := d.call("navigate", map[string]any{"url": "u"}, ""); !strings.HasSuffix(res.Text, "<nil>|<nil>") {
		t.Fatalf("no session, no group: %+v", res)
	}
}

func TestSessionLabel(t *testing.T) {
	if got := sessionLabel("chat:42/conv:abcdef123456"); got != "agent abcdef12" {
		t.Fatalf("got %q", got)
	}
	if got := sessionLabel("chat:42"); got != "agent 42" {
		t.Fatalf("got %q", got)
	}
}
