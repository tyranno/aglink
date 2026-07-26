package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestUpdatePlugins_SkipsMissingSubdir(t *testing.T) {
	orig := pluginBuilds
	defer func() { pluginBuilds = orig }()
	pluginBuilds = []struct{ subdir, exe string }{{"nonexistent", "nonexistent-plugin"}}

	aglinkDir := t.TempDir()
	report, err := updatePlugins(aglinkDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(report) != 0 {
		t.Errorf("expected no plugins built, got %v", report)
	}
}

func TestUpdatePlugins_BuildsSubdirAndReportsIt(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	orig := pluginBuilds
	defer func() { pluginBuilds = orig }()
	pluginBuilds = []struct{ subdir, exe string }{{"okplugin", "okbin"}}

	// The merged layout: the plugin source lives in a sub-dir of aglink's srcDir,
	// and its binary is written back into that same sub-dir (the configured path).
	aglinkDir := t.TempDir()
	pluginDir := filepath.Join(aglinkDir, "okplugin")
	mustMkdir(t, pluginDir)
	writeMinimalGoModule(t, pluginDir, "okbin")

	report, err := updatePlugins(aglinkDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(report) != 1 || report[0] != "okbin" {
		t.Errorf("report = %v, want [okbin]", report)
	}
	binPath := filepath.Join(pluginDir, "okbin"+exeSuffix)
	if _, statErr := os.Stat(binPath); statErr != nil {
		t.Errorf("expected binary at %s: %v", binPath, statErr)
	}
}

func TestUpdatePlugins_BuildFailureAborts(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	orig := pluginBuilds
	defer func() { pluginBuilds = orig }()
	pluginBuilds = []struct{ subdir, exe string }{{"brokenplugin", "brokenbin"}}

	aglinkDir := t.TempDir()
	pluginDir := filepath.Join(aglinkDir, "brokenplugin")
	mustMkdir(t, pluginDir)
	if err := os.WriteFile(filepath.Join(pluginDir, "go.mod"), []byte("module brokenbin\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "main.go"), []byte("package main\n\nfunc main() { this is not valid go }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := updatePlugins(aglinkDir); err == nil {
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
