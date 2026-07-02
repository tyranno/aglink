package main

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestTokenOK(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/ws?token=secret", nil)
	if !tokenOK(r, "secret") {
		t.Error("query token should match")
	}
	if tokenOK(r, "other") {
		t.Error("wrong token must fail")
	}
	r2 := httptest.NewRequest(http.MethodGet, "/api/upload", nil)
	r2.Header.Set("Authorization", "Bearer secret")
	if !tokenOK(r2, "secret") {
		t.Error("bearer token should match")
	}
	r3 := httptest.NewRequest(http.MethodGet, "/ws", nil)
	if tokenOK(r3, "secret") {
		t.Error("missing token must fail")
	}
}

func TestLoadOrCreateWebToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	tok1, err := loadOrCreateWebToken("")
	if err != nil {
		t.Fatalf("first call: unexpected error: %v", err)
	}
	if tok1 == "" {
		t.Fatal("first call: expected non-empty token")
	}

	tok2, err := loadOrCreateWebToken("")
	if err != nil {
		t.Fatalf("second call: unexpected error: %v", err)
	}
	if tok2 != tok1 {
		t.Errorf("token not persisted across calls: first=%q second=%q", tok1, tok2)
	}

	if got, err := loadOrCreateWebToken("explicit"); err != nil || got != "explicit" {
		t.Errorf("loadOrCreateWebToken(%q) = (%q, %v), want (%q, nil)", "explicit", got, err, "explicit")
	}
}

func TestOriginOK(t *testing.T) {
	cases := []struct {
		origin string
		want   bool
	}{
		{"", true},
		{"http://127.0.0.1:1717", true},
		{"http://localhost:1717", true},
		{"http://localhost", true},
		{"http://evil.com", false},
		{"https://example.org:1717", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/ws", nil)
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if got := originOK(r); got != c.want {
			t.Errorf("originOK(%q)=%v, want %v", c.origin, got, c.want)
		}
	}
}

func TestWebInjectRouting(t *testing.T) {
	var gotCmd, gotText string
	b := &Bot{}
	b.out = NewHub()
	b.commandHook = func(_ int64, text string) { gotCmd = text }
	b.dispatchHook = func(_ int64, text string) { gotText = text }
	s := &webServer{ownerChatID: 7, bot: b, hub: b.out}

	s.inject("!help")
	s.inject("hello world")

	if gotCmd != "!help" {
		t.Errorf("command not routed to handleCommand, got %q", gotCmd)
	}
	if gotText != "hello world" {
		t.Errorf("text not routed to dispatchText, got %q", gotText)
	}
}

func TestWebInjectRateLimited(t *testing.T) {
	var dispatchCount int
	b := &Bot{}
	b.out = NewHub()
	b.rateLimiter = NewRateLimiter(1) // allow 1 per minute
	b.dispatchHook = func(_ int64, _ string) { dispatchCount++ }
	s := &webServer{ownerChatID: 7, bot: b, hub: b.out}

	s.inject("hi")
	s.inject("hi again")

	if dispatchCount != 1 {
		t.Errorf("dispatch count = %d, want 1 (second call should have been rate-limited)", dispatchCount)
	}
}

func TestHandleUpload_Ingests(t *testing.T) {
	var gotText string
	b := &Bot{}
	b.out = NewHub()
	b.dispatchHook = func(_ int64, text string) { gotText = text }
	s := &webServer{ownerChatID: 7, token: "secret", bot: b, hub: b.out}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("caption", "이거 봐줘")
	fw, _ := mw.CreateFormFile("file", "note.txt")
	_, _ = fw.Write([]byte("hello"))
	_ = mw.Close()

	r := httptest.NewRequest(http.MethodPost, "/api/upload?token=secret", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	s.handleUpload(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	// The ingest prompt must contain the caption and the saved path (ends in .txt).
	if !strings.Contains(gotText, "이거 봐줘") || !strings.Contains(gotText, ".txt]") {
		t.Errorf("ingest prompt = %q", gotText)
	}
}

// TestWSRoundTrip exercises the full browser <-> server pipeline over a real
// WebSocket connection: an inbound {"type":"send"} frame must reach the bot's
// dispatch pipeline, and a Hub broadcast to the registered chatID must arrive
// back at the client as a {"type":"text"} frame.
func TestWSRoundTrip(t *testing.T) {
	dispatched := make(chan string, 1)

	b := &Bot{}
	hub := NewHub()
	b.out = hub
	b.dispatchHook = func(_ int64, text string) { dispatched <- text }
	s := &webServer{token: "secret", ownerChatID: 7, hub: hub, bot: b}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	host := strings.TrimPrefix(srv.URL, "http://")
	c, _, err := websocket.Dial(ctx, "ws://"+host+"/ws?token=secret", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	if werr := wsjson.Write(ctx, c, inMsg{Type: "send", Text: "hello"}); werr != nil {
		t.Fatalf("write: %v", werr)
	}

	select {
	case text := <-dispatched:
		if text != "hello" {
			t.Errorf("dispatched text = %q, want %q", text, "hello")
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for inbound frame to reach dispatchHook")
	}

	// By the time dispatchHook fired, the server's reader loop was already
	// running, which only starts after hub.Register — so the channel is
	// guaranteed registered before this broadcast.
	_ = s.hub.Send(7, "reply")

	var frame wsFrame
	if rerr := wsjson.Read(ctx, c, &frame); rerr != nil {
		t.Fatalf("read: %v", rerr)
	}
	if frame.Type != "text" || frame.Text != "reply" {
		t.Errorf("broadcast frame = %+v, want {Type:text Text:reply}", frame)
	}
}
