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

func TestAccessTokenExemptsLoopback(t *testing.T) {
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

	// httptest connects over loopback, which is exempt: a same-machine caller
	// keeps working with no token even after one is set to expose the network.
	if code := postCall(t, srv.URL, ""); code != http.StatusOK {
		t.Fatalf("loopback without token should pass: got %d", code)
	}

	// /mcp is likewise open to loopback.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", bytes.NewReader([]byte(`{}`)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("/mcp over loopback should not 401")
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

	// Local callers attach the token by themselves (so a network hop still carries it).
	r, _ := http.NewRequest(http.MethodPost, srv.URL+"/call", nil)
	addTokenHeader(r)
	if r.Header.Get("Authorization") != "Bearer s3cret" {
		t.Fatalf("addTokenHeader: %q", r.Header.Get("Authorization"))
	}
}

// TestRequireTokenNetworkPeer checks the gate for a non-loopback peer: with a
// token configured, a network request must carry the right bearer; loopback is
// exempt. RemoteAddr is set directly so no real network is needed.
func TestRequireTokenNetworkPeer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGLINK_HOME", home)
	forgetTokenCache()
	defer forgetTokenCache()
	if err := os.WriteFile(filepath.Join(home, tokenFileName), []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	forgetTokenCache()

	h := requireToken(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	call := func(remoteAddr, bearer string) int {
		req := httptest.NewRequest(http.MethodPost, "/call", nil)
		req.RemoteAddr = remoteAddr
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := call("203.0.113.5:40000", ""); code != http.StatusUnauthorized {
		t.Fatalf("network peer, no token: got %d", code)
	}
	if code := call("203.0.113.5:40000", "wrong"); code != http.StatusUnauthorized {
		t.Fatalf("network peer, wrong token: got %d", code)
	}
	if code := call("203.0.113.5:40000", "s3cret"); code != http.StatusOK {
		t.Fatalf("network peer, right token: got %d", code)
	}
	if code := call("127.0.0.1:40000", ""); code != http.StatusOK {
		t.Fatalf("loopback peer should be exempt: got %d", code)
	}
}
