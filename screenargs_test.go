package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScreenSystemPrompt(t *testing.T) {
	p := screenSystemPrompt()
	for _, kw := range []string{"snapshot", "UIA", "screenshot", "preset"} {
		if !strings.Contains(p, kw) {
			t.Errorf("screenSystemPrompt() missing keyword %q", kw)
		}
	}
}

func TestResolveScreenBinaryPath(t *testing.T) {
	// Explicit override wins regardless of selfExe.
	if got := resolveScreenBinaryPath(&Config{ScreenBinaryPath: "C:\\custom\\aglink-screen.exe"}, "C:\\t\\teleclaude.exe"); got != "C:\\custom\\aglink-screen.exe" {
		t.Errorf("override: got %q", got)
	}
	// No override, no selfExe → unresolved.
	if got := resolveScreenBinaryPath(&Config{}, ""); got != "" {
		t.Errorf("no selfExe: got %q, want \"\"", got)
	}
	// No override → aglink-screen next to selfExe, but only when it exists.
	dir := t.TempDir()
	self := filepath.Join(dir, "teleclaude"+exeSuffix)
	agl := filepath.Join(dir, "aglink-screen"+exeSuffix)
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
