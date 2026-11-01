//go:build !windows

package main

func pasteAndSend(pid int, title string) error { return errNoVSCodeWindow }
