package main

import "testing"

func TestVersionPayload_ReportsRunningVersion(t *testing.T) {
	p := versionPayload("claude")
	v, ok := p["version"].(string)
	if !ok || v == "" {
		t.Fatalf("version missing/empty: %v", p["version"])
	}
	if p["backend"] != "claude" {
		t.Errorf("backend = %v, want claude", p["backend"])
	}
	// commitCount is always present (0 for a dev build).
	if _, ok := p["commitCount"].(int); !ok {
		t.Errorf("commitCount missing or not int: %v", p["commitCount"])
	}
}

func TestVersionPayload_OmitsBackendWhenEmpty(t *testing.T) {
	p := versionPayload("")
	if _, present := p["backend"]; present {
		t.Errorf("backend should be omitted when empty, got %v", p["backend"])
	}
}
