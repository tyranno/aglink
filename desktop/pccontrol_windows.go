package main

import (
	"errors"
	"math"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32          = windows.NewLazySystemDLL("user32.dll")
	procPostMessage = user32.NewProc("PostMessageW")
	ole32           = windows.NewLazySystemDLL("ole32.dll")
	procCoCreate    = ole32.NewProc("CoCreateInstance")
)

func shellOpen(name string) error {
	file, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	open, _ := windows.UTF16PtrFromString("open")
	return windows.ShellExecute(0, open, file, nil, nil, windows.SW_SHOWNORMAL)
}

// monitorOff asks every monitor to power down (SC_MONITORPOWER, 2). Posted,
// not sent: a window that does not answer must not hold this up.
func monitorOff() {
	const (
		hwndBroadcast  = 0xFFFF
		wmSysCommand   = 0x0112
		scMonitorPower = 0xF170
	)
	procPostMessage.Call(hwndBroadcast, wmSysCommand, scMonitorPower, 2)
}

// The speaker volume through Core Audio (IAudioEndpointVolume on the default
// output), called through the COM vtables directly — no dependency for four
// methods.
var (
	clsidMMDeviceEnumerator = windows.GUID{Data1: 0xBCDE0395, Data2: 0xE52F, Data3: 0x467C, Data4: [8]byte{0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E}}
	iidIMMDeviceEnumerator  = windows.GUID{Data1: 0xA95664D2, Data2: 0x9614, Data3: 0x4F35, Data4: [8]byte{0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6}}
	iidIAudioEndpointVolume = windows.GUID{Data1: 0x5CDF2C82, Data2: 0x841E, Data3: 0x4546, Data4: [8]byte{0x97, 0x22, 0x0C, 0xF7, 0x40, 0x78, 0x22, 0x9A}}
)

// Vtable slots used (IUnknown takes 0–2).
const (
	vtRelease                    = 2
	vtGetDefaultAudioEndpoint    = 4 // IMMDeviceEnumerator
	vtActivate                   = 3 // IMMDevice
	vtSetMasterVolumeLevelScalar = 7 // IAudioEndpointVolume
	vtGetMasterVolumeLevelScalar = 9
	vtSetMute                    = 14
	vtGetMute                    = 15
	clsctxAll                    = 23
)

// comCall calls method slot of the COM object obj with args after "this".
func comCall(obj unsafe.Pointer, slot int, args ...uintptr) uintptr {
	fn := (*(**[16]uintptr)(obj))[slot]
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(obj)}, args...)...)
	return r
}

func hresult(r uintptr, what string) error {
	if int32(r) < 0 {
		return errors.New(what + ": " + windows.Errno(r).Error())
	}
	return nil
}

// withEndpointVolume runs fn with the default speaker's IAudioEndpointVolume,
// on one OS thread with COM set up.
func withEndpointVolume(fn func(vol unsafe.Pointer) error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// S_FALSE (1): COM was already set up on this thread — still to be paired.
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); err == nil || err == syscall.Errno(1) {
		defer windows.CoUninitialize()
	}
	var enum unsafe.Pointer
	r, _, _ := procCoCreate.Call(uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)), 0, clsctxAll,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)), uintptr(unsafe.Pointer(&enum)))
	if err := hresult(r, "MMDeviceEnumerator"); err != nil {
		return err
	}
	defer comCall(enum, vtRelease)
	var dev unsafe.Pointer
	if err := hresult(comCall(enum, vtGetDefaultAudioEndpoint, 0 /* eRender */, 0 /* eConsole */, uintptr(unsafe.Pointer(&dev))), "default speaker"); err != nil {
		return err
	}
	defer comCall(dev, vtRelease)
	var vol unsafe.Pointer
	if err := hresult(comCall(dev, vtActivate, uintptr(unsafe.Pointer(&iidIAudioEndpointVolume)), clsctxAll, 0, uintptr(unsafe.Pointer(&vol))), "endpoint volume"); err != nil {
		return err
	}
	defer comCall(vol, vtRelease)
	return fn(vol)
}

// volumeGet reports the speaker volume (0–100) and whether it is muted.
func volumeGet() (level int, muted bool, err error) {
	err = withEndpointVolume(func(vol unsafe.Pointer) error {
		var f float32
		var m int32
		if err := hresult(comCall(vol, vtGetMasterVolumeLevelScalar, uintptr(unsafe.Pointer(&f))), "get volume"); err != nil {
			return err
		}
		if err := hresult(comCall(vol, vtGetMute, uintptr(unsafe.Pointer(&m))), "get mute"); err != nil {
			return err
		}
		level, muted = int(math.Round(float64(f)*100)), m != 0
		return nil
	})
	return
}

// volumeSet sets the speaker volume (0–100) and mute. The level is a float
// argument: on amd64 Go's syscall copies the first four arguments into XMM0–3
// as well, which is where the callee reads it.
func volumeSet(level int, muted bool) error {
	return withEndpointVolume(func(vol unsafe.Pointer) error {
		f := math.Float32bits(float32(level) / 100)
		if err := hresult(comCall(vol, vtSetMasterVolumeLevelScalar, uintptr(f), 0), "set volume"); err != nil {
			return err
		}
		m := uintptr(0)
		if muted {
			m = 1
		}
		return hresult(comCall(vol, vtSetMute, m, 0), "set mute")
	})
}
