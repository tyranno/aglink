//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// list_processes / kill_process via the Toolhelp snapshot and psapi — no
// PowerShell/tasklist spawn.

var (
	procK32GetProcessMemoryInfo = modKernel32App.NewProc("K32GetProcessMemoryInfo")
	procIsProcessCritical       = modKernel32App.NewProc("IsProcessCritical")
)

// processMemoryCounters is PROCESS_MEMORY_COUNTERS.
type processMemoryCounters struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

// killWaitMs is how long kill_process waits for a terminated process to exit.
const killWaitMs = 3000

// listProcesses returns every running process with its working set and first
// visible window title. Memory is 0 for processes this one may not query
// (other users' / protected ones when not elevated).
func listProcesses() ([]procInfo, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("process snapshot: %w", err)
	}
	defer windows.CloseHandle(snap)

	titles := map[uint32][]string{}
	for _, w := range enumWindows() {
		var pid uint32
		procGetWindowThreadPID.Call(w.HWND, uintptr(unsafe.Pointer(&pid)))
		titles[pid] = append(titles[pid], w.Title)
	}

	var out []procInfo
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		p := procInfo{
			PID:   pe.ProcessID,
			PPID:  pe.ParentProcessID,
			Name:    windows.UTF16ToString(pe.ExeFile[:]),
			Windows: titles[pe.ProcessID],
		}
		if len(p.Windows) > 0 {
			p.Title = p.Windows[0]
		}
		p.MemMB = processMemMB(p.PID)
		out = append(out, p)
	}
	if err != windows.ERROR_NO_MORE_FILES {
		return nil, fmt.Errorf("process walk: %w", err)
	}
	return out, nil
}

// processMemMB is pid's working set in MB, 0 when it cannot be read.
func processMemMB(pid uint32) float64 {
	if pid == 0 {
		return 0
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_VM_READ, false, pid)
	if err != nil {
		if h, err = windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid); err != nil {
			return 0
		}
	}
	defer windows.CloseHandle(h)
	var pmc processMemoryCounters
	pmc.CB = uint32(unsafe.Sizeof(pmc))
	if r, _, _ := procK32GetProcessMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&pmc)), uintptr(pmc.CB)); r == 0 {
		return 0
	}
	return float64(pmc.WorkingSetSize) / (1024 * 1024)
}

// killProcesses terminates the process with pid (when pid > 0) or every process
// whose executable is exactly name, applying killRefusal to each. It returns
// one line per process killed or refused; err is set only when nothing matched
// or nothing could be killed.
func killProcesses(pid int, name string, force bool) (killed, refused []string, err error) {
	ps, err := listProcesses()
	if err != nil {
		return nil, nil, err
	}
	var targets []procInfo
	for _, p := range ps {
		if (pid > 0 && p.PID == uint32(pid)) || (pid <= 0 && procNameMatches(p.Name, name)) {
			targets = append(targets, p)
		}
	}
	if len(targets) == 0 {
		if pid > 0 {
			return nil, nil, fmt.Errorf("no process with pid %d", pid)
		}
		return nil, nil, fmt.Errorf("no process named exactly %q (names match whole, e.g. \"notepad\" or \"notepad.exe\"; see list_processes)", name)
	}
	self, parent := uint32(os.Getpid()), uint32(os.Getppid())
	for _, p := range targets {
		label := fmt.Sprintf("%s (pid %d)", p.Name, p.PID)
		if why := killRefusal(p, self, parent, force); why != "" {
			refused = append(refused, fmt.Sprintf("%s: refused — %s", label, why))
			continue
		}
		if kerr := terminateProcess(p); kerr != nil {
			refused = append(refused, fmt.Sprintf("%s: %v", label, kerr))
			continue
		}
		killed = append(killed, label)
	}
	if len(killed) == 0 {
		return nil, refused, fmt.Errorf("nothing killed:\n%s", strings.Join(refused, "\n"))
	}
	return killed, refused, nil
}

// terminateProcess kills p and waits for it to exit. It re-checks the image
// name on the opened handle (the pid may have been reused since the snapshot)
// and refuses a process Windows flags as critical.
func terminateProcess(p procInfo) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, p.PID)
	if err != nil {
		return fmt.Errorf("open failed: %w (an elevated or protected process needs aglink-screen elevated)", err)
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) == nil {
		if got := filepath.Base(windows.UTF16ToString(buf[:n])); !strings.EqualFold(got, p.Name) {
			return fmt.Errorf("pid now belongs to %s, not killed", got)
		}
	}
	var critical int32
	if procIsProcessCritical.Find() == nil {
		if r, _, _ := procIsProcessCritical.Call(uintptr(h), uintptr(unsafe.Pointer(&critical))); r != 0 && critical != 0 {
			return fmt.Errorf("refused — Windows marks it critical")
		}
	}
	if err := windows.TerminateProcess(h, 1); err != nil {
		return fmt.Errorf("terminate failed: %w", err)
	}
	if ev, _ := windows.WaitForSingleObject(h, killWaitMs); ev != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("terminated but still running after %dms", killWaitMs)
	}
	return nil
}
