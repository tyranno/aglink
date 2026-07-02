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

	// Connect a fake extension that echoes each request as "pong:<method>".
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ext"
	hdr := http.Header{}
	hdr.Set("Origin", "chrome-extension://fake")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, hdr)
	if err != nil {
		t.Fatalf("fake extension dial: %v", err)
	}
	defer conn.Close()

	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var req Request
			if json.Unmarshal(data, &req) != nil {
				continue
			}
			rep := Reply{ID: req.ID, OK: true, Text: "pong:" + req.Method}
			b, _ := json.Marshal(rep)
			if conn.WriteMessage(websocket.TextMessage, b) != nil {
				return
			}
		}
	}()

	// Wait for the daemon to register the extension connection.
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.ext != nil
	})

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
