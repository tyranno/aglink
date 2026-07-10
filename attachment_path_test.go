package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeAged creates n files in dir with distinct mtimes (oldest first).
func writeAged(t *testing.T, dir string, n int) []string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var paths []string
	base := time.Now().Add(-time.Duration(n) * time.Hour)
	for i := 0; i < n; i++ {
		p := filepath.Join(dir, fmt.Sprintf("f%02d.txt", i))
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		mt := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	return paths
}

// upload_attachment carries a client-supplied Path. ingestAttachment used to
// prune filepath.Dir(that path), so a path outside the attachments directory
// made teleclaude delete everything but the newest maxAttachments files in
// whatever directory it named.
func TestIngestAttachment_RefusesPathOutsideAttachmentsDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	// A directory the attacker (or a buggy client) names. Well over the cap.
	victim := filepath.Join(t.TempDir(), "victim")
	files := writeAged(t, victim, maxAttachments+5)

	dispatched := false
	b := &Bot{
		cfgh:         NewConfigHolder(&Config{MaxWorkers: 1, TimeoutMinutes: 1}),
		cancels:      map[int]context.CancelFunc{},
		dispatchHook: func(int64, string) { dispatched = true },
	}
	b.out = NewHub()

	b.ingestAttachment(1, filepath.Join(victim, "f00.txt"), "caption", OriginWeb)

	survived := 0
	for _, p := range files {
		if _, err := os.Stat(p); err == nil {
			survived++
		}
	}
	if survived != len(files) {
		t.Errorf("ingestAttachment pruned a directory it was handed: %d of %d files survived",
			survived, len(files))
	}
	if dispatched {
		t.Error("a path outside the attachments directory must not be dispatched to a worker")
	}
}

// The legitimate path still works: a file inside the attachments directory is
// dispatched, and the directory is capped.
func TestIngestAttachment_AcceptsPathInsideAttachmentsDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	dir := filepath.Join(home, ".teleclaude", "attachments")
	files := writeAged(t, dir, maxAttachments+5)
	newest := files[len(files)-1]

	var gotPrompt string
	b := &Bot{
		cfgh:         NewConfigHolder(&Config{MaxWorkers: 1, TimeoutMinutes: 1}),
		cancels:      map[int]context.CancelFunc{},
		dispatchHook: func(_ int64, text string) { gotPrompt = text },
	}
	b.out = NewHub()

	b.ingestAttachment(1, newest, "설명", OriginWeb)

	if gotPrompt == "" {
		t.Fatal("a file inside the attachments directory must be dispatched")
	}
	if want := "[첨부파일: " + newest + "]"; !contains(gotPrompt, want) {
		t.Errorf("prompt = %q, want it to reference %q", gotPrompt, want)
	}
	if _, err := os.Stat(newest); err != nil {
		t.Errorf("the just-saved file must survive pruning: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != maxAttachments {
		t.Errorf("attachments dir holds %d files, want %d", len(entries), maxAttachments)
	}
}

// insideDir is the guard. It must reject an escape via "..", a sibling whose
// name merely starts with the directory's, and the directory itself.
func TestInsideDir(t *testing.T) {
	base := filepath.Join(t.TempDir(), "attachments")

	inside := []string{
		filepath.Join(base, "a.png"),
		filepath.Join(base, "sub", "b.png"),
		filepath.Join(base, "x", "..", "c.png"), // cleans back inside
	}
	for _, p := range inside {
		if !insideDir(base, p) {
			t.Errorf("insideDir(%q) = false, want true", p)
		}
	}

	outside := []string{
		filepath.Join(base, "..", "secrets.txt"),
		filepath.Join(base, "..", "..", "etc", "passwd"),
		base,                    // the directory itself is not a file in it
		base + "-sibling/a.png", // prefix match must not count
		filepath.Join(t.TempDir(), "elsewhere.txt"),
	}
	for _, p := range outside {
		if insideDir(base, p) {
			t.Errorf("insideDir(%q) = true, want false", p)
		}
	}
}
