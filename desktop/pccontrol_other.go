//go:build !windows

package main

import "errors"

var errNotWindows = errors.New("PC 제어는 Windows 에서만 돼요")

func shellOpen(string) error                { return errNotWindows }
func monitorOff()                           {}
func volumeGet() (int, bool, error)         { return 0, false, errNotWindows }
func volumeSet(level int, muted bool) error { return errNotWindows }
