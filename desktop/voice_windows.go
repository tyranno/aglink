package main

import (
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// hideConsole keeps a console child (whisper-cli.exe) from flashing a terminal
// window over this GUI-subsystem app.
func hideConsole(cmd *exec.Cmd) {
	const createNoWindow = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

// belowNormal starts cmd (after hideConsole) one priority step down. The
// speech synthesizer fills every core while the next sentence is made, and at
// normal priority it starved the reading of the one before: a few syllables
// into the first sentence the voice broke off, then went on.
func belowNormal(cmd *exec.Cmd) {
	if cmd.SysProcAttr != nil {
		cmd.SysProcAttr.CreationFlags |= windows.BELOW_NORMAL_PRIORITY_CLASS
	}
}

var (
	appJobOnce sync.Once
	appJob     windows.Handle
)

// killWithApp puts p in a job that Windows closes when this process exits,
// however it exits, so a helper server is never left running on its own.
func killWithApp(p *os.Process) {
	appJobOnce.Do(func() {
		job, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
			windows.CloseHandle(job)
			return
		}
		appJob = job
	})
	if appJob == 0 {
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(p.Pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	_ = windows.AssignProcessToJobObject(appJob, h)
}
