//go:build !windows

package main

import "fmt"

// RunMCPScreen on non-Windows: screen control relies on Win32 APIs, so there is
// no local screen here. But in a Remote-SSH session (SSH_CONNECTION set) this is
// the SERVER a worker runs on, and the real screen is on the client PC — so
// transparently proxy the screen MCP tools to that PC's screen server. Outside
// an SSH session there is no screen to reach, so fail fast as before.
func RunMCPScreen() error {
	if host := screenClientHost(); host != "" {
		return runScreenProxy(host, screenRemoteToken())
	}
	return fmt.Errorf("screen control is Windows-only")
}
