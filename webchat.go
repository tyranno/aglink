package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// loadOrCreateWebToken returns cfgToken if set, otherwise reads (or creates and
// persists) ~/.teleclaude/web_chat.token with 0600 perms.
func loadOrCreateWebToken(cfgToken string) (string, error) {
	if cfgToken != "" {
		return cfgToken, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(home, ".teleclaude", "web_chat.token")
	if b, rerr := os.ReadFile(p); rerr == nil {
		if tok := strings.TrimSpace(string(b)); tok != "" {
			return tok, nil
		}
	}
	buf := make([]byte, 24)
	if _, rerr := rand.Read(buf); rerr != nil {
		return "", rerr
	}
	tok := hex.EncodeToString(buf)
	_ = os.WriteFile(p, []byte(tok), 0o600)
	return tok, nil
}

// tokenOK checks the request token (query ?token= or "Authorization: Bearer <t>")
// against want using a constant-time comparison.
func tokenOK(r *http.Request, want string) bool {
	got := r.URL.Query().Get("token")
	if got == "" {
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			got = strings.TrimPrefix(h, "Bearer ")
		}
	}
	if got == "" || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// originOK guards against cross-site requests: allow when there is no Origin
// header (non-browser / same-origin) or when the Origin host is loopback.
func originOK(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "127.0.0.1" || host == "localhost"
}
