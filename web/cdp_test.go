package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeCDP is a scripted CDP endpoint. handle is called for every request the
// client sends and may write any number of frames back (responses, events).
type fakeCDP struct {
	srv    *httptest.Server
	mu     sync.Mutex
	conn   *websocket.Conn
	got    []cdpMsg
	handle func(f *fakeCDP, req cdpMsg)
}

func newFakeCDP(t *testing.T, handle func(f *fakeCDP, req cdpMsg)) *fakeCDP {
	t.Helper()
	f := &fakeCDP{handle: handle}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conn = c
		f.mu.Unlock()
		for {
			_, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			var m cdpMsg
			_ = json.Unmarshal(data, &m)
			f.mu.Lock()
			f.got = append(f.got, m)
			f.mu.Unlock()
			if f.handle != nil {
				f.handle(f, m)
			}
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCDP) url() string { return "ws" + strings.TrimPrefix(f.srv.URL, "http") + "/devtools/page/X" }

func (f *fakeCDP) send(v any) {
	data, _ := json.Marshal(v)
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = f.conn.WriteMessage(websocket.TextMessage, data)
}

func (f *fakeCDP) reply(id int64, result any) {
	f.send(map[string]any{"id": id, "result": result})
}

func (f *fakeCDP) requests() []cdpMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]cdpMsg(nil), f.got...)
}

func TestCDPCallMatchesReplyByID(t *testing.T) {
	f := newFakeCDP(t, func(f *fakeCDP, req cdpMsg) {
		f.reply(req.ID, map[string]any{"echo": req.Method})
	})
	c, err := dialCDP(f.url())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	res, err := c.Call(context.Background(), "Runtime.evaluate", map[string]any{"expression": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res), "Runtime.evaluate") {
		t.Fatalf("wrong result: %s", res)
	}
}

func TestCDPOutOfOrderRepliesReachTheirCallers(t *testing.T) {
	var mu sync.Mutex
	var held []cdpMsg
	f := newFakeCDP(t, func(f *fakeCDP, req cdpMsg) {
		mu.Lock()
		defer mu.Unlock()
		held = append(held, req)
		if len(held) == 2 { // answer the second request first
			f.reply(held[1].ID, map[string]any{"m": held[1].Method})
			f.reply(held[0].ID, map[string]any{"m": held[0].Method})
		}
	})
	c, err := dialCDP(f.url())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var wg sync.WaitGroup
	for _, m := range []string{"A.one", "B.two"} {
		wg.Add(1)
		go func(m string) {
			defer wg.Done()
			res, err := c.Call(context.Background(), m, nil)
			if err != nil || !strings.Contains(string(res), m) {
				t.Errorf("%s got %s (%v)", m, res, err)
			}
		}(m)
		time.Sleep(20 * time.Millisecond) // keep A before B on the wire
	}
	wg.Wait()
}

func TestCDPErrorFieldBecomesError(t *testing.T) {
	f := newFakeCDP(t, func(f *fakeCDP, req cdpMsg) {
		f.send(map[string]any{"id": req.ID, "error": map[string]any{"code": -32000, "message": "No dialog is showing"}})
	})
	c, _ := dialCDP(f.url())
	defer c.Close()
	_, err := c.Call(context.Background(), "Page.handleJavaScriptDialog", nil)
	if err == nil || !strings.Contains(err.Error(), "No dialog is showing") {
		t.Fatalf("want the CDP error message, got %v", err)
	}
}

func TestCDPEventsGoToSubscribers(t *testing.T) {
	f := newFakeCDP(t, func(f *fakeCDP, req cdpMsg) {
		f.send(map[string]any{"method": "Page.javascriptDialogOpening", "params": map[string]any{"type": "confirm", "message": "지울까요?"}})
		f.reply(req.ID, map[string]any{})
	})
	c, _ := dialCDP(f.url())
	defer c.Close()
	got := make(chan string, 1)
	c.OnEvent(func(method string, params json.RawMessage) {
		if method == "Page.javascriptDialogOpening" {
			got <- string(params)
		}
	})
	if _, err := c.Call(context.Background(), "Page.enable", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-got:
		if !strings.Contains(p, "지울까요?") {
			t.Fatalf("event params lost: %s", p)
		}
	case <-time.After(time.Second):
		t.Fatal("event never delivered")
	}
}

func TestCDPDisconnectFailsPendingCallsAtOnce(t *testing.T) {
	f := newFakeCDP(t, func(f *fakeCDP, req cdpMsg) {
		f.mu.Lock()
		_ = f.conn.Close() // never answer; hang up instead
		f.mu.Unlock()
	})
	c, _ := dialCDP(f.url())
	start := time.Now()
	_, err := c.Call(context.Background(), "Runtime.evaluate", nil)
	if err == nil {
		t.Fatal("a call on a dropped connection must fail")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("waited %v — a dead connection must fail its callers promptly", time.Since(start))
	}
	select {
	case <-c.Done():
	case <-time.After(time.Second):
		t.Fatal("Done() must close when the connection drops")
	}
}

func TestDialCDPRefusesNonLoopback(t *testing.T) {
	for _, u := range []string{"ws://10.0.0.5:9222/devtools/page/X", "ws://example.com:9222/x", "http://127.0.0.1:9222/x"} {
		if _, err := dialCDP(u); err == nil {
			t.Errorf("%s: must be refused without connecting", u)
		}
	}
}
