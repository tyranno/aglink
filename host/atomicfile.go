package main

import (
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// renameFile is os.Rename, swappable so tests can simulate a failed
// replacement (the step that would otherwise leave a half-written target).
var renameFile = os.Rename

// writeFileAtomic writes data to a temp file beside path, fsyncs it, and
// renames it over path, so neither a reader nor a crash/interruption mid-write
// ever leaves a partial file: the target is either the old content or the new.
// The file is created 0600 (os.CreateTemp's mode) — every caller writes
// private data (config.yaml with secrets, memory files).
//
// On Windows os.Rename is MoveFileEx(MOVEFILE_REPLACE_EXISTING), so replacing
// an existing file works; it can still fail transiently while another process
// (antivirus, indexer, an editor) holds the target open without delete
// sharing, so the rename is retried briefly there. On any failure the temp
// file is removed and the original is left untouched.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	err = renameFile(tmp, path)
	if err != nil && runtime.GOOS == "windows" {
		for i := 0; i < 5 && err != nil; i++ {
			time.Sleep(40 * time.Millisecond)
			err = renameFile(tmp, path)
		}
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	// Persist the directory entry too (best effort; not supported on Windows,
	// where the rename is already durable once MoveFileEx returns).
	if runtime.GOOS != "windows" {
		if d, derr := os.Open(dir); derr == nil {
			_ = d.Sync()
			d.Close()
		}
	}
	return nil
}
