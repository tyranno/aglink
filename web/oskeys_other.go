//go:build !windows

package main

import "errors"

const osKeysSupported = false

const (
	vkReturn = 0x0D
	vkEscape = 0x1B
)

func typeIntoChrome(wantTitle, text string) error {
	return chromeKeys(wantTitle, text, 0)
}

func chromeKeys(wantTitle, text string, vk uint16) error {
	return errors.New("typing into the browser window is only implemented on Windows")
}
