//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Design Ref: docs/control-ownership.md — cross-process screen-control ownership.
//
// Every teleclaude conversation spawns its OWN aglink-screen process, and they all
// drive the same physical screen. To stop two of them synthesizing input at once we
// keep a single, system-wide "control lease": a small JSON file recording which
// process currently owns the screen, guarded by a session-local named mutex so the
// read-modify-write is atomic across processes.
//
// Policy is fail-fast (§2): if another live session holds a fresh lease, a control
// op is refused with a SCREEN_BUSY error rather than blocking — the caller (worker
// LLM / teleclaude) decides whether to back off and retry. The lease self-expires
// after an idle TTL and is reclaimed if the owner PID is gone, so no explicit
// release is needed (crash-safe).

const (
	leaseDefaultTTLMS = 8000  // matches controlNoticeGap: the "session ended" idle mark
	leaseMinTTLMS     = 1000
	leaseMaxTTLMS     = 120000

	// Session-local (not Global\): every screen-driving process shares the one
	// interactive desktop session, so a session-local name suffices and avoids the
	// SeCreateGlobalPrivilege that Global\ requires.
	leaseMutexName = `Local\aglink-screen-control`

	// leaseMutexWaitMS bounds the wait for the guard mutex. The guard is held only
	// for the microseconds of a file read+write, so a timeout here means something
	// pathological — we fail OPEN (allow control) rather than brick all input.
	leaseMutexWaitMS = 2000

	stillActive = 259 // STILL_ACTIVE from GetExitCodeProcess
)

// controlLeaseOff disables the whole mechanism (single-session deploy / debugging).
var controlLeaseOff = os.Getenv("AGLINK_NO_CONTROL_LEASE") != ""

// Test seams: production wires these to the real path/clock/liveness check; tests
// override them to exercise the acquisition logic deterministically without needing
// multiple real processes.
var (
	leasePathFn = defaultLeasePath
	leaseNowMS  = func() int64 { return time.Now().UnixMilli() }
	leaseAlive  = isProcessAlive
)

// leaseRecord mirrors ~/.teleclaude/screen-control.lock (docs §3.1).
type leaseRecord struct {
	OwnerPID     int    `json:"owner_pid"`
	OwnerLabel   string `json:"owner_label,omitempty"`
	Since        int64  `json:"since"`          // when this ownership began (UnixMillis)
	LastActivity int64  `json:"last_activity"`  // last control op (UnixMillis) — TTL basis
}

// leaseTTLMS returns the lease idle-timeout, honoring AGLINK_CONTROL_LEASE_TTL_MS
// (clamped) and defaulting to leaseDefaultTTLMS.
func leaseTTLMS() int64 {
	d := int64(leaseDefaultTTLMS)
	if v := os.Getenv("AGLINK_CONTROL_LEASE_TTL_MS"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			d = n
		}
	}
	if d < leaseMinTTLMS {
		d = leaseMinTTLMS
	}
	if d > leaseMaxTTLMS {
		d = leaseMaxTTLMS
	}
	return d
}

// ownerLabel is the human-readable owner tag teleclaude may pass per conversation.
func ownerLabel() string { return os.Getenv("AGLINK_OWNER_LABEL") }

// leaseCanTake is the pure decision (docs §3.3): may `myPID` take/renew the screen
// given the existing record (nil = free)? Returns true when the lease is free,
// already ours, stale (idle past ttl), or owned by a dead PID.
func leaseCanTake(rec *leaseRecord, myPID int, nowMS, ttlMS int64, alive func(int) bool) bool {
	switch {
	case rec == nil || rec.OwnerPID == 0:
		return true // free
	case rec.OwnerPID == myPID:
		return true // ours — continuous ownership, just renew
	case nowMS-rec.LastActivity > ttlMS:
		return true // stale — previous owner went idle
	case !alive(rec.OwnerPID):
		return true // owner process gone
	default:
		return false // another live, fresh owner → busy
	}
}

// acquireControlLease is called at the top of beginSyntheticInput (docs §3.4). It
// takes/renews the lease for this process, or returns a SCREEN_BUSY error if another
// live session currently owns the screen. Any infrastructure failure (no data dir,
// guard mutex unavailable) fails OPEN — control is never bricked by the lease layer.
func acquireControlLease() error {
	if controlLeaseOff {
		return nil
	}
	path, err := leasePathFn()
	if err != nil {
		return nil // can't locate the lease file — don't block control
	}

	h, err := lockLeaseMutex()
	if err != nil {
		return nil // guard unavailable — fail open
	}
	defer unlockLeaseMutex(h)

	now := leaseNowMS()
	rec, _ := readLeaseFrom(path) // nil on missing/corrupt (= free)
	myPID := os.Getpid()
	if !leaseCanTake(rec, myPID, now, leaseTTLMS(), leaseAlive) {
		return busyError(rec, now)
	}

	since := now
	if rec != nil && rec.OwnerPID == myPID && rec.Since != 0 {
		since = rec.Since // preserve the start of our own continuous ownership
	}
	_ = writeLeaseTo(path, &leaseRecord{
		OwnerPID:     myPID,
		OwnerLabel:   ownerLabel(),
		Since:        since,
		LastActivity: now,
	})
	return nil
}

// busyError builds the SCREEN_BUSY refusal (docs §4.1). The "SCREEN_BUSY:" marker is
// a stable contract the caller can match on; the tool handler may prefix its own
// context (e.g. "click failed: SCREEN_BUSY: ...").
func busyError(rec *leaseRecord, nowMS int64) error {
	label := ""
	if rec.OwnerLabel != "" {
		label = fmt.Sprintf(", label=%q", rec.OwnerLabel)
	}
	return fmt.Errorf("SCREEN_BUSY: another teleclaude session is controlling the screen "+
		"(owner_pid=%d%s, active %.1fs ago); refused to avoid colliding with its input — "+
		"wait a few seconds and retry, or ensure only one session drives the screen at a time",
		rec.OwnerPID, label, secsAgo(rec.LastActivity, nowMS))
}

// controlStatusText backs the read-only control_status MCP tool (docs §4.2). It
// reports the current owner without taking the lease.
func controlStatusText() string {
	if controlLeaseOff {
		return "control: lease disabled (AGLINK_NO_CONTROL_LEASE)"
	}
	path, err := leasePathFn()
	if err != nil {
		return "control: unknown (cannot locate lease file)"
	}
	if h, err := lockLeaseMutex(); err == nil {
		defer unlockLeaseMutex(h)
	}
	rec, _ := readLeaseFrom(path)
	now := leaseNowMS()
	if rec == nil {
		return "control: free"
	}
	if rec.OwnerPID == os.Getpid() {
		return fmt.Sprintf("control: held by me (pid=%d, since %.1fs ago)", rec.OwnerPID, secsAgo(rec.Since, now))
	}
	ttl := leaseTTLMS()
	if now-rec.LastActivity > ttl || !leaseAlive(rec.OwnerPID) {
		return "control: free (previous owner expired)"
	}
	label := ""
	if rec.OwnerLabel != "" {
		label = fmt.Sprintf(", label=%q", rec.OwnerLabel)
	}
	return fmt.Sprintf("control: held by another (owner_pid=%d%s, last_activity %.1fs ago, ttl %dms)",
		rec.OwnerPID, label, secsAgo(rec.LastActivity, now), ttl)
}

func secsAgo(thenMS, nowMS int64) float64 {
	d := nowMS - thenMS
	if d < 0 {
		d = 0
	}
	return float64(d) / 1000
}

// ---- lease file I/O ----

func defaultLeasePath() (string, error) {
	d, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "screen-control.lock"), nil
}

// readLeaseFrom returns the record, or (nil, nil) when the file is missing, empty,
// or corrupt — all of which are treated as "free".
func readLeaseFrom(path string) (*leaseRecord, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil // missing → free
	}
	var r leaseRecord
	if json.Unmarshal(b, &r) != nil || r.OwnerPID == 0 {
		return nil, nil // corrupt/empty → free
	}
	return &r, nil
}

// writeLeaseTo writes the record atomically (temp + rename). Callers hold the guard
// mutex, so the pid-tagged temp name never collides across processes.
func writeLeaseTo(path string, r *leaseRecord) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path) // os.Rename replaces an existing file on Windows
}

// ---- Win32 plumbing (named mutex + process liveness) ----

var (
	procCreateMutexW = modKernel32N.NewProc("CreateMutexW")
	procReleaseMutex = modKernel32N.NewProc("ReleaseMutex")
)

const (
	waitObject0    = 0x00000000
	waitAbandoned  = 0x00000080
)

// lockLeaseMutex acquires the session-local guard mutex, returning its handle. A
// WAIT_ABANDONED (a previous holder died mid-update) is treated as acquired.
func lockLeaseMutex() (windows.Handle, error) {
	namePtr, err := windows.UTF16PtrFromString(leaseMutexName)
	if err != nil {
		return 0, err
	}
	hRaw, _, _ := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(namePtr)))
	if hRaw == 0 {
		return 0, fmt.Errorf("CreateMutex(%s) failed", leaseMutexName)
	}
	h := windows.Handle(hRaw)
	ev, err := windows.WaitForSingleObject(h, leaseMutexWaitMS)
	if err != nil {
		windows.CloseHandle(h)
		return 0, err
	}
	switch ev {
	case waitObject0, waitAbandoned:
		return h, nil
	default: // WAIT_TIMEOUT or error
		windows.CloseHandle(h)
		return 0, fmt.Errorf("lease mutex wait returned 0x%x", ev)
	}
}

func unlockLeaseMutex(h windows.Handle) {
	procReleaseMutex.Call(uintptr(h))
	windows.CloseHandle(h)
}

// isProcessAlive reports whether pid is a currently-running process. It is biased
// toward "alive" on uncertainty (access-denied, unknown errors) so we never steal a
// lease from a live owner we simply can't query — the TTL is the backstop for those.
// Only a definitive "no such process" (ERROR_INVALID_PARAMETER) or a non-active exit
// code counts as dead.
func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if err == windows.ERROR_INVALID_PARAMETER {
			return false // no such pid
		}
		return true // access-denied / unknown → assume alive, don't steal
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return true // can't tell → assume alive
	}
	return code == stillActive
}
