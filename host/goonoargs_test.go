package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoonoSystemPrompt(t *testing.T) {
	p := goonoSystemPrompt()
	for _, kw := range []string{"list_projects", "search_files", "upload_document"} {
		if !strings.Contains(p, kw) {
			t.Errorf("goonoSystemPrompt() missing keyword %q", kw)
		}
	}
}

func TestResolveGoonoBinaryPath(t *testing.T) {
	// Isolate PATH so the real machine's PATH can't mask the unresolved cases.
	t.Setenv("PATH", "")

	dir := t.TempDir()
	self := filepath.Join(dir, "aglink"+exeSuffix)
	agl := filepath.Join(dir, "goono-mcp"+exeSuffix)

	// An explicit override that exists wins regardless of selfExe.
	override := filepath.Join(dir, "custom-goono-mcp"+exeSuffix)
	if err := os.WriteFile(override, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveGoonoBinaryPath(&Config{GoonoBinaryPath: override}, self); got != override {
		t.Errorf("override: got %q, want %q", got, override)
	}
	// A missing override falls through rather than being handed downstream.
	if got := resolveGoonoBinaryPath(&Config{GoonoBinaryPath: filepath.Join(dir, "gone"+exeSuffix)}, self); got != "" {
		t.Errorf("missing override should fall through: got %q, want \"\"", got)
	}
	// No override, no selfExe → unresolved.
	if got := resolveGoonoBinaryPath(&Config{}, ""); got != "" {
		t.Errorf("no selfExe: got %q, want \"\"", got)
	}
	// No override → goono-mcp next to selfExe, but only when it exists.
	if got := resolveGoonoBinaryPath(&Config{}, self); got != "" {
		t.Errorf("default (binary absent): got %q, want \"\"", got)
	}
	if err := os.WriteFile(agl, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveGoonoBinaryPath(&Config{}, self); got != agl {
		t.Errorf("default (binary present): got %q, want %q", got, agl)
	}
}

// The goono binary is also found on PATH when nothing else resolves.
func TestResolveGoonoBinaryPath_PathFallback(t *testing.T) {
	dir := t.TempDir()
	pathDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(pathDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pathBin := filepath.Join(pathDir, "goono-mcp"+exeSuffix)
	if err := os.WriteFile(pathBin, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir)

	self := filepath.Join(dir, "aglink"+exeSuffix)
	if got := resolveGoonoBinaryPath(&Config{}, self); got != pathBin {
		t.Errorf("PATH fallback: got %q, want %q", got, pathBin)
	}
}
