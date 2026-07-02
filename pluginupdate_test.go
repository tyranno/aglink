package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestUpdatePlugins_SkipsMissingSibling(t *testing.T) {
	orig := pluginNames
	defer func() { pluginNames = orig }()
	pluginNames = []string{"nonexistent-plugin"}

	teleclaudeDir := t.TempDir()
	report, err := updatePlugins(teleclaudeDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(report) != 0 {
		t.Errorf("expected no plugins built, got %v", report)
	}
}

func TestUpdatePlugins_BuildsSiblingAndReportsIt(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	orig := pluginNames
	defer func() { pluginNames = orig }()
	pluginNames = []string{"okplugin"}

	parent := t.TempDir()
	teleclaudeDir := filepath.Join(parent, "teleclaude")
	mustMkdir(t, teleclaudeDir)
	pluginDir := filepath.Join(parent, "okplugin")
	mustMkdir(t, pluginDir)
	writeMinimalGoModule(t, pluginDir, "okplugin")

	report, err := updatePlugins(teleclaudeDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(report) != 1 || report[0] != "okplugin" {
		t.Errorf("report = %v, want [okplugin]", report)
	}
	binPath := filepath.Join(teleclaudeDir, "okplugin"+exeSuffix)
	if _, statErr := os.Stat(binPath); statErr != nil {
		t.Errorf("expected binary at %s: %v", binPath, statErr)
	}
}

func TestUpdatePlugins_BuildFailureAborts(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	orig := pluginNames
	defer func() { pluginNames = orig }()
	pluginNames = []string{"brokenplugin"}

	parent := t.TempDir()
	teleclaudeDir := filepath.Join(parent, "teleclaude")
	mustMkdir(t, teleclaudeDir)
	pluginDir := filepath.Join(parent, "brokenplugin")
	mustMkdir(t, pluginDir)
	if err := os.WriteFile(filepath.Join(pluginDir, "go.mod"), []byte("module brokenplugin\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "main.go"), []byte("package main\n\nfunc main() { this is not valid go }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := updatePlugins(teleclaudeDir); err == nil {
		t.Fatal("expected error for broken plugin build")
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeMinimalGoModule(t *testing.T, dir, module string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module "+module+"\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
