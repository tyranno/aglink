package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveHomeDir_DefaultCreatesTeleclaudeUnderHome(t *testing.T) {
	cfg := &Config{} // no HomeDir → default
	got := resolveHomeDir(cfg)
	if !strings.HasSuffix(filepath.Clean(got), filepath.Join("", "teleclaude")) && filepath.Base(got) != "teleclaude" {
		t.Fatalf("default home should end in /teleclaude, got %q", got)
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
