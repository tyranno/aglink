//go:build !windows

package main

import (
	"os"
	"testing"
)

// makeUnreadable takes away read permission on p — what a root-owned 0600
// file is to the user aglink runs as. Root reads it anyway, so skip there.
func makeUnreadable(t *testing.T, p string) func() {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root can read any file")
	}
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatal(err)
	}
	return func() { _ = os.Chmod(p, 0o644) }
}
