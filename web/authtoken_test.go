package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func postCall(t *testing.T, url, bearer string) int {
	t.Helper()
	body, _ := json.Marshal(callRequest{Method: "list_profiles"})
	req, _ := http.NewRequest(http.MethodPost, url+"/call", bytes.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestAccessTokenGuardsCallAndMCP(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGLINK_HOME", home)
	forgetTokenCache()
	defer forgetTokenCache()

	d := newDaemon("")
	srv := httptest.NewServer(d.handler())
	defer srv.Close()

	// No token file: open, as before.
	if code := postCall(t, srv.URL, ""); code != http.StatusOK {
		t.Fatalf("no token configured: got %d", code)
	}

	if err := os.WriteFile(filepath.Join(home, tokenFileName), []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	forgetTokenCache()

	if code := postCall(t, srv.URL, ""); code != http.StatusUnauthorized {
		t.Fatalf("missing token: got %d", code)
	}
	if code := postCall(t, srv.URL, "wrong"); code != http.StatusUnauthorized {
		t.Fatalf("wrong token: got %d", code)
	}
	if code := postCall(t, srv.URL, "s3cret"); code != http.StatusOK {
		t.Fatalf("right token: got %d", code)
	}

	// /mcp is guarded the same way.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", bytes.NewReader([]byte(`{}`)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/mcp without token: got %d", resp.StatusCode)
	}

	// /health stays open (ensureDaemon uses it).
	h, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	h.Body.Close()
	if h.StatusCode != http.StatusOK {
		t.Fatalf("/health: got %d", h.StatusCode)
	}

	// Local callers attach the token by themselves.
	r, _ := http.NewRequest(http.MethodPost, srv.URL+"/call", nil)
	addTokenHeader(r)
	if r.Header.Get("Authorization") != "Bearer s3cret" {
		t.Fatalf("addTokenHeader: %q", r.Header.Get("Authorization"))
	}
}
