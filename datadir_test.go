package main

import (
	"testing"
)

func TestConfiguredPort(t *testing.T) {
	t.Setenv(portEnv, "")
	if got := configuredPort(); got != defaultPort {
		t.Fatalf("empty env: got %d want %d", got, defaultPort)
	}
	t.Setenv(portEnv, "51000")
	if got := configuredPort(); got != 51000 {
		t.Fatalf("valid env: got %d want 51000", got)
	}
	t.Setenv(portEnv, "not-a-port")
	if got := configuredPort(); got != defaultPort {
		t.Fatalf("invalid env falls back: got %d want %d", got, defaultPort)
	}
	t.Setenv(portEnv, "99999")
	if got := configuredPort(); got != defaultPort {
		t.Fatalf("out-of-range env falls back: got %d want %d", got, defaultPort)
	}
}

func TestPortFileRoundTrip(t *testing.T) {
	// Redirect the home dir (Windows uses USERPROFILE, unix uses HOME) so the
	// port file lands in an isolated temp dir, not the real ~/.teleclaude.
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)
	t.Setenv(portEnv, "") // so the fallback is the fixed default

	if err := writePort(50123); err != nil {
		t.Fatalf("writePort: %v", err)
	}
	if got := readPort(); got != 50123 {
		t.Fatalf("readPort round-trip: got %d want 50123", got)
	}
}

func TestReadPortFallsBackWhenMissing(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)
	t.Setenv(portEnv, "")
	if got := readPort(); got != defaultPort {
		t.Fatalf("missing port file: got %d want %d", got, defaultPort)
	}
}
