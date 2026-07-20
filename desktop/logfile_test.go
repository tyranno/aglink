package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotateLogIfLarge_Boundaries(t *testing.T) {
	const limit = 100

	t.Run("missing file is a no-op", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), logFileName)
		rotateLogIfLarge(p, limit) // must not panic or create anything
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Error("rotation must not create the log")
		}
		if _, err := os.Stat(p + ".old"); !os.IsNotExist(err) {
			t.Error("rotation must not create an .old for a missing log")
		}
	})

	t.Run("just under the limit stays put", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), logFileName)
		writeN(t, p, limit-1)
		rotateLogIfLarge(p, limit)
		if sz := size(t, p); sz != limit-1 {
			t.Errorf("log size = %d, want it untouched at %d", sz, limit-1)
		}
		if _, err := os.Stat(p + ".old"); !os.IsNotExist(err) {
			t.Error("a log under the limit must not rotate")
		}
	})

	t.Run("exactly at the limit rotates", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), logFileName)
		writeN(t, p, limit)
		rotateLogIfLarge(p, limit)
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Error("the oversized log must be moved aside")
		}
		if sz := size(t, p+".old"); sz != limit {
			t.Errorf(".old size = %d, want %d", sz, limit)
		}
	})

	t.Run("only one .old generation is kept", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, logFileName)
		writeN(t, p+".old", 7) // a previous generation
		writeN(t, p, limit+50)

		rotateLogIfLarge(p, limit)

		if sz := size(t, p+".old"); sz != limit+50 {
			t.Errorf(".old size = %d, want the new one (%d): the previous generation must be replaced", sz, limit+50)
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 1 {
			t.Errorf("dir holds %d files, want only the single .old", len(entries))
		}
	})
}

// The point of the whole file: with no console, log.Printf must land on disk.
func TestSetupFileLogging_CapturesStandardLogger(t *testing.T) {
	prev := log.Writer()
	t.Cleanup(func() { log.SetOutput(prev) })

	dir := t.TempDir()
	closer, logger, err := setupFileLogging(dir)
	if err != nil {
		t.Fatalf("setupFileLogging: %v", err)
	}
	defer closer.Close()
	if logger == nil {
		t.Error("a slog logger for Wails system messages must be returned")
	}

	log.Printf("[control] connected to %s", "127.0.0.1:27270")

	b, err := os.ReadFile(filepath.Join(dir, logFileName))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(b), "connected to 127.0.0.1:27270") {
		t.Errorf("log file missing the standard logger's output:\n%s", b)
	}
}

// setupFileLogging must never stop startup: a directory it cannot write into
// yields an error the caller ignores, not a panic.
func TestSetupFileLogging_UnwritableDirIsNotFatal(t *testing.T) {
	prev := log.Writer()
	t.Cleanup(func() { log.SetOutput(prev) })

	// A path whose parent is a file, not a directory: the open must fail.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	writeN(t, blocker, 1)

	closer, _, err := setupFileLogging(blocker)
	if err == nil {
		if closer != nil {
			_ = closer.Close()
		}
		t.Fatal("opening a log under a regular file should have failed")
	}
}

func writeN(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, n), 0o600); err != nil {
		t.Fatal(err)
	}
}

func size(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Size()
}
