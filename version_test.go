package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestWebServer(t *testing.T) *webServer {
	t.Helper()
	return &webServer{addr: "127.0.0.1:0", token: "tok"}
}

func authGet(t *testing.T, s *webServer, h http.HandlerFunc, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer tok")
	rr := httptest.NewRecorder()
	h(rr, req)
	return rr
}

func TestHandleCapabilities_AdminTrue(t *testing.T) {
	s := newTestWebServer(t)
	rr := authGet(t, s, s.handleCapabilities, "/api/capabilities")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var body struct {
		Admin   bool   `json:"admin"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !body.Admin {
		t.Errorf("admin = false, want true")
	}
}

func TestHandleCapabilities_Unauthorized(t *testing.T) {
	s := newTestWebServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/capabilities", nil) // no token
	rr := httptest.NewRecorder()
	s.handleCapabilities(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestHandleVersion_ReportsBuildVars(t *testing.T) {
	s := newTestWebServer(t)
	rr := authGet(t, s, s.handleVersion, "/api/version")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var body struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Version == "" {
		t.Errorf("version empty, want at least %q", "dev")
	}
}
