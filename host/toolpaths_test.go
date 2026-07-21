package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveToolPath_ExplicitExistingWins(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "sshpass.exe")
	if err := os.WriteFile(exe, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{ToolPaths: map[string]string{"sshpass": exe}}
	if got := resolveToolPath(cfg, "sshpass"); got != exe {
		t.Errorf("explicit existing path should win, got %q", got)
	}
}

func TestResolveToolPath_MissingOverrideFallsThrough(t *testing.T) {
	// A configured-but-nonexistent path must not be returned; with the tool also
	// absent from PATH the result is "" (not the stale override).
	cfg := &Config{ToolPaths: map[string]string{"definitely-not-a-tool-xyz": `C:\nope\missing.exe`}}
	if got := resolveToolPath(cfg, "definitely-not-a-tool-xyz"); got != "" {
		t.Errorf("stale override should fall through to PATH (empty here), got %q", got)
	}
}

func TestSetToolPath_ClearDeletes(t *testing.T) {
	cfg := &Config{}
	setToolPath(cfg, "ssh", `C:\a\ssh.exe`)
	if cfg.ToolPaths["ssh"] != `C:\a\ssh.exe` {
		t.Fatalf("set failed: %+v", cfg.ToolPaths)
	}
	setToolPath(cfg, "ssh", "  ")
	if _, ok := cfg.ToolPaths["ssh"]; ok {
		t.Errorf("blank value should clear the entry: %+v", cfg.ToolPaths)
	}
}
