//go:build !windows

package main

// Non-Windows: screen control itself is Windows-only, so there is no idle lock
// to defeat here.

func startKeepAwake() (stop func()) { return func() {} }
