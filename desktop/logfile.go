package main

import (
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
)

// logFileName is the desktop client's diagnostic log under the data dir.
// It is separate from the host's aglink.log so the two processes — which may
// share a data dir — don't interleave into one file.
const logFileName = "aglink-desktop.log"

// maxLogBytes caps the log at startup. Past this the current file is rotated to
// aglink-desktop.log.old (one generation) so the log can't grow without bound.
const maxLogBytes = 10 << 20 // 10 MiB

// setupFileLogging points the standard logger and the Wails system logger at
// <dir>/aglink-desktop.log, and returns the file to close on shutdown.
//
// The desktop binary is linked with -H windowsgui so launching it does not pop a
// console window. That also means it has no usable stderr: every log.Printf —
// including control.go's connect/disconnect lines, the only trace of a failing
// control API handshake — would go nowhere. Unlike the host's tee, this replaces
// stderr rather than adding to it, because io.MultiWriter aborts on the first
// writer that errors and a GUI-subsystem stderr is an invalid handle.
//
// Failure to open the log is not fatal: the app still starts, just silently.
func setupFileLogging(dir string) (io.Closer, *slog.Logger, error) {
	path := filepath.Join(dir, logFileName)
	rotateLogIfLarge(path, maxLogBytes)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open log file %s: %w", path, err)
	}
	log.SetOutput(f)
	log.Printf("[log] diagnostic log → %s (pid %d)", path, os.Getpid())
	return f, slog.New(slog.NewTextHandler(f, nil)), nil
}

// rotateLogIfLarge moves an oversized log aside, keeping a single .old
// generation. Errors are ignored — a rotation failure must not stop startup;
// the log simply keeps growing rather than the process refusing to run.
//
// limit is a parameter only so the boundary is testable without writing 10 MiB.
func rotateLogIfLarge(path string, limit int64) {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() < limit {
		return
	}
	_ = os.Remove(path + ".old")
	_ = os.Rename(path, path+".old")
}
