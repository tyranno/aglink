//go:build !windows

package main

import "errors"

func playWavNative([]byte, int) error { return errors.New("native playback is Windows-only") }

func stopWavNative(int) {}
