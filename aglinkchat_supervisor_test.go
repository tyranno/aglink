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

	// Isolate PATH so the real machine's PATH can't satisfy the lookup and mask
	// the "nothing present" case below.
	t.Setenv("PATH", "")

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

// TestResolveAglinkChatBinary_PathFallback covers the last resort: when neither
// config nor the sibling layouts have the binary, a copy on PATH is used, so a
// system-installed aglink-chat runs with no config at all.
func TestResolveAglinkChatBinary_PathFallback(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "teleclaude")
	pathDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(pathDir, 0o755); err != nil {
		t.Fatal(err)
	}
	selfExe := filepath.Join(srcDir, "teleclaude"+exeSuffix)
	name := "aglink-chat" + exeSuffix

	pathBin := filepath.Join(pathDir, name)
	if err := os.WriteFile(pathBin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir)

	got := resolveAglinkChatBinary(&Config{}, selfExe)
	if got != pathBin {
		t.Errorf("PATH fallback: got %q, want %q", got, pathBin)
	}

	// A binary next to teleclaude still wins over PATH.
	srcBin := filepath.Join(srcDir, name)
	if err := os.WriteFile(srcBin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveAglinkChatBinary(&Config{}, selfExe); got != srcBin {
		t.Errorf("sibling should beat PATH: got %q, want %q", got, srcBin)
	}
}

// A configured-but-missing binary_path must not dead-end: it falls through to
// the sibling/PATH lookup instead of returning "".
func TestResolveAglinkChatBinary_MissingConfiguredPathFallsThrough(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "teleclaude")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	selfExe := filepath.Join(srcDir, "teleclaude"+exeSuffix)
	srcBin := filepath.Join(srcDir, "aglink-chat"+exeSuffix)
	if err := os.WriteFile(srcBin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")

	cfg := &Config{AglinkChatBinaryPath: filepath.Join(dir, "does-not-exist"+exeSuffix)}
	if got := resolveAglinkChatBinary(cfg, selfExe); got != srcBin {
		t.Errorf("missing configured path should fall through: got %q, want %q", got, srcBin)
	}
}
