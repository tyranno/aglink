package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneAttachments_KeepsNewest(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	// 120 files, mtime increasing with index (index 119 is newest).
	for i := 0; i < 120; i++ {
		p := filepath.Join(dir, fmt.Sprintf("f%03d.png", i))
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		mod := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
	}

	pruneAttachments(dir, 100)

	remaining, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 100 {
		t.Fatalf("kept %d files, want 100", len(remaining))
	}
	// The 20 oldest (f000..f019) must be gone; the newest (f119) must remain.
	if _, err := os.Stat(filepath.Join(dir, "f000.png")); !os.IsNotExist(err) {
		t.Errorf("oldest file f000.png should have been pruned")
	}
	if _, err := os.Stat(filepath.Join(dir, "f019.png")); !os.IsNotExist(err) {
		t.Errorf("f019.png (20th oldest) should have been pruned")
	}
	if _, err := os.Stat(filepath.Join(dir, "f020.png")); err != nil {
		t.Errorf("f020.png (101st newest) should remain, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "f119.png")); err != nil {
		t.Errorf("newest file f119.png should remain, stat err=%v", err)
	}
}

func TestPruneAttachments_UnderLimitNoOp(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 10; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d.png", i)), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pruneAttachments(dir, 100)
	remaining, _ := os.ReadDir(dir)
	if len(remaining) != 10 {
		t.Errorf("under-limit dir should be untouched, got %d files, want 10", len(remaining))
	}
}

func TestPruneAttachments_IgnoresSubdirsAndBadArgs(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d.png", i)), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// keep <= 0 and empty dir are no-ops (must not panic).
	pruneAttachments(dir, 0)
	pruneAttachments("", 100)
	// keep=2 removes 3 files but leaves the subdir intact.
	pruneAttachments(dir, 2)
	if _, err := os.Stat(filepath.Join(dir, "sub")); err != nil {
		t.Errorf("subdirectory must not be removed, stat err=%v", err)
	}
	remaining, _ := os.ReadDir(dir)
	// 2 files + 1 subdir.
	if len(remaining) != 3 {
		t.Errorf("expected 2 files + 1 subdir = 3 entries, got %d", len(remaining))
	}
}
