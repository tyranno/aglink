package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRotateLogIfLarge_Boundaries(t *testing.T) {
	const cap = 100

	t.Run("missing file is a no-op", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "aglink.log")
		rotateLogIfLarge(p, cap) // must not panic or create anything
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Error("rotation must not create the log")
		}
		if _, err := os.Stat(p + ".old"); !os.IsNotExist(err) {
			t.Error("rotation must not create an .old for a missing log")
		}
	})

	t.Run("just under the cap stays put", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "aglink.log")
		write(t, p, cap-1)
		rotateLogIfLarge(p, cap)
		if sz := size(t, p); sz != cap-1 {
			t.Errorf("log size = %d, want it untouched at %d", sz, cap-1)
		}
		if _, err := os.Stat(p + ".old"); !os.IsNotExist(err) {
			t.Error("a log under the cap must not rotate")
		}
	})

	t.Run("exactly at the cap rotates", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "aglink.log")
		write(t, p, cap)
		rotateLogIfLarge(p, cap)
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Error("the oversized log must be moved aside")
		}
		if sz := size(t, p+".old"); sz != cap {
			t.Errorf(".old size = %d, want %d", sz, cap)
		}
	})

	t.Run("only one .old generation is kept", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "aglink.log")
		write(t, p+".old", 7) // a previous generation
		write(t, p, cap+50)

		rotateLogIfLarge(p, cap)

		if sz := size(t, p+".old"); sz != cap+50 {
			t.Errorf(".old size = %d, want the new one (%d): the previous generation must be replaced", sz, cap+50)
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 1 {
			t.Errorf("dir holds %d files, want only the single .old", len(entries))
		}
	})
}

func write(t *testing.T, path string, n int) {
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

// setupFileLogging must never stop startup. A directory it cannot write into
// yields an error the caller logs, not a panic — and childLogWriter keeps a
// usable default so a supervised child's output still goes somewhere.
func TestSetupFileLogging_UnwritableDirIsNotFatal(t *testing.T) {
	prev := childLogWriter
	t.Cleanup(func() { childLogWriter = prev })

	// A path whose parent is a file, not a directory: the open must fail.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	write(t, blocker, 1)

	closer, err := setupFileLogging(blocker)
	if err == nil {
		if closer != nil {
			_ = closer.Close()
		}
		t.Fatal("opening a log under a regular file should have failed")
	}
	if childLogWriter == nil {
		t.Error("childLogWriter must stay usable when file logging is unavailable")
	}
}
