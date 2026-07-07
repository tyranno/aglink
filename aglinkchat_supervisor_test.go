package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveAglinkChatBinary_Priority(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "teleclaude")
	sibling := filepath.Join(dir, "aglink-chat")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	selfExe := filepath.Join(srcDir, "teleclaude"+exeSuffix)
	name := "aglink-chat" + exeSuffix

	// Nothing present → "".
	if got := resolveAglinkChatBinary(&Config{}, selfExe); got != "" {
		t.Errorf("no binary anywhere: got %q, want \"\"", got)
	}

	// Sibling only.
	siblingBin := filepath.Join(sibling, name)
	if err := os.WriteFile(siblingBin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveAglinkChatBinary(&Config{}, selfExe); got != siblingBin {
		t.Errorf("sibling only: got %q, want %q", got, siblingBin)
	}

	// srcDir binary wins over sibling.
	srcBin := filepath.Join(srcDir, name)
	if err := os.WriteFile(srcBin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveAglinkChatBinary(&Config{}, selfExe); got != srcBin {
		t.Errorf("srcDir should win: got %q, want %q", got, srcBin)
	}

	// Explicit config path wins over everything (when it exists).
	explicit := filepath.Join(dir, "custom-"+name)
	if err := os.WriteFile(explicit, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveAglinkChatBinary(&Config{AglinkChatBinaryPath: explicit}, selfExe); got != explicit {
		t.Errorf("explicit path should win: got %q, want %q", got, explicit)
	}
}
