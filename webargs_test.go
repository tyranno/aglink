package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebSystemPrompt(t *testing.T) {
	p := webSystemPrompt()
	for _, kw := range []string{"list_tabs", "navigate", "get_page_text"} {
		if !strings.Contains(p, kw) {
			t.Errorf("webSystemPrompt() missing keyword %q", kw)
		}
	}
}

func TestResolveWebBinaryPath(t *testing.T) {
	// Isolate PATH so the real machine's PATH can't mask the unresolved cases.
	t.Setenv("PATH", "")

	dir := t.TempDir()
	self := filepath.Join(dir, "teleclaude"+exeSuffix)
	agl := filepath.Join(dir, "aglink-web"+exeSuffix)

	// An explicit override that exists wins regardless of selfExe.
	override := filepath.Join(dir, "custom-aglink-web"+exeSuffix)
	if err := os.WriteFile(override, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveWebBinaryPath(&Config{WebBinaryPath: override}, self); got != override {
		t.Errorf("override: got %q, want %q", got, override)
	}
	// A missing override falls through rather than being handed downstream.
	if got := resolveWebBinaryPath(&Config{WebBinaryPath: filepath.Join(dir, "gone"+exeSuffix)}, self); got != "" {
		t.Errorf("missing override should fall through: got %q, want \"\"", got)
	}
	// No override, no selfExe → unresolved.
	if got := resolveWebBinaryPath(&Config{}, ""); got != "" {
		t.Errorf("no selfExe: got %q, want \"\"", got)
	}
	// No override → aglink-web next to selfExe, but only when it exists.
	if got := resolveWebBinaryPath(&Config{}, self); got != "" {
		t.Errorf("default (binary absent): got %q, want \"\"", got)
	}
	if err := os.WriteFile(agl, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveWebBinaryPath(&Config{}, self); got != agl {
		t.Errorf("default (binary present): got %q, want %q", got, agl)
	}
}

// The web binary is also found on PATH when nothing else resolves.
func TestResolveWebBinaryPath_PathFallback(t *testing.T) {
	dir := t.TempDir()
	pathDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(pathDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pathBin := filepath.Join(pathDir, "aglink-web"+exeSuffix)
	if err := os.WriteFile(pathBin, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir)

	self := filepath.Join(dir, "teleclaude"+exeSuffix)
	if got := resolveWebBinaryPath(&Config{}, self); got != pathBin {
		t.Errorf("PATH fallback: got %q, want %q", got, pathBin)
	}
}
