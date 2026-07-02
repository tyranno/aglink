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
	// Explicit override wins regardless of selfExe.
	if got := resolveWebBinaryPath(&Config{WebBinaryPath: "C:\\custom\\aglink-web.exe"}, "C:\\t\\teleclaude.exe"); got != "C:\\custom\\aglink-web.exe" {
		t.Errorf("override: got %q", got)
	}
	// No override, no selfExe → unresolved.
	if got := resolveWebBinaryPath(&Config{}, ""); got != "" {
		t.Errorf("no selfExe: got %q, want \"\"", got)
	}
	// No override → aglink-web next to selfExe, but only when it exists.
	dir := t.TempDir()
	self := filepath.Join(dir, "teleclaude"+exeSuffix)
	agl := filepath.Join(dir, "aglink-web"+exeSuffix)
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
