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

func TestHandleAux_ListsUnifiedFeatures(t *testing.T) {
	// No control server + chat_control disabled → aglink-chat is idle (never absent).
	s := &webServer{token: "tok", holder: NewConfigHolder(&Config{})}
	req := httptest.NewRequest(http.MethodGet, "/api/aux", nil)
	req.Header.Set("Authorization", "Bearer tok")
	rr := httptest.NewRecorder()
	s.handleAux(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	var body struct {
		Features []struct {
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"features"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Expected: aglink-chat relay + each non-aglink-chat plugin (aglink-chat is
	// in pluginNames for !update builds but shown once, as the relay).
	want := 1
	for _, n := range pluginNames {
		if n != "aglink-chat" {
			want++
		}
	}
	if len(body.Features) != want {
		t.Fatalf("got %d features, want %d", len(body.Features), want)
	}
	valid := map[string]bool{auxRunning: true, auxIdle: true, auxAbsent: true}
	var chat *string
	for i := range body.Features {
		f := body.Features[i]
		if !valid[f.State] {
			t.Errorf("feature %q has invalid state %q", f.Name, f.State)
		}
		if f.Name == "aglink-chat" {
			chat = &body.Features[i].State
		}
	}
	if chat == nil {
		t.Fatal("aglink-chat feature missing")
	}
	if *chat != auxIdle {
		t.Errorf("aglink-chat state = %q, want idle (never absent/error)", *chat)
	}

	// Unauthorized → 401.
	noauth := httptest.NewRequest(http.MethodGet, "/api/aux", nil)
	nrr := httptest.NewRecorder()
	s.handleAux(nrr, noauth)
	if nrr.Code != http.StatusUnauthorized {
		t.Errorf("no-token status = %d, want 401", nrr.Code)
	}
}
