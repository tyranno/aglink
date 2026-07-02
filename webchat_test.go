package main

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
