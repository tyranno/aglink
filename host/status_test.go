package main

import "testing"

func TestBuildAuxFeatures_UnifiedStates(t *testing.T) {
	// relayClients=1 → aglink-chat running; chat_control disabled here just sets
	// the relay detail, not the state.
	feats := buildAuxFeatures(1, false, "127.0.0.1:17170")

	// Expected: aglink-chat relay + each non-aglink-chat plugin (aglink-chat is in
	// pluginNames for !update builds but shown once, as the relay entry).
	want := 1
	for _, n := range pluginNames {
		if n != "aglink-chat" {
			want++
		}
	}
	if len(feats) != want {
		t.Fatalf("got %d features, want %d", len(feats), want)
	}

	valid := map[string]bool{auxRunning: true, auxIdle: true, auxAbsent: true}
	var chatState string
	found := false
	for _, f := range feats {
		if !valid[f.State] {
			t.Errorf("feature %q has invalid state %q", f.Name, f.State)
		}
		if f.Name == "aglink-chat" {
			chatState = f.State
			found = true
		}
	}
	if !found {
		t.Fatal("aglink-chat feature missing")
	}
	if chatState != auxRunning {
		t.Errorf("aglink-chat state = %q, want running (1 relay client connected)", chatState)
	}
}

func TestBuildAuxFeatures_ChatIdleWhenNoRelay(t *testing.T) {
	feats := buildAuxFeatures(0, false, "")
	for _, f := range feats {
		if f.Name == "aglink-chat" {
			if f.State != auxIdle {
				t.Errorf("aglink-chat with no relay should be idle (never absent/error), got %q", f.State)
			}
			return
		}
	}
	t.Fatal("aglink-chat feature missing")
}
