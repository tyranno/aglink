//go:build !windows

package main

func anyChromeWindowRunning() bool { return false }

func launchChrome() error { return errChromeNotFound }
