package main

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// TestWebServerStartFailSoft asserts Start() does not panic and returns
// promptly when net.Listen fails (malformed addr here), leaving the bot
// alive. A timeout means Start wrongly blocked past the listen-error path.
func TestWebServerStartFailSoft(t *testing.T) {
	s := &webServer{addr: "127.0.0.1:999999", token: "secret", ownerChatID: 7, hub: NewHub(), bot: &Bot{}}

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Start()
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start() blocked past a listen error instead of returning promptly")
	}
}

// TestResolveWebOwner covers the three owner-resolution outcomes used at
// web-chat startup: explicit config wins, else fall back to the first
// allowed user, else web chat is disabled (ok=false).
func TestResolveWebOwner(t *testing.T) {
	if got, ok := resolveWebOwner(42, []int64{1, 2}); got != 42 || !ok {
		t.Errorf("explicit owner: got (%d, %v), want (42, true)", got, ok)
	}
	if got, ok := resolveWebOwner(0, []int64{5, 6}); got != 5 || !ok {
		t.Errorf("fallback to allowed[0]: got (%d, %v), want (5, true)", got, ok)
	}
	if got, ok := resolveWebOwner(0, nil); got != 0 || ok {
		t.Errorf("no owner: got (%d, %v), want (0, false)", got, ok)
	}
}

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

// TestOriginOK_Rejects checks originOK against spoofed/hostile Origin headers
// (subdomain confusables, the sandboxed "null" origin, and a bare foreign
// host) as well as the loopback origins that must still be allowed.
func TestOriginOK_Rejects(t *testing.T) {
	cases := []struct {
		origin string
		want   bool
	}{
		{"null", false},
		{"http://127.0.0.1.evil.com", false},
		{"http://localhost.evil.com", false},
		{"http://evil.com", false},
		{"http://127.0.0.1:1717", true},
		{"http://localhost:1717", true},
		{"", true},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/ws", nil)
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if got := originOK(r); got != c.want {
			t.Errorf("originOK(%q) = %v, want %v", c.origin, got, c.want)
		}
	}
}

// TestWS_Unauthorized asserts the WS handshake is rejected for a wrong or
// missing token, and that no per-chat channel is left registered in the Hub
// as a result of the rejected attempts.
func TestWS_Unauthorized(t *testing.T) {
	b := &Bot{}
	hub := NewHub()
	b.out = hub
	s := &webServer{token: "secret", ownerChatID: 7, hub: hub, bot: b}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")

	dialAndExpectReject := func(name, url string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c, resp, err := websocket.Dial(ctx, url, nil)
		if err == nil {
			c.Close(websocket.StatusNormalClosure, "")
			t.Errorf("%s: expected dial to be rejected, got success", name)
			return
		}
		if resp != nil && resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: response status = %d, want %d", name, resp.StatusCode, http.StatusUnauthorized)
		}
	}

	dialAndExpectReject("wrong token", "ws://"+host+"/ws?token=wrong")
	dialAndExpectReject("no token", "ws://"+host+"/ws")

	if n := len(hub.perChat[7]); n != 0 {
		t.Errorf("hub.perChat[7] has %d channel(s) after rejected handshakes, want 0", n)
	}
}

// TestUpload_Unauthorized asserts /api/upload rejects wrong/missing tokens
// and non-POST methods with 401, and never reaches ingestAttachment
// (dispatchHook) for any rejected request.
func TestUpload_Unauthorized(t *testing.T) {
	var dispatchCount int
	b := &Bot{}
	b.out = NewHub()
	b.dispatchHook = func(_ int64, _ string) { dispatchCount++ }
	s := &webServer{ownerChatID: 7, token: "secret", bot: b, hub: b.out}

	newUploadBody := func() (*bytes.Buffer, string) {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		fw, _ := mw.CreateFormFile("file", "note.txt")
		_, _ = fw.Write([]byte("hello"))
		_ = mw.Close()
		return &body, mw.FormDataContentType()
	}

	// Wrong token, POST.
	body, ct := newUploadBody()
	r := httptest.NewRequest(http.MethodPost, "/api/upload?token=wrong", body)
	r.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	s.handleUpload(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: status = %d, want 401", w.Code)
	}

	// No token, POST.
	body, ct = newUploadBody()
	r = httptest.NewRequest(http.MethodPost, "/api/upload", body)
	r.Header.Set("Content-Type", ct)
	w = httptest.NewRecorder()
	s.handleUpload(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", w.Code)
	}

	// Valid token, wrong method (GET): method guard must reject before auth
	// would otherwise allow it through.
	r = httptest.NewRequest(http.MethodGet, "/api/upload?token=secret", nil)
	w = httptest.NewRecorder()
	s.handleUpload(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong method: status = %d, want 401", w.Code)
	}

	if dispatchCount != 0 {
		t.Errorf("dispatchHook called %d time(s) on rejected requests, want 0", dispatchCount)
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

// TestWebChannelBackpressure proves a slow/dead client is dropped rather than
// blocking the Hub: once the small send buffer is full, further pushes must
// not block, and the overflow must close the channel (via cancel) exactly
// once even across multiple overflow pushes.
func TestWebChannelBackpressure(t *testing.T) {
	send := make(chan wsFrame, 1) // small buffer, deliberately never drained
	var cancelCalls int32
	done := make(chan struct{})
	ch := &webChannel{
		send: send,
		cancel: func() {
			atomic.AddInt32(&cancelCalls, 1)
			close(done)
		},
	}

	// Fill the one-slot buffer; no reader goroutine drains it.
	ch.push(wsFrame{Type: "text", Text: "1"})

	// Further pushes/Sends must not block even though the buffer stays full
	// and the overflow path is hit repeatedly.
	overflowDone := make(chan struct{})
	go func() {
		ch.push(wsFrame{Type: "text", Text: "2"}) // overflow #1 -> triggers close()
		_ = ch.Send(7, "overflow-3")              // overflow #2 -> close() again, but closeOnce guards
		close(overflowDone)
	}()

	select {
	case <-overflowDone:
	case <-time.After(2 * time.Second):
		t.Fatal("push/Send blocked on a full buffer; overflow must be non-blocking")
	}

	select {
	case <-done:
	default:
		t.Fatal("overflow push should have triggered close()/cancel")
	}

	if got := atomic.LoadInt32(&cancelCalls); got != 1 {
		t.Errorf("cancel must be invoked exactly once via closeOnce even across multiple overflow pushes, got %d", got)
	}
}

// TestWSFrameJSONTags locks the wire protocol between the server and app.js:
// field names and omitempty behavior must not drift silently.
func TestWSFrameJSONTags(t *testing.T) {
	imgB, err := json.Marshal(wsFrame{Type: "image", Caption: "c", Data: "ZGF0YQ=="})
	if err != nil {
		t.Fatal(err)
	}
	var imgKeys map[string]json.RawMessage
	if err := json.Unmarshal(imgB, &imgKeys); err != nil {
		t.Fatal(err)
	}
	wantImgKeys := map[string]bool{"type": true, "caption": true, "data": true}
	if len(imgKeys) != len(wantImgKeys) {
		t.Fatalf("image frame keys = %v, want exactly %v", imgKeys, wantImgKeys)
	}
	for k := range imgKeys {
		if !wantImgKeys[k] {
			t.Errorf("unexpected key %q in image frame JSON: %s", k, imgB)
		}
	}
	if _, ok := imgKeys["text"]; ok {
		t.Errorf("empty text must be omitted via omitempty, got: %s", imgB)
	}

	textB, err := json.Marshal(wsFrame{Type: "text", Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	var textKeys map[string]json.RawMessage
	if err := json.Unmarshal(textB, &textKeys); err != nil {
		t.Fatal(err)
	}
	wantTextKeys := map[string]bool{"type": true, "text": true}
	if len(textKeys) != len(wantTextKeys) {
		t.Fatalf("text frame keys = %v, want exactly %v", textKeys, wantTextKeys)
	}
	for k := range textKeys {
		if !wantTextKeys[k] {
			t.Errorf("unexpected key %q in text frame JSON: %s", k, textB)
		}
	}
}
