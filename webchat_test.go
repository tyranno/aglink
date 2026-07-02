package main

import (
	"net/http"
	"net/http/httptest"
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
