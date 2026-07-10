package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
)

// logFileName is the diagnostic log under the data dir (~/.teleclaude).
const logFileName = "teleclaude.log"

// maxLogBytes caps the log at startup. Past this the current file is rotated to
// teleclaude.log.old (one generation) so the log can't grow without bound.
const maxLogBytes = 10 << 20 // 10 MiB

// childLogWriter is where supervised aglink-* children send stdout/stderr. It
// starts as plain stderr and is widened to also tee into the log file once
// setupFileLogging runs, so a crash-looping child's own error output is
// captured — not just teleclaude's "restarting" line.
var childLogWriter io.Writer = os.Stderr

// setupFileLogging tees the standard logger to <dir>/teleclaude.log in addition
// to stderr, and returns a close func.
//
// This exists because the elevated instance is launched with SW_HIDE (see
// relaunchElevated), so its console — and every log line teleclaude and its
// aglink-* children write — is invisible. A child crash-looping on a permanent
// error (e.g. aglink-chat unable to bind its port) left no trace anywhere.
// Writes are O_APPEND so the brief overlap between the un-elevated process and
// the elevated one it spawns interleaves cleanly instead of truncating.
//
// Failure to open the log is not fatal: logging falls back to stderr alone.
func setupFileLogging(dir string) (io.Closer, error) {
	path := filepath.Join(dir, logFileName)
	rotateLogIfLarge(path)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log file %s: %w", path, err)
	}
	tee := io.MultiWriter(os.Stderr, f)
	log.SetOutput(tee)
	childLogWriter = tee
	log.Printf("[log] diagnostic log → %s (pid %d, elevated=%v)", path, os.Getpid(), isElevated())
	return f, nil
}

// rotateLogIfLarge moves an oversized log aside, keeping a single .old
// generation. Errors are ignored — a rotation failure must not stop startup.
func rotateLogIfLarge(path string) {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() < maxLogBytes {
		return
	}
	_ = os.Remove(path + ".old")
	_ = os.Rename(path, path+".old")
}
