package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeConf creates <dir>/.aglink-web/config with the given body.
func writeConf(t *testing.T, dir, body string) {
	t.Helper()
	cd := filepath.Join(dir, projectConfDir)
	if err := os.MkdirAll(cd, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cd, projectConfFile), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestFindProjectAccountReadsConfig(t *testing.T) {
	dir := t.TempDir()
	writeConf(t, dir, "account = doowon.lab.02@gmail.com\n")
	if got := findProjectAccount(dir); got != "doowon.lab.02@gmail.com" {
		t.Fatalf("got %q, want doowon.lab.02@gmail.com", got)
	}
}

// The bridge's working directory is often a subdirectory of the project, so the
// search must climb the way git finds .git.
func TestFindProjectAccountWalksUp(t *testing.T) {
	root := t.TempDir()
	writeConf(t, root, "account = a@b.com\n")
	deep := filepath.Join(root, "src", "pkg")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if got := findProjectAccount(deep); got != "a@b.com" {
		t.Fatalf("got %q, want a@b.com", got)
	}
}

// No config anywhere up the tree means "no preference" — the caller falls back
// to the daemon's default profile. Never an error.
func TestFindProjectAccountMissing(t *testing.T) {
	if got := findProjectAccount(t.TempDir()); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// Comments, blank lines, and keys we do not recognise are skipped rather than
// treated as errors: an unknown key is a typo or a setting from a newer build.
func TestFindProjectAccountIgnoresCommentsAndUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	writeConf(t, dir, "# aglink-web settings\n\nfuture_key = 1\naccount = x@y.com\n")
	if got := findProjectAccount(dir); got != "x@y.com" {
		t.Fatalf("got %q, want x@y.com", got)
	}
}

// A config that exists but pins nothing is the same as no config at all.
func TestFindProjectAccountEmptyValue(t *testing.T) {
	dir := t.TempDir()
	writeConf(t, dir, "account =\n")
	if got := findProjectAccount(dir); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}
