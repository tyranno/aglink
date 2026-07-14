//go:build windows

package main

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Screen-control heads-up notice.
//
// When the agent starts driving the mouse/keyboard while the user may be typing
// or clicking themselves, their real input and our synthetic input can collide.
// So, at the START of a control session, we show a small, rounded, light toast in
// a screen corner ("AI가 화면 제어를 시작합니다 — 잠시 손을 떼어 주세요") with an amber status
// dot and accent border, that fades in, holds for a few seconds, and fades out. The
// wording is an anticipatory warning (control is about to begin, please pause), not
// a "control in progress" status — it appears just before synthetic input starts. It is
// throttled: once a session is under way (inputs within controlNoticeGap of each
// other) no further notice is shown; a fresh one only appears after an idle gap.
//
// Implementation: a self-owned WS_EX_LAYERED|TOPMOST|NOACTIVATE|TRANSPARENT popup
// drawn with user32/gdi32 only — no WinRT toast (which needs a registered
// AppUserModelID this CLI/child process lacks) and no new dependencies. NOACTIVATE
// + SW_SHOWNOACTIVATE never steal foreground from the window we are about to
// control; TRANSPARENT lets real user clicks pass through. A rounded window region
// + drop shadow + accent border + a fade envelope (driven by a ~60fps timer) make
// it look like a designed toast rather than a flashing text box. It runs on its
// own OS-locked goroutine with a message pump. (A colored icon is drawn as a GDI
// accent dot rather than an emoji, since classic GDI cannot render color emoji.)
//
// Completion ack: the start warning has a mirror-image counterpart. After any MCP
// tool call that actually drove the screen, a green "AI가 화면 제어를 마쳤습니다" toast
// is shown — same overlay, green accent instead of amber, a shorter duration since
// it is an after-the-fact acknowledgment rather than a warning to react to. The MCP
// tool-handler middleware detects a screen-driving call by watching syntheticInputCount
// (bumped in ensureControlNotice), so read-only tools (screenshot/snapshot/...) never
// trigger it. Because the start toast's on-screen time outlasts its ~1s lead, it is
// usually still fading when control finishes, so the completion ack PREEMPTS it (via
// noticeCloseRequested, polled by the overlay's own timer so DestroyWindow runs on the
// owning thread) — the stale "시작합니다" warning yields to "완료". It is fire-and-forget
// so the tool response is never delayed. Like the start toast, it always shows
// when notices are enabled — it is not gated on user presence, so a user watching
// the automation still sees the "완료" they asked for.
//
// Lead time: at session start, ensureControlNotice() shows the notice and then
// briefly BLOCKS before returning, so synthetic input begins only after the user
// has had a moment to see the warning and pause their own typing/clicking. The
// overlay is drawn on its own goroutine, so ensureControlNotice first waits for a
// "shown" handshake (the toast's first paint, bounded by noticeShownWaitMax) and
// only THEN sleeps noticeLeadMS — otherwise the lead sleep and the ShowWindow/paint
// would race and input could precede the visible warning. This lead applies ONLY
// on session start (the first input, or after an idle gap) — continuous control
// within a session proceeds with no delay, so it stays responsive.
//
//	AGLINK_NO_CONTROL_NOTICE=1      disable entirely (both start + completion; headless / no user present)
//	AGLINK_NOTICE_DURATION_MS=4500  override the start notice on-screen time (clamped 1500..15000)
//	AGLINK_NOTICE_LEAD_MS=1500      override the session-start lead delay (clamped 0..5000)
//	AGLINK_NOTICE_COMPLETE_MS=1800  override the completion ack on-screen time (clamped 1200..8000)

const (
	// controlNoticeGap is the idle time after which a new control session is
	// considered to have started and the notice is shown again.
	controlNoticeGap = 8 * time.Second

	// noticeDefaultMS / min / max bound the total on-screen time (fade in+hold+out).
	noticeDefaultMS = 3800
	noticeMinMS     = 1500
	noticeMaxMS     = 15000

	// Completion notice: a brief green acknowledgment shown at the end of each
	// control tool call ("AI가 화면 제어를 마쳤습니다"). Shorter than the start notice —
	// it is an ack after the fact, not a warning the user must react to before
	// input begins. Overridable with AGLINK_NOTICE_COMPLETE_MS (clamped).
	completeDefaultMS = 1800
	completeMinMS     = 1200
	completeMaxMS     = 8000

	// Fade envelope (subset of the total duration).
	noticeFadeInMS  = 170
	noticeFadeOutMS = 340
	noticeMaxAlpha  = 244

	// noticeDefaultLeadMS is how long, at session start, we hold off synthetic
	// input after showing the notice so the user can notice it and pause. Only
	// applied on session start, never mid-session.
	noticeDefaultLeadMS = 1000
	noticeMaxLeadMS     = 5000

	// userAwayThresholdMS: only when real (non-synthetic) input is older than
	// this is the user considered to have stepped away from the desk, letting
	// ensureControlNotice skip the session-start lead delay (no one to warn).
	// This must be MUCH longer than controlNoticeGap (8s) — a user who is
	// present but passively WATCHING the agent work (the common case, e.g.
	// during normal LLM thinking time between tool calls) routinely goes many
	// seconds without touching their own mouse/keyboard. Originally this reused
	// controlNoticeGap directly, which meant the lead delay — and with it the
	// entire point of the warning toast — was skipped on nearly every
	// session-start call, since 8s of silence is the norm, not the exception,
	// for someone just watching. Found live: 2026-07-09, "토스트가 제어와
	// 거의 동시에 뜬다" (the notice appears to fire right as control starts,
	// not before it). A few minutes of real silence is a far more honest
	// signal that nobody is there to see the warning.
	userAwayThresholdMS = int64(3 * time.Minute / time.Millisecond)
)

// userActiveWindowMS, userYieldPollEvery, userYieldMaxWait, and
// userResumeQuietMS are vars (not consts) purely so tests can shrink them —
// production code never mutates them after init. See beginSyntheticInput.
var (
	// userActiveWindowMS: real input newer than this is treated as "the user is
	// actively using the mouse/keyboard right now" — see beginSyntheticInput.
	userActiveWindowMS = int64(350)

	// userYieldPollEvery / userYieldMaxWait bound how long beginSyntheticInput
	// waits for an active user to pause before giving up and returning an error
	// instead of silently racing their input.
	userYieldPollEvery = 120 * time.Millisecond
	userYieldMaxWait   = 5 * time.Second

	// userResumeQuietMS is how long the user must stay quiet after a yield
	// before we treat control as safely resumable.
	userResumeQuietMS = int64(700)
)

var noticeText = "AI가 화면 제어를 시작합니다 — 손을 떼어 주세요"

// completeText is the wording of the green completion ack. Kept shorter than
// noticeText so it comfortably fits the same overlay box (see TestCompleteTextFitsBox).
var completeText = "AI가 화면 제어를 마쳤습니다"

var (
	lastSyntheticInput atomic.Int64 // UnixNano of the last synthetic input
	noticeShowing      atomic.Bool  // guards against stacking overlays
	controlNoticeOff   = os.Getenv("AGLINK_NO_CONTROL_NOTICE") != ""
)

// syntheticInputCount increments once per control op that passes the user-yield
// gate (see ensureControlNotice). The MCP tool-handler middleware snapshots it
// around each tool call: if it advanced, that call actually drove the screen and
// a completion notice is shown afterward. Read-only tools never touch it.
var syntheticInputCount atomic.Int64

// noticeCloseRequested asks the currently-showing overlay to self-destroy on its
// next timer tick — on its own OS thread, the only safe place to DestroyWindow.
// showControlComplete sets it to preempt a still-fading start toast so the "완료"
// ack appears immediately instead of being swallowed by the one-overlay guard.
var noticeCloseRequested atomic.Bool

// noticeDurationMS returns the total on-screen time, honoring
// AGLINK_NOTICE_DURATION_MS (clamped) and defaulting to noticeDefaultMS.
func noticeDurationMS() int {
	d := noticeDefaultMS
	if v := os.Getenv("AGLINK_NOTICE_DURATION_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			d = n
		}
	}
	if d < noticeMinMS {
		d = noticeMinMS
	}
	if d > noticeMaxMS {
		d = noticeMaxMS
	}
	return d
}

// completeDurationMS returns the completion notice's total on-screen time,
// honoring AGLINK_NOTICE_COMPLETE_MS (clamped) and defaulting to completeDefaultMS.
func completeDurationMS() int {
	d := completeDefaultMS
	if v := os.Getenv("AGLINK_NOTICE_COMPLETE_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			d = n
		}
	}
	if d < completeMinMS {
		d = completeMinMS
	}
	if d > completeMaxMS {
		d = completeMaxMS
	}
	return d
}

// noticeLeadMS returns the session-start lead delay in ms, honoring
// AGLINK_NOTICE_LEAD_MS (clamped 0..noticeMaxLeadMS) and defaulting to
// noticeDefaultLeadMS. 0 disables the delay.
func noticeLeadMS() int {
	d := noticeDefaultLeadMS
	if v := os.Getenv("AGLINK_NOTICE_LEAD_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			d = n
		}
	}
	if d < 0 {
		d = 0
	}
	if d > noticeMaxLeadMS {
		d = noticeMaxLeadMS
	}
	return d
}

// ensureControlNotice is called at the entry of every function that synthesizes
// input. It records the input time and, if this is the first input of a new
// control session (>= controlNoticeGap since the previous one), shows the overlay
// and blocks until it is on screen (plus the lead delay) before returning, so the
// warning is always visible before synthetic input begins. Mid-session calls
// return immediately.
func ensureControlNotice() {
	// A control op has passed the yield gate and is about to drive the screen.
	// Count it so the tool-handler middleware can tell this tool call apart from
	// a read-only one and fire a completion notice afterward.
	syntheticInputCount.Add(1)

	now := time.Now().UnixNano()
	prev := lastSyntheticInput.Swap(now)
	if controlNoticeOff {
		return
	}
	if noticeDue(prev, now) {
		// Show the toast and wait until it is actually painted on screen before
		// starting the lead delay. The overlay is drawn on its own goroutine, so
		// without this handshake the fixed lead sleep and the ShowWindow/first
		// paint race — under scheduling pressure synthetic input could begin
		// before the warning was ever visible. Bounded by noticeShownWaitMax so a
		// stalled UI thread can never block input indefinitely.
		if shown := showControlNotice(); shown != nil {
			select {
			case <-shown:
			case <-time.After(noticeShownWaitMax):
			}
		}
		// Session start: hold off briefly so the user sees the notice and can
		// pause their own input before synthetic control begins. Mid-session
		// calls skip this (noticeDue is false), so control stays responsive.
		//
		// This blind delay used to apply unconditionally, which meant every LLM
		// tool call in a multi-step flow (turns are almost always spaced further
		// apart than controlNoticeGap) paid the full tax even when nobody was at
		// the keyboard. Now that the low-level input watcher gives ground truth,
		// skip it when the watcher is active AND has not seen real input within
		// userAwayThresholdMS — there is no one to warn. If the watcher failed to
		// install, userWatcherOK is false and we keep the old unconditional delay
		// so the safety warning never silently disappears.
		skipLead := userWatcherOK.Load() && msSinceRealUserInput() >= userAwayThresholdMS
		if lead := noticeLeadMS(); lead > 0 && !skipLead {
			time.Sleep(time.Duration(lead) * time.Millisecond)
		}
	}
}

// beginSyntheticInput is the single gate every input-synthesizing function
// (mouseMove, mouseClick, typeText, keyCombo, scroll, uiaInvoke, ...) calls
// before sending anything, replacing a bare ensureControlNotice() call.
//
// If the input watcher shows the user is actively driving the mouse/keyboard
// right now, control yields to them: we wait (bounded by userYieldMaxWait)
// for a quiet gap instead of racing their input, and give up with an error
// rather than silently colliding if they never pause. Once they do go quiet,
// we force a fresh session-start notice (lastSyntheticInput reset) so they
// see an explicit "AI is resuming control" warning before automation
// continues — the "다시 확인하고 진행" behavior. When the user was never
// active to begin with, this just delegates to ensureControlNotice, whose own
// lead-delay skip (above) removes the fixed tax for that common case.
func beginSyntheticInput() error {
	// Cross-process gate first: if another teleclaude session currently owns the
	// screen, refuse immediately (SCREEN_BUSY) — no point yielding to the human or
	// showing a notice for a control op we are not allowed to perform. See
	// docs/control-ownership.md.
	if err := acquireControlLease(); err != nil {
		return err
	}

	installUserInputWatcher()

	yielded := false
	deadline := time.Now().Add(userYieldMaxWait)
	for userWatcherOK.Load() && msSinceRealUserInput() < userActiveWindowMS {
		yielded = true
		if time.Now().After(deadline) {
			return fmt.Errorf("user is actively using the mouse/keyboard — pausing automation instead of colliding with their input; wait a moment and retry")
		}
		time.Sleep(userYieldPollEvery)
	}
	if yielded {
		// The user just released control. Wait for a real quiet gap (not just
		// the instant activity crossed userActiveWindowMS) before resuming.
		for msSinceRealUserInput() < userResumeQuietMS {
			time.Sleep(userYieldPollEvery)
		}
		lastSyntheticInput.Store(0) // forces noticeDue() true on the next call below
	}
	ensureControlNotice()
	return nil
}

// noticeDue reports whether a control-start notice should be shown: on the very
// first input (prev == 0) or after at least controlNoticeGap of idle since the
// previous synthetic input.
func noticeDue(prevNano, nowNano int64) bool {
	return prevNano == 0 || nowNano-prevNano >= int64(controlNoticeGap)
}

// noticeShow runs the overlay window to completion. It is a package var (rather
// than calling runNoticeWindow directly) so tests can substitute a fake that
// drives the shown-signal without creating real Win32 windows.
var noticeShow = runNoticeWindow

// noticeShownWaitMax bounds how long ensureControlNotice waits for the toast to
// appear before proceeding anyway, so a stalled UI thread can never block
// synthetic input forever. A var for testability.
var noticeShownWaitMax = 2 * time.Second

var (
	noticeShownOnce sync.Once     // guards a single close of noticeShownCh per showing
	noticeShownCh   chan struct{} // closed when the current toast is on screen
)

// signalNoticeShown closes the current showing's shown-channel exactly once,
// unblocking ensureControlNotice. Called from the paint handler on the toast's
// first paint, and as a fallback when the overlay goroutine exits without ever
// painting (e.g. window creation failed) so the caller is never left blocked.
func signalNoticeShown() {
	noticeShownOnce.Do(func() {
		if noticeShownCh != nil {
			close(noticeShownCh)
		}
	})
}

// noticeCloseWaitMax bounds how long showControlComplete waits for a preempted
// start toast to tear down before giving up. Teardown happens on the overlay's
// next ~60fps timer tick, so this is generous; a var for testability.
var noticeCloseWaitMax = 400 * time.Millisecond

// showNoticeOverlay spawns the overlay on a dedicated goroutine using the given
// style and total on-screen time. The fade runs asynchronously, but the returned
// channel is closed the moment the toast is on screen (first paint) — or if it
// cannot be shown. Returns nil when a notice is already showing (nothing new to
// wait on). Shared by the start-warning and completion-ack paths.
func showNoticeOverlay(style *noticeStyle, totalMS int) <-chan struct{} {
	if !noticeShowing.CompareAndSwap(false, true) {
		return nil // one already on screen
	}
	// A fresh overlay must not inherit a preempt request aimed at the previous one.
	noticeCloseRequested.Store(false)
	activeStyle = style
	pendingNoticeTotalMS = totalMS
	noticeShownOnce = sync.Once{}
	shown := make(chan struct{})
	noticeShownCh = shown
	go func() {
		defer noticeShowing.Store(false)
		defer signalNoticeShown() // fallback: never leave the caller blocked
		noticeShow()
	}()
	return shown
}

// showControlNotice shows the amber start warning. The returned channel is closed
// the moment the toast is actually on screen (first paint) — or if it cannot be
// shown — so the caller can guarantee the warning is visible before synthetic
// input begins. Returns nil when a notice is already showing.
func showControlNotice() <-chan struct{} {
	return showNoticeOverlay(&startStyle, noticeDurationMS())
}

// showControlComplete shows the green "control complete" ack. It is fire-and-forget
// (the MCP middleware calls it as `go showControlComplete()`), so it may block
// internally to preempt a still-fading start toast without delaying the tool
// response. It no-ops when notices are disabled or nobody is at the desk to see it.
func showControlComplete() {
	if controlNoticeOff {
		return
	}
	// NB: unlike the start-notice lead delay, the completion ack is NOT skipped
	// when the user appears "away". The start toast itself always shows regardless
	// of presence (only its lead *delay* is skipped), and the completion must be
	// just as reliably visible — the user explicitly asked to always see "완료".
	// A user passively WATCHING automation can easily be input-idle past the away
	// threshold, and suppressing the ack then would hide exactly the message they
	// wanted. The worst case (a brief green flash at a truly empty desk) is
	// harmless and matches how the start notice already behaves.
	//
	// Preempt any still-showing overlay (typically the start warning, whose 3.8s
	// duration outlasts the ~1s lead) so the ack appears immediately rather than
	// being dropped by the one-overlay guard. Semantically right too: once control
	// is done, the stale "시작합니다" warning should yield to "완료".
	if noticeShowing.Load() {
		noticeCloseRequested.Store(true)
		deadline := time.Now().Add(noticeCloseWaitMax)
		for noticeShowing.Load() && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
	}
	showNoticeOverlay(&completeStyle, completeDurationMS())
}

// noticeAlpha returns the layered-window alpha for a fade in → hold → fade out
// envelope at elapsedMS into a totalMS-long showing.
func noticeAlpha(elapsedMS, totalMS int) byte {
	switch {
	case elapsedMS < 0:
		return 0
	case elapsedMS < noticeFadeInMS:
		return byte(noticeMaxAlpha * elapsedMS / noticeFadeInMS)
	case elapsedMS < totalMS-noticeFadeOutMS:
		return noticeMaxAlpha
	case elapsedMS < totalMS:
		return byte(noticeMaxAlpha * (totalMS - elapsedMS) / noticeFadeOutMS)
	default:
		return 0
	}
}

// ---- Win32 plumbing ----

var (
	modUser32N   = windows.NewLazySystemDLL("user32.dll")
	modGdi32N    = windows.NewLazySystemDLL("gdi32.dll")
	modKernel32N = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassExN      = modUser32N.NewProc("RegisterClassExW")
	procCreateWindowExN       = modUser32N.NewProc("CreateWindowExW")
	procDefWindowProcN        = modUser32N.NewProc("DefWindowProcW")
	procShowWindowN           = modUser32N.NewProc("ShowWindow")
	procDestroyWindowN        = modUser32N.NewProc("DestroyWindow")
	procSetLayeredWinAttrN    = modUser32N.NewProc("SetLayeredWindowAttributes")
	procSetWindowRgnN         = modUser32N.NewProc("SetWindowRgn")
	procSetTimerN             = modUser32N.NewProc("SetTimer")
	procKillTimerN            = modUser32N.NewProc("KillTimer")
	procGetMessageN           = modUser32N.NewProc("GetMessageW")
	procTranslateMessageN     = modUser32N.NewProc("TranslateMessage")
	procDispatchMessageN      = modUser32N.NewProc("DispatchMessageW")
	procPostQuitMessageN      = modUser32N.NewProc("PostQuitMessage")
	procBeginPaintN           = modUser32N.NewProc("BeginPaint")
	procEndPaintN             = modUser32N.NewProc("EndPaint")
	procDrawTextWN            = modUser32N.NewProc("DrawTextW")
	procSystemParametersInfoN = modUser32N.NewProc("SystemParametersInfoW")
	procMonitorFromWindowN    = modUser32N.NewProc("MonitorFromWindow")
	procGetMonitorInfoN       = modUser32N.NewProc("GetMonitorInfoW")

	procCreateSolidBrushN   = modGdi32N.NewProc("CreateSolidBrush")
	procCreatePenN          = modGdi32N.NewProc("CreatePen")
	procCreateFontWN        = modGdi32N.NewProc("CreateFontW")
	procCreateRoundRectRgnN = modGdi32N.NewProc("CreateRoundRectRgn")
	procRoundRectN          = modGdi32N.NewProc("RoundRect")
	procEllipseN            = modGdi32N.NewProc("Ellipse")
	procSelectObjectN       = modGdi32N.NewProc("SelectObject")
	procSetTextColorN       = modGdi32N.NewProc("SetTextColor")
	procSetBkModeN          = modGdi32N.NewProc("SetBkMode")

	procGetModuleHandleN = modKernel32N.NewProc("GetModuleHandleW")
)

const (
	wsPopup         = 0x80000000
	wsExTopmost     = 0x00000008
	wsExTransparent = 0x00000020
	wsExToolWindow  = 0x00000080
	wsExLayered     = 0x00080000
	wsExNoActivate  = 0x08000000

	csDropShadow     = 0x00020000
	swShowNoActivate = 4
	lwaAlpha         = 0x2

	wmDestroy = 0x0002
	wmPaint   = 0x000F
	wmTimer   = 0x0113

	spiGetWorkArea          = 0x0030
	monitorDefaultToNearest = 0x00000002
	bkTransparent           = 1
	psSolid                 = 0

	dtLeft       = 0x0
	dtVCenter    = 0x4
	dtSingleLine = 0x20
	dtNoPrefix   = 0x800

	hangeulCharset = 129
	fwBold         = 700

	noticeTimerID = 1

	// Overlay geometry (physical pixels; the process is DPI-aware). Compact,
	// single-line — roughly half the earlier footprint.
	noticeW      = 430
	noticeH      = 52
	noticeRadius = 14
	noticeMargin = 24
)

// COLORREF is 0x00BBGGRR.
const (
	noticeBgColor     = 0x00F2F1EF // #EFF1F2 bright off-white
	noticeAccentColor = 0x001C90E8 // #E8901C amber accent (start: border + status dot)
	noticeGreenColor  = 0x005B9E2E // #2E9E5B green accent (completion: border + status dot)
	noticeTextColor   = 0x00262220 // #202226 dark slate text
)

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type msgStruct struct {
	hwnd    uintptr
	message uint32
	_       uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	ptX     int32
	ptY     int32
	_       uint32
}

type paintStruct struct {
	hdc         uintptr
	fErase      int32
	rcPaint     rect
	fRestore    int32
	fIncUpdate  int32
	rgbReserved [32]byte
}

var (
	noticeClassOnce sync.Once
	noticeClassOK   bool
	noticeClassName = windows.StringToUTF16Ptr("AglinkControlNotice")

	// GDI objects created once and reused for the process lifetime.
	noticeFont        uintptr
	noticeBgBrush     uintptr
	noticeAccentBrush uintptr // amber (start)
	noticeAccentPen   uintptr // amber (start)
	noticeGreenBrush  uintptr // green (completion)
	noticeGreenPen    uintptr // green (completion)

	// Per-showing state (only one overlay at a time, guarded by noticeShowing).
	noticeStartNano int64
	noticeTotalMS   int
)

// noticeStyle is the per-showing appearance: the amber start warning vs the green
// completion ack. activeStyle points at whichever overlay is currently on screen
// and paintNotice reads it. The GDI handles are filled in by registerNoticeClass.
type noticeStyle struct {
	text        string
	accentPen   uintptr
	accentBrush uintptr
}

var (
	startStyle    noticeStyle
	completeStyle noticeStyle
	activeStyle   = &startStyle

	// pendingNoticeTotalMS is the total on-screen time for the next overlay; a
	// show initiator sets it before the overlay goroutine starts and
	// runNoticeWindow copies it into noticeTotalMS.
	pendingNoticeTotalMS = noticeDefaultMS
)

// registerNoticeClass registers the overlay window class and its GDI resources
// once, returning whether registration succeeded.
func registerNoticeClass() bool {
	noticeClassOnce.Do(func() {
		hInst, _, _ := procGetModuleHandleN.Call(0)
		noticeBgBrush, _, _ = procCreateSolidBrushN.Call(uintptr(noticeBgColor))
		noticeAccentBrush, _, _ = procCreateSolidBrushN.Call(uintptr(noticeAccentColor))
		noticeAccentPen, _, _ = procCreatePenN.Call(psSolid, 2, uintptr(noticeAccentColor))
		noticeGreenBrush, _, _ = procCreateSolidBrushN.Call(uintptr(noticeGreenColor))
		noticeGreenPen, _, _ = procCreatePenN.Call(psSolid, 2, uintptr(noticeGreenColor))
		// Malgun Gothic renders Latin + Hangul; -16 ≈ 12pt @96dpi, bold.
		face := windows.StringToUTF16Ptr("Malgun Gothic")
		noticeFont, _, _ = procCreateFontWN.Call(
			^uintptr(0)-15, // height = -16
			0, 0, 0, fwBold, 0, 0, 0,
			hangeulCharset, 0, 0, 0, 0,
			uintptr(unsafe.Pointer(face)),
		)
		wc := wndClassExW{
			cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
			style:         csDropShadow,
			lpfnWndProc:   syscall.NewCallback(noticeWndProc),
			hInstance:     hInst,
			hbrBackground: noticeBgBrush,
			lpszClassName: noticeClassName,
		}
		atom, _, _ := procRegisterClassExN.Call(uintptr(unsafe.Pointer(&wc)))
		noticeClassOK = atom != 0

		// Fill the style structs now that the GDI handles exist. activeStyle
		// already points at startStyle, so populating it here makes the default
		// showing amber without any further wiring.
		startStyle = noticeStyle{text: noticeText, accentPen: noticeAccentPen, accentBrush: noticeAccentBrush}
		completeStyle = noticeStyle{text: completeText, accentPen: noticeGreenPen, accentBrush: noticeGreenBrush}
	})
	return noticeClassOK
}

// noticeWndProc paints the toast and advances the fade envelope, self-destructing
// when the duration elapses.
func noticeWndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	switch uint32(msg) {
	case wmPaint:
		var ps paintStruct
		hdc, _, _ := procBeginPaintN.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		if hdc != 0 {
			paintNotice(hdc)
			procEndPaintN.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		}
		// The toast is now on screen; release any caller waiting to begin input.
		signalNoticeShown()
		return 0
	case wmTimer:
		elapsed := int((time.Now().UnixNano() - noticeStartNano) / 1e6)
		// Self-destruct when the duration elapses OR when a preempt was requested
		// (showControlComplete replacing a still-fading start toast). DestroyWindow
		// must run on this owning thread, which is why the request is a flag polled
		// here rather than a cross-thread DestroyWindow call.
		if elapsed >= noticeTotalMS || noticeCloseRequested.Load() {
			procKillTimerN.Call(hwnd, wParam)
			procDestroyWindowN.Call(hwnd)
			return 0
		}
		procSetLayeredWinAttrN.Call(hwnd, 0, uintptr(noticeAlpha(elapsed, noticeTotalMS)), lwaAlpha)
		return 0
	case wmDestroy:
		procPostQuitMessageN.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcN.Call(hwnd, msg, wParam, lParam)
	return r
}

// paintNotice draws the toast contents into hdc. The client DC starts at (0,0)
// and the popup is a fixed noticeW x noticeH size, so client coords are known.
func paintNotice(hdc uintptr) {
	// The active style (amber start vs green completion) supplies the accent
	// pen/brush and the text; everything else is shared.
	style := activeStyle

	// Rounded background + accent border (RoundRect uses the selected brush+pen).
	oldBrush, _, _ := procSelectObjectN.Call(hdc, noticeBgBrush)
	oldPen, _, _ := procSelectObjectN.Call(hdc, style.accentPen)
	procRoundRectN.Call(hdc, 1, 1, noticeW-1, noticeH-1, noticeRadius, noticeRadius)

	// Status dot (filled accent circle) on the left, vertically centered.
	const dotR = 6
	dotCx, dotCy := 24, noticeH/2
	procSelectObjectN.Call(hdc, style.accentBrush)
	procEllipseN.Call(hdc, uintptr(dotCx-dotR), uintptr(dotCy-dotR), uintptr(dotCx+dotR), uintptr(dotCy+dotR))

	// Text, left-aligned after the dot so the prefix always stays visible.
	procSetBkModeN.Call(hdc, bkTransparent)
	procSetTextColorN.Call(hdc, uintptr(noticeTextColor))
	var oldFont uintptr
	if noticeFont != 0 {
		oldFont, _, _ = procSelectObjectN.Call(hdc, noticeFont)
	}
	txtRc := rect{Left: 40, Top: 0, Right: noticeW - 16, Bottom: noticeH}
	u := windows.StringToUTF16(style.text)
	procDrawTextWN.Call(hdc, uintptr(unsafe.Pointer(&u[0])), ^uintptr(0),
		uintptr(unsafe.Pointer(&txtRc)), uintptr(dtLeft|dtVCenter|dtSingleLine|dtNoPrefix))

	// Restore original GDI selections.
	if oldFont != 0 {
		procSelectObjectN.Call(hdc, oldFont)
	}
	if oldPen != 0 {
		procSelectObjectN.Call(hdc, oldPen)
	}
	if oldBrush != 0 {
		procSelectObjectN.Call(hdc, oldBrush)
	}
}

// monitorInfo mirrors Win32's MONITORINFO (RectMonitor/RectWork use the
// existing `rect` type from screen_apps_windows.go).
type monitorInfo struct {
	CbSize    uint32
	RcMonitor rect
	RcWork    rect
	DwFlags   uint32
}

// foregroundMonitorWorkArea returns the work-area rect of the monitor
// currently showing the foreground window, falling back to the primary
// monitor's work area (SPI_GETWORKAREA) if the foreground window can't be
// resolved or GetMonitorInfo fails — the previous, monitor-agnostic behavior.
func foregroundMonitorWorkArea() rect {
	fallback := func() rect {
		var wa rect
		procSystemParametersInfoN.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&wa)), 0)
		return wa
	}
	fg, _, _ := procGetForegroundWindow.Call()
	if fg == 0 {
		return fallback()
	}
	hMonitor, _, _ := procMonitorFromWindowN.Call(fg, uintptr(monitorDefaultToNearest))
	if hMonitor == 0 {
		return fallback()
	}
	mi := monitorInfo{CbSize: uint32(unsafe.Sizeof(monitorInfo{}))}
	if r, _, _ := procGetMonitorInfoN.Call(hMonitor, uintptr(unsafe.Pointer(&mi))); r == 0 {
		return fallback()
	}
	return mi.RcWork
}

// runNoticeWindow creates the overlay in the bottom-right of the work area, shows
// it without activation, and pumps its messages until the fade timer destroys it.
func runNoticeWindow() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if !registerNoticeClass() {
		return
	}

	// Position on the work area of the monitor showing the foreground window —
	// not always the primary monitor's SPI_GETWORKAREA, which is what this used
	// to do. On a multi-monitor setup that placed the notice on whichever
	// monitor happens to be primary regardless of where the actual controlled
	// window (and the user's attention) was, so a warning meant to be seen
	// before synthetic input landed could go entirely unnoticed on another
	// screen. foregroundMonitorWorkArea falls back to the primary monitor's
	// work area if the foreground window can't be resolved.
	wa := foregroundMonitorWorkArea()
	x := int(wa.Right) - noticeW - noticeMargin
	y := int(wa.Bottom) - noticeH - noticeMargin

	hInst, _, _ := procGetModuleHandleN.Call(0)
	hwnd, _, _ := procCreateWindowExN.Call(
		uintptr(wsExTopmost|wsExLayered|wsExToolWindow|wsExNoActivate|wsExTransparent),
		uintptr(unsafe.Pointer(noticeClassName)),
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("aglink-screen"))),
		uintptr(wsPopup),
		uintptr(int32(x)), uintptr(int32(y)), uintptr(noticeW), uintptr(noticeH),
		0, 0, hInst, 0,
	)
	if hwnd == 0 {
		return
	}

	// Round the whole window (so the corners are actually clipped) and start fully
	// transparent so the fade-in is smooth.
	if rgn, _, _ := procCreateRoundRectRgnN.Call(0, 0, noticeW+1, noticeH+1, noticeRadius, noticeRadius); rgn != 0 {
		procSetWindowRgnN.Call(hwnd, rgn, 1)
	}
	procSetLayeredWinAttrN.Call(hwnd, 0, 0, lwaAlpha)

	noticeTotalMS = pendingNoticeTotalMS
	noticeStartNano = time.Now().UnixNano()

	procShowWindowN.Call(hwnd, swShowNoActivate)
	procSetTimerN.Call(hwnd, noticeTimerID, 15, 0) // ~60fps fade envelope

	var msg msgStruct
	for {
		r, _, _ := procGetMessageN.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 { // 0 = WM_QUIT, -1 = error
			break
		}
		procTranslateMessageN.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageN.Call(uintptr(unsafe.Pointer(&msg)))
	}
}
