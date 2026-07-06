package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveHomeDir_DefaultCreatesTeleclaudeUnderHome(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("USERPROFILE", tmpHome) // Windows
	t.Setenv("HOME", tmpHome)        // Unix
	cfg := &Config{}                 // no HomeDir → default
	got := resolveHomeDir(cfg)
	want := filepath.Join(tmpHome, "teleclaude")
	if got != want {
		t.Fatalf("default home = %q, want %q", got, want)
	}
	if fi, err := os.Stat(got); err != nil || !fi.IsDir() {
		t.Errorf("resolveHomeDir must create the directory, stat err=%v", err)
	}
}

func TestResolveHomeDir_ExplicitOverride(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "myhome")
	cfg := &Config{HomeDir: dir}
	got := resolveHomeDir(cfg)
	if got != dir {
		t.Errorf("explicit HomeDir should be used, got %q want %q", got, dir)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Errorf("explicit HomeDir must be created, err=%v", err)
	}
}
