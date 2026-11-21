package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// methodLog records which methods a fake extension received.
type methodLog struct {
	mu   sync.Mutex
	seen []string
}

func (l *methodLog) add(m string) {
	l.mu.Lock()
	l.seen = append(l.seen, m)
	l.mu.Unlock()
}

func (l *methodLog) count(m string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, s := range l.seen {
		if s == m {
			n++
		}
	}
	return n
}

// dropOnFirstCommand reads one command, records it, and closes the socket
// without answering — an MV3 worker killed mid-command.
func dropOnFirstCommand(conn *websocket.Conn, log *methodLog, dropped chan<- struct{}) {
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var req Request
		if json.Unmarshal(data, &req) != nil || req.Method == pingMethod {
			continue
		}
		log.add(req.Method)
		conn.Close()
		close(dropped)
		return
	}
}

// answerAndLog is runFakeExtension that also records each command.
func answerAndLog(conn *websocket.Conn, log *methodLog) {
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var req Request
		if json.Unmarshal(data, &req) != nil || req.Method == pingMethod {
			continue
		}
		log.add(req.Method)
		b, _ := json.Marshal(Reply{ID: req.ID, OK: true, Text: "pong:" + req.Method})
		if conn.WriteMessage(websocket.TextMessage, b) != nil {
			return
		}
	}
}

func TestCallWaitsForExtensionToConnect(t *testing.T) {
	d := newDaemon("")
	d.reconnectWait = 3 * time.Second
	srv := httptest.NewServer(d.handler())
	defer srv.Close()

	done := make(chan CallResult, 1)
	go func() { done <- d.call("list_tabs", nil, "") }()

	time.Sleep(100 * time.Millisecond)
	select {
	case res := <-done:
		t.Fatalf("call should be waiting for the extension, got %+v", res)
	default:
	}
	conn := dialFakeExtension(t, srv, "a@example.com")
	defer conn.Close()
	go runFakeExtension(conn, true)

	select {
	case res := <-done:
		if !res.OK || res.Text != "pong:list_tabs" {
			t.Fatalf("after connect: %+v", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call did not proceed once the extension connected")
	}
}

func TestCallWaitTimesOut(t *testing.T) {
	d := newDaemon("")
	d.reconnectWait = 150 * time.Millisecond
	start := time.Now()
	res := d.call("list_tabs", nil, "")
	if res.OK || !strings.Contains(res.Error, "Chrome extension not connected") || !strings.Contains(res.Error, "waited 0.15s") {
		t.Fatalf("want the not-connected error with the wait, got %+v", res)
	}
	if el := time.Since(start); el < 150*time.Millisecond {
		t.Fatalf("returned after %v, before the wait was up", el)
	}
}

func TestReconnectWaitEnvOverride(t *testing.T) {
	d := newDaemon("")
	d.reconnectWait = time.Hour
	t.Setenv(reconnectWaitEnv, "0")
	if res := d.call("list_tabs", nil, ""); res.OK || strings.Contains(res.Error, "waited") {
		t.Fatalf("AGLINK_WEB_RECONNECT_WAIT_MS=0 should fail at once: %+v", res)
	}
}

// dropThenReconnect runs method against an extension that dies on receiving it,
// with a second connection for the same account appearing right after.
func dropThenReconnect(t *testing.T, method string) (CallResult, *methodLog, *methodLog) {
	t.Helper()
	d := newDaemon("")
	d.reconnectWait = 3 * time.Second
	srv := httptest.NewServer(d.handler())
	t.Cleanup(srv.Close)

	first, second := &methodLog{}, &methodLog{}
	dropped := make(chan struct{})
	c1 := dialFakeExtension(t, srv, "a@example.com")
	go dropOnFirstCommand(c1, first, dropped)
	waitForProfile(t, d, "a@example.com")

	go func() {
		<-dropped
		time.Sleep(50 * time.Millisecond)
		c2 := dialFakeExtension(t, srv, "a@example.com")
		t.Cleanup(func() { c2.Close() })
		go answerAndLog(c2, second)
	}()
	res := d.call(method, map[string]any{"selector": "#x"}, "")
	return res, first, second
}

func TestNonIdempotentCallNotResentAfterDrop(t *testing.T) {
	res, first, second := dropThenReconnect(t, "click")
	time.Sleep(150 * time.Millisecond) // let the reconnect land, to show nothing is resent on it
	if res.OK || !strings.Contains(res.Error, "may or may not have run") {
		t.Fatalf("a dropped click must fail and say it may have run: %+v", res)
	}
	if first.count("click") != 1 || second.count("click") != 0 {
		t.Fatalf("click must reach the extension exactly once: first=%d second=%d", first.count("click"), second.count("click"))
	}
}

func TestReadOnlyCallRetriedAfterDrop(t *testing.T) {
	res, first, second := dropThenReconnect(t, "get_page_text")
	if !res.OK || res.Text != "pong:get_page_text" {
		t.Fatalf("a read-only call should be answered by the new connection: %+v", res)
	}
	if first.count("get_page_text") != 1 || second.count("get_page_text") != 1 {
		t.Fatalf("want one send per connection: first=%d second=%d", first.count("get_page_text"), second.count("get_page_text"))
	}
}

// A request that never reached the extension is safe to send again, whatever
// the method.
func TestUnsentCallDeliveredAfterReconnect(t *testing.T) {
	d := newDaemon("")
	srv := httptest.NewServer(d.handler())
	defer srv.Close()

	c1 := dialFakeExtension(t, srv, "a@example.com")
	waitForProfile(t, d, "a@example.com")
	d.mu.Lock()
	old := d.exts["a@example.com"]
	d.mu.Unlock()
	c1.Close()
	waitFor(t, func() bool { return profileCount(d) == 0 })

	second := &methodLog{}
	go func() {
		time.Sleep(50 * time.Millisecond)
		c2 := dialFakeExtension(t, srv, "a@example.com")
		t.Cleanup(func() { c2.Close() })
		go answerAndLog(c2, second)
	}()
	_, res := d.sendChrome(old, "click", map[string]any{"selector": "#x"}, 3*time.Second)
	if !res.OK || second.count("click") != 1 {
		t.Fatalf("unsent click should go to the new connection once: %+v (count %d)", res, second.count("click"))
	}
}
