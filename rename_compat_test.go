package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The teleclaude → aglink rename must not orphan memory a project already
// accumulated under the old directory name. These pin the fallback policy:
// prefer .aglink, but keep using a pre-existing .teleclaude in place.

// setHomeEnv points os.UserHomeDir at dir on both Windows and Unix.
func setHomeEnv(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("USERPROFILE", dir) // Windows
	t.Setenv("HOME", dir)        // Unix
}

func TestProjectMemoryDirName_NewProjectUsesAglink(t *testing.T) {
	dir := t.TempDir()
	if got := projectMemoryDirName(dir); got != ".aglink" {
		t.Errorf("got %q, want .aglink for a project with no memory dir", got)
	}
}

func TestProjectMemoryDirName_LegacyOnlyKeepsTeleclaude(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".teleclaude", "memory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := projectMemoryDirName(dir); got != ".teleclaude" {
		t.Errorf("got %q, want .teleclaude — a pre-rename project must keep reading its existing memory", got)
	}
}

func TestProjectMemoryDirName_AglinkWinsWhenBothExist(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{".teleclaude", ".aglink"} {
		if err := os.MkdirAll(filepath.Join(dir, n), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if got := projectMemoryDirName(dir); got != ".aglink" {
		t.Errorf("got %q, want .aglink once the project has migrated", got)
	}
}

func TestProjectMemoryDirName_EmptyPath(t *testing.T) {
	if got := projectMemoryDirName(""); got != ".aglink" {
		t.Errorf("got %q, want .aglink", got)
	}
}

// A regular file named .aglink must not be mistaken for a migrated project.
func TestProjectMemoryDirName_LegacyFileNotDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".teleclaude"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := projectMemoryDirName(dir); got != ".aglink" {
		t.Errorf("got %q, want .aglink — a stray file is not a legacy memory dir", got)
	}
}

func TestConversationMemoryPath(t *testing.T) {
	legacy := t.TempDir()
	if err := os.MkdirAll(filepath.Join(legacy, ".teleclaude"), 0o700); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name        string
		projectPath string
		convID      string
		want        string
	}{
		{"new project", t.TempDir(), "42", ".aglink/memory/42.md"},
		{"new project, no conv", t.TempDir(), "", ".aglink/memory.md"},
		{"legacy project", legacy, "42", ".teleclaude/memory/42.md"},
		{"legacy project, no conv", legacy, "", ".teleclaude/memory.md"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := conversationMemoryPath(tc.projectPath, tc.convID); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// readProjectMemory must follow the same fallback, or a pre-rename project's
// memory would silently stop being injected into the Worker prompt.
func TestReadProjectMemory_ReadsLegacyDir(t *testing.T) {
	dir := t.TempDir()
	memDir := filepath.Join(dir, ".teleclaude", "memory")
	if err := os.MkdirAll(memDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "42.md"), []byte("  legacy note  "), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readProjectMemory(dir, "42"); got != "legacy note" {
		t.Errorf("got %q, want %q", got, "legacy note")
	}
}

func TestReadProjectMemory_ReadsAglinkDir(t *testing.T) {
	dir := t.TempDir()
	memDir := filepath.Join(dir, ".aglink", "memory")
	if err := os.MkdirAll(memDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "42.md"), []byte("new note"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readProjectMemory(dir, "42"); got != "new note" {
		t.Errorf("got %q, want %q", got, "new note")
	}
}

// dataDir keeps a pre-rename ~/.teleclaude install working in place rather than
// creating an empty ~/.aglink beside it and losing config.yaml/store.json.
func TestDataDir_PrefersLegacyWhenOnlyLegacyExists(t *testing.T) {
	home := t.TempDir()
	setHomeEnv(t, home)

	legacy := filepath.Join(home, ".teleclaude")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := dataDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != legacy {
		t.Errorf("got %q, want %q", got, legacy)
	}
	if _, err := os.Stat(filepath.Join(home, ".aglink")); !os.IsNotExist(err) {
		t.Errorf(".aglink should not have been created alongside an existing legacy dir")
	}
}

func TestDataDir_CreatesAglinkForFreshInstall(t *testing.T) {
	home := t.TempDir()
	setHomeEnv(t, home)

	got, err := dataDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".aglink")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if st, err := os.Stat(want); err != nil || !st.IsDir() {
		t.Errorf(".aglink was not created: %v", err)
	}
}
