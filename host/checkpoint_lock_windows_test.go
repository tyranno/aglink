package main

import (
	"syscall"
	"testing"
)

// makeUnreadable opens p with no sharing, so no other process (git) can read
// it until the returned func closes it — what a database or VM disk does.
func makeUnreadable(t *testing.T, p string) func() {
	t.Helper()
	name, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		t.Fatal(err)
	}
	h, err := syscall.CreateFile(name, syscall.GENERIC_READ, 0, nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	return func() {
		if !closed {
			closed = true
			syscall.CloseHandle(h)
		}
	}
}
