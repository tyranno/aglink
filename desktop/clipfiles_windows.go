package main

import (
	"errors"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32Clip           = windows.NewLazySystemDLL("user32.dll")
	shell32Clip          = windows.NewLazySystemDLL("shell32.dll")
	procOpenClipboard    = user32Clip.NewProc("OpenClipboard")
	procCloseClipboard   = user32Clip.NewProc("CloseClipboard")
	procGetClipboardData = user32Clip.NewProc("GetClipboardData")
	procIsClipFormatAvl  = user32Clip.NewProc("IsClipboardFormatAvailable")
	procDragQueryFileW   = shell32Clip.NewProc("DragQueryFileW")
)

const cfHDROP = 15

// clipboardFilePaths returns the files copied in Explorer (CF_HDROP), or nil
// when the clipboard holds no file list. WebView2 only exposes those as
// nameless blobs (non-images not at all), so the paths come from Win32.
func clipboardFilePaths() ([]string, error) {
	if r, _, _ := procIsClipFormatAvl.Call(cfHDROP); r == 0 {
		return nil, nil
	}
	if r, _, err := procOpenClipboard.Call(0); r == 0 {
		return nil, errors.New("open clipboard: " + err.Error())
	}
	defer procCloseClipboard.Call()
	h, _, _ := procGetClipboardData.Call(cfHDROP)
	if h == 0 {
		return nil, nil
	}
	n, _, _ := procDragQueryFileW.Call(h, 0xFFFFFFFF, 0, 0)
	paths := make([]string, 0, n)
	for i := uintptr(0); i < n; i++ {
		size, _, _ := procDragQueryFileW.Call(h, i, 0, 0)
		buf := make([]uint16, size+1)
		procDragQueryFileW.Call(h, i, uintptr(unsafe.Pointer(&buf[0])), size+1)
		paths = append(paths, syscall.UTF16ToString(buf))
	}
	return paths, nil
}
