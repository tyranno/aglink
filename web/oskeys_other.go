//go:build !windows

package main

import "errors"

const osKeysSupported = false

func typeIntoChrome(wantTitle, text string) error {
	return errors.New("typing into the browser window is only implemented on Windows")
}
