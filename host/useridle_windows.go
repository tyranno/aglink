//go:build windows

package main

import (
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procGetLastInputInfo = windows.NewLazySystemDLL("user32.dll").NewProc("GetLastInputInfo")
	procGetTickCount     = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetTickCount")
)

type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

// userIdleDuration is how long this session has had no keyboard/mouse input
// (GetLastInputInfo). The host runs as a logon task in the user's session
// (taskinstall.go), so this is the user's own idle time. Note that input
// injected by screen control (SendInput) also counts as input.
func userIdleDuration() (time.Duration, bool) {
	if procGetLastInputInfo.Find() != nil || procGetTickCount.Find() != nil {
		return 0, false
	}
	li := lastInputInfo{cbSize: uint32(unsafe.Sizeof(lastInputInfo{}))}
	r, _, _ := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&li)))
	if r == 0 {
		return 0, false
	}
	now, _, _ := procGetTickCount.Call()
	// Both are 32-bit millisecond tick counts; the unsigned difference stays
	// right across the 49.7-day wrap.
	return time.Duration(uint32(now)-li.dwTime) * time.Millisecond, true
}
