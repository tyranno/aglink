//go:build !windows

package main

import "fmt"

// RunScreenRemote is the non-Windows stub. Serving the screen tools over HTTP
// is only meaningful where those tools exist, so this fails fast with the same
// OS-specific message as the stdio entry point rather than starting a listener
// that would answer every call with an error.
func RunScreenRemote(addr string) error {
	return fmt.Errorf("screen control is Windows-only")
}
