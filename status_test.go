package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleStatus_ReportsConfigAndConnections(t *testing.T) {
	cfg := &Config{WebChatAddr: "127.0.0.1:1717", ChatControl: true, ChatControlAddr: "127.0.0.1:17170"}
	cs := &chatControlServer{}
	cs.connCount.Add(1) // one aglink-chat connected
	s := &webServer{token: "tok", holder: NewConfigHolder(cfg), control: cs}

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.Header.Set("Authorization", "Bearer tok")
	rr := httptest.NewRecorder()
	s.handleStatus(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}

	var body struct {
		WebChatAddr     string `json:"webChatAddr"`
		AglinkClients   int    `json:"aglinkClients"`
		AglinkConnected bool   `json:"aglinkConnected"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.WebChatAddr != "127.0.0.1:1717" {
		t.Errorf("addr = %q", body.WebChatAddr)
	}
	if body.AglinkClients != 1 || !body.AglinkConnected {
		t.Errorf("clients=%d connected=%v", body.AglinkClients, body.AglinkConnected)
	}
}

func TestHandlePlugins_ListsConfiguredPlugins(t *testing.T) {
	s := &webServer{token: "tok"}
	req := httptest.NewRequest(http.MethodGet, "/api/plugins", nil)
	req.Header.Set("Authorization", "Bearer tok")
	rr := httptest.NewRecorder()
	s.handlePlugins(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	var body struct {
		Plugins []struct {
			Name string `json:"name"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body.Plugins) != len(pluginNames) {
		t.Errorf("got %d plugins, want %d", len(body.Plugins), len(pluginNames))
	}

	// Unauthorized → 401.
	noauth := httptest.NewRequest(http.MethodGet, "/api/plugins", nil)
	nrr := httptest.NewRecorder()
	s.handlePlugins(nrr, noauth)
	if nrr.Code != http.StatusUnauthorized {
		t.Errorf("no-token status = %d, want 401", nrr.Code)
	}
}
