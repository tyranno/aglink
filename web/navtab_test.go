package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
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
