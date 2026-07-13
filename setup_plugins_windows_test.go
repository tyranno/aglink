//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestAglinkRepoURL(t *testing.T) {
	got := aglinkRepoURL("aglink-screen")
	want := "https://github.com/tyranno/aglink-screen.git"
	if got != want {
		t.Errorf("aglinkRepoURL = %q, want %q", got, want)
	}
}

func TestBuildPlugin_Success(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	pluginDir := t.TempDir()
	writeMinimalGoModule(t, pluginDir, "okplugin")
	target := filepath.Join(t.TempDir(), "okplugin"+exeSuffix)

	if err := buildPlugin(pluginDir, target); err != nil {
		t.Fatalf("buildPlugin: %v", err)
	}
	if _, statErr := os.Stat(target); statErr != nil {
		t.Errorf("expected binary at %s: %v", target, statErr)
	}
}

func TestBuildPlugin_FailureReturnsOutput(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	pluginDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(pluginDir, "main.go"), []byte("package main\nfunc main() { this is not valid go }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "go.mod"), []byte("module brokenplugin\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := buildPlugin(pluginDir, filepath.Join(t.TempDir(), "brokenplugin"+exeSuffix))
	if err == nil {
		t.Fatal("expected build error for invalid source, got nil")
	}
}

func TestEnsureAglinkPlugins_SkipsWhenAllPresent(t *testing.T) {
	orig := pluginNames
	defer func() { pluginNames = orig }()
	pluginNames = []string{"already-here"}

	parent := t.TempDir()
	teleclaudeDir := filepath.Join(parent, "teleclaude")
	mustMkdir(t, teleclaudeDir)
	mustMkdir(t, filepath.Join(parent, "already-here"))

	// A nil *bufio.Reader would panic if ensureAglinkPlugins tried to prompt;
	// reaching the end without a panic confirms it took the early-return path.
	ensureAglinkPlugins(nil, teleclaudeDir)
}
