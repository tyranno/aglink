package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScreenSystemPrompt(t *testing.T) {
	p := screenSystemPrompt()
	for _, kw := range []string{"snapshot", "UIA", "screenshot", "preset", "SCREEN_BUSY", "control_status"} {
		if !strings.Contains(p, kw) {
			t.Errorf("screenSystemPrompt() missing keyword %q", kw)
		}
	}
}

func TestResolveScreenBinaryPath(t *testing.T) {
	// Isolate PATH: the real machine's PATH must not satisfy the lookup and mask
	// the "unresolved" cases below.
	t.Setenv("PATH", "")

	dir := t.TempDir()
	self := filepath.Join(dir, "teleclaude"+exeSuffix)
	agl := filepath.Join(dir, "aglink-screen"+exeSuffix)

	// An explicit override that exists wins regardless of selfExe.
	override := filepath.Join(dir, "custom-aglink-screen"+exeSuffix)
	if err := os.WriteFile(override, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveScreenBinaryPath(&Config{ScreenBinaryPath: override}, self); got != override {
		t.Errorf("override: got %q, want %q", got, override)
	}
	// An override pointing at a missing file falls through instead of being
	// handed downstream to fail as an opaque exec error.
	if got := resolveScreenBinaryPath(&Config{ScreenBinaryPath: filepath.Join(dir, "gone"+exeSuffix)}, self); got != "" {
		t.Errorf("missing override should fall through: got %q, want \"\"", got)
	}
	// No override, no selfExe → unresolved.
	if got := resolveScreenBinaryPath(&Config{}, ""); got != "" {
		t.Errorf("no selfExe: got %q, want \"\"", got)
	}
	// No override → aglink-screen next to selfExe, but only when it exists.
	if got := resolveScreenBinaryPath(&Config{}, self); got != "" {
		t.Errorf("default (binary absent): got %q, want \"\"", got)
	}
	if err := os.WriteFile(agl, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveScreenBinaryPath(&Config{}, self); got != agl {
		t.Errorf("default (binary present): got %q, want %q", got, agl)
	}
}

// The screen binary is also found on PATH when nothing else resolves, so a
// system-installed aglink-screen works with no config.
func TestResolveScreenBinaryPath_PathFallback(t *testing.T) {
	dir := t.TempDir()
	pathDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(pathDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pathBin := filepath.Join(pathDir, "aglink-screen"+exeSuffix)
	if err := os.WriteFile(pathBin, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir)

	self := filepath.Join(dir, "teleclaude"+exeSuffix)
	if got := resolveScreenBinaryPath(&Config{}, self); got != pathBin {
		t.Errorf("PATH fallback: got %q, want %q", got, pathBin)
	}
}
