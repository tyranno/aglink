//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPreferNativeClaude verifies the claude.cmd → bin\claude.exe unwrapping so
// workers exec claude directly instead of through cmd.exe (which mangles the
// plugin MCP args). See preferNativeClaude for the why.
func TestPreferNativeClaude(t *testing.T) {
	dir := t.TempDir()
	shim := filepath.Join(dir, "claude.cmd")
	if err := os.WriteFile(shim, []byte("@echo off\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exeDir := filepath.Join(dir, "node_modules", "@anthropic-ai", "claude-code", "bin")
	if err := os.MkdirAll(exeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(exeDir, "claude.exe")
	if err := os.WriteFile(exe, []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}

	// .cmd with the native exe present → unwrapped to the exe.
	if got := preferNativeClaude(shim); got != exe {
		t.Errorf("preferNativeClaude(%q) = %q, want %q", shim, got, exe)
	}

	// Already a native .exe → returned unchanged.
	if got := preferNativeClaude(exe); got != exe {
		t.Errorf("native exe should pass through unchanged, got %q", got)
	}

	// .cmd but no native exe in the expected layout → shim kept as-is.
	lonelyShim := filepath.Join(t.TempDir(), "claude.cmd")
	if err := os.WriteFile(lonelyShim, []byte("@echo off\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := preferNativeClaude(lonelyShim); got != lonelyShim {
		t.Errorf("missing native exe should keep the shim, got %q", got)
	}
}
