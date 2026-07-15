//go:build windows

package main

import (
	"testing"
	"time"
)

// fakeAbsentUserInput / fakePresentUserInput are handy constants for tests
// that stub msSinceRealUserInput with a fixed value rather than a real clock.
const (
	fakeAbsentUserInputMS  = int64(999_999) // "long ago" — nobody at the controls
	fakePresentUserInputMS = int64(100)     // recently, but not RIGHT now
)

// withFastNotice stubs noticeShow so ensureControlNotice's toast-paint wait
// resolves instantly instead of creating a real Win32 window, mirroring the
// pattern in screen_notify_windows_test.go. Returns a restore func.
func withFastNotice(t *testing.T) {
	t.Helper()
	origRunner := noticeShow
	noticeShow = func() { signalNoticeShown() }
	t.Cleanup(func() {
		noticeShow = origRunner
		noticeShowing.Store(false)
	})
}

// TestEnsureControlNoticeSkipsLeadWhenUserAbsent is the core bottleneck fix:
// when the watcher is active and confirms nobody has touched the mouse/
// keyboard recently, the blind lead-delay must be skipped — there is no one
// to warn — instead of taxing every session-start call.
func TestEnsureControlNoticeSkipsLeadWhenUserAbsent(t *testing.T) {
	t.Setenv("AGLINK_NOTICE_LEAD_MS", "250")
	withFastNotice(t)

	origOK := userWatcherOK.Load()
	origProbe := msSinceRealUserInput
	origObserved := hasObservedRealInput
	t.Cleanup(func() {
		userWatcherOK.Store(origOK)
		msSinceRealUserInput = origProbe
		hasObservedRealInput = origObserved
		lastSyntheticInput.Store(0)
	})
	userWatcherOK.Store(true)
	hasObservedRealInput = func() bool { return true }
	msSinceRealUserInput = func() int64 { return fakeAbsentUserInputMS }
	lastSyntheticInput.Store(0) // force noticeDue

	start := time.Now()
	ensureControlNotice()
	elapsed := time.Since(start)

	if elapsed >= 250*time.Millisecond {
		t.Errorf("ensureControlNotice took %v with an absent user — the lead delay should have been skipped", elapsed)
	}
}

// TestEnsureControlNoticeKeepsLeadWhenUserRecentlyPresent verifies the other
// side: if the watcher shows real input within userAwayThresholdMS, the
// session-start lead delay must still apply — someone may be about to
// collide with our synthetic input.
func TestEnsureControlNoticeKeepsLeadWhenUserRecentlyPresent(t *testing.T) {
	t.Setenv("AGLINK_NOTICE_LEAD_MS", "150")
	withFastNotice(t)

	origOK := userWatcherOK.Load()
	origProbe := msSinceRealUserInput
	t.Cleanup(func() {
		userWatcherOK.Store(origOK)
		msSinceRealUserInput = origProbe
		lastSyntheticInput.Store(0)
	})
	userWatcherOK.Store(true)
	msSinceRealUserInput = func() int64 { return fakePresentUserInputMS }
	lastSyntheticInput.Store(0)

	start := time.Now()
	ensureControlNotice()
	elapsed := time.Since(start)

	if elapsed < 150*time.Millisecond {
		t.Errorf("ensureControlNotice took only %v with a recently-present user — the lead delay should have applied", elapsed)
	}
}

// TestEnsureControlNoticeKeepsLeadDuringNormalThinkingGap guards a live-found
// regression: a user just watching the agent work (not touching their own
// mouse/keyboard) routinely goes 8-10s of silence between tool calls — that
// used to be misread as "the user stepped away" (it reused controlNoticeGap,
// 8s, as the absence threshold) and skipped the lead delay, so the warning
// toast ended up firing with no perceptible pause before control resumed
// (reported live 2026-07-09: "토스트가 제어와 거의 동시에 뜬다"). 10s of
// silence must still be well within "present" now that the threshold is
// userAwayThresholdMS (3 minutes) — only real, multi-minute absence skips it.
func TestEnsureControlNoticeKeepsLeadDuringNormalThinkingGap(t *testing.T) {
	t.Setenv("AGLINK_NOTICE_LEAD_MS", "150")
	withFastNotice(t)

	origOK := userWatcherOK.Load()
	origProbe := msSinceRealUserInput
	t.Cleanup(func() {
		userWatcherOK.Store(origOK)
		msSinceRealUserInput = origProbe
		lastSyntheticInput.Store(0)
	})
	userWatcherOK.Store(true)
	msSinceRealUserInput = func() int64 { return int64(10 * time.Second / time.Millisecond) }
	lastSyntheticInput.Store(0)

	start := time.Now()
	ensureControlNotice()
	elapsed := time.Since(start)

	if elapsed < 150*time.Millisecond {
		t.Errorf("ensureControlNotice took only %v after a normal 10s thinking gap — the lead delay must still apply; only real absence (userAwayThresholdMS) should skip it", elapsed)
	}
}

// TestEnsureControlNoticeKeepsLeadWhenWatcherUnavailable ensures that if hook
// installation ever failed (userWatcherOK stays false), the old unconditional
// delay is preserved rather than silently disabling the safety warning.
func TestEnsureControlNoticeKeepsLeadWhenWatcherUnavailable(t *testing.T) {
	t.Setenv("AGLINK_NOTICE_LEAD_MS", "150")
	withFastNotice(t)

	origOK := userWatcherOK.Load()
	origProbe := msSinceRealUserInput
	t.Cleanup(func() {
		userWatcherOK.Store(origOK)
		msSinceRealUserInput = origProbe
		lastSyntheticInput.Store(0)
	})
	userWatcherOK.Store(false)
	msSinceRealUserInput = func() int64 { return fakeAbsentUserInputMS } // would skip if the watcher were OK
	lastSyntheticInput.Store(0)

	start := time.Now()
	ensureControlNotice()
	elapsed := time.Since(start)

	if elapsed < 150*time.Millisecond {
		t.Errorf("ensureControlNotice took only %v with the watcher unavailable — must fall back to the unconditional delay", elapsed)
	}
}

// TestBeginSyntheticInputYieldsThenErrorsOnTimeout simulates a user who never
// stops touching the mouse/keyboard: beginSyntheticInput must yield (not
// collide) and eventually give up with an error rather than blocking forever.
func TestBeginSyntheticInputYieldsThenErrorsOnTimeout(t *testing.T) {
	origInstall := installUserInputWatcher
	origOK := userWatcherOK.Load()
	origProbe := msSinceRealUserInput
	origObserved := hasObservedRealInput
	origActiveWindow := userActiveWindowMS
	origPollEvery := userYieldPollEvery
	origMaxWait := userYieldMaxWait
	t.Cleanup(func() {
		installUserInputWatcher = origInstall
		userWatcherOK.Store(origOK)
		msSinceRealUserInput = origProbe
		hasObservedRealInput = origObserved
		userActiveWindowMS = origActiveWindow
		userYieldPollEvery = origPollEvery
		userYieldMaxWait = origMaxWait
	})

	installUserInputWatcher = func() {} // no real hook in tests
	userWatcherOK.Store(true)
	hasObservedRealInput = func() bool { return true }
	msSinceRealUserInput = func() int64 { return 0 } // "user is active" forever
	userActiveWindowMS = 50
	userYieldPollEvery = 10 * time.Millisecond
	userYieldMaxWait = 80 * time.Millisecond

	start := time.Now()
	err := beginSyntheticInput()
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected beginSyntheticInput to give up and return an error when the user never pauses")
	}
	if elapsed < userYieldMaxWait {
		t.Errorf("returned after %v, expected to wait out userYieldMaxWait (%v) before giving up", elapsed, userYieldMaxWait)
	}
	if elapsed > userYieldMaxWait+500*time.Millisecond {
		t.Errorf("returned after %v — the timeout did not bound the wait", elapsed)
	}
}

// TestBeginSyntheticInputResumesAfterUserGoesQuiet simulates a user who is
// active for a while and then stops: beginSyntheticInput must wait through
// the active period, wait an additional quiet gap, and then proceed
// (re-arming the session-start notice) rather than erroring out.
func TestBeginSyntheticInputResumesAfterUserGoesQuiet(t *testing.T) {
	t.Setenv("AGLINK_NOTICE_LEAD_MS", "0") // isolate this test from the lead delay
	withFastNotice(t)

	origInstall := installUserInputWatcher
	origOK := userWatcherOK.Load()
	origProbe := msSinceRealUserInput
	origObserved := hasObservedRealInput
	origActiveWindow := userActiveWindowMS
	origPollEvery := userYieldPollEvery
	origMaxWait := userYieldMaxWait
	origQuiet := userResumeQuietMS
	t.Cleanup(func() {
		installUserInputWatcher = origInstall
		userWatcherOK.Store(origOK)
		msSinceRealUserInput = origProbe
		hasObservedRealInput = origObserved
		userActiveWindowMS = origActiveWindow
		userYieldPollEvery = origPollEvery
		userYieldMaxWait = origMaxWait
		userResumeQuietMS = origQuiet
		lastSyntheticInput.Store(0)
	})

	installUserInputWatcher = func() {}
	userWatcherOK.Store(true)
	hasObservedRealInput = func() bool { return true }
	userActiveWindowMS = 50
	userYieldPollEvery = 10 * time.Millisecond
	userYieldMaxWait = 2 * time.Second
	userResumeQuietMS = 60

	// The user is "active" (msSince stays under userActiveWindowMS) for the
	// first 150ms of the test, then goes quiet — msSince starts counting up
	// from that point, as the real hook would report.
	const activePeriod = 150 * time.Millisecond
	start := time.Now()
	msSinceRealUserInput = func() int64 {
		elapsed := time.Since(start)
		if elapsed < activePeriod {
			return 0
		}
		return int64((elapsed - activePeriod) / time.Millisecond)
	}
	lastSyntheticInput.Store(time.Now().UnixNano())

	err := beginSyntheticInput()
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected beginSyntheticInput to resume after the user went quiet, got error: %v", err)
	}
	if elapsed < activePeriod {
		t.Errorf("returned after only %v — should have waited out the active period (%v)", elapsed, activePeriod)
	}
	if got := lastSyntheticInput.Load(); got == 0 {
		t.Error("expected lastSyntheticInput to be re-armed by the final ensureControlNotice call")
	}
}

// TestEnsureControlNoticeKeepsLeadWhenInputNeverObserved is the regression
// test for the live incident found 2026-07-15 ("제어하고 나서 알림이 뜬다" —
// control happens, then the notice appears). aglink-screen.exe is respawned
// fresh for every conversation turn, so lastRealUserInputNano is still 0 (no
// genuine event observed yet) on essentially every session-start call in
// practice — this must NOT be read as "confirmed nobody's home" and must NOT
// skip the safety lead. Deliberately exercises the real hasObservedRealInput/
// msSinceRealUserInput implementations (no stubbing) against a freshly-reset
// lastRealUserInputNano, mirroring an actual fresh-process cold start.
func TestEnsureControlNoticeKeepsLeadWhenInputNeverObserved(t *testing.T) {
	t.Setenv("AGLINK_NOTICE_LEAD_MS", "150")
	withFastNotice(t)

	origOK := userWatcherOK.Load()
	origLast := lastRealUserInputNano.Load()
	t.Cleanup(func() {
		userWatcherOK.Store(origOK)
		lastRealUserInputNano.Store(origLast)
		lastSyntheticInput.Store(0)
	})
	userWatcherOK.Store(true)
	lastRealUserInputNano.Store(0) // never observed, as on a fresh process
	lastSyntheticInput.Store(0)    // force noticeDue

	start := time.Now()
	ensureControlNotice()
	elapsed := time.Since(start)

	if elapsed < 150*time.Millisecond {
		t.Errorf("ensureControlNotice took only %v with no real input ever observed — the lead delay must apply (fail safe, not fail-unsafe)", elapsed)
	}
}

// TestBeginSyntheticInputDoesNotYieldWhenInputNeverObserved covers the other
// direction of the same incident: a first fix attempt (returning 0 instead of
// a sentinel for "never observed") made beginSyntheticInput's active-user
// yield loop read "0ms since last input" as "the user is active RIGHT NOW",
// forever — since a never-observed timestamp never advances — so every
// control call yielded for the full userYieldMaxWait and then errored out.
// With no real input ever observed, there is no evidence of current activity,
// so beginSyntheticInput must proceed without yielding or erroring.
func TestBeginSyntheticInputDoesNotYieldWhenInputNeverObserved(t *testing.T) {
	t.Setenv("AGLINK_NOTICE_LEAD_MS", "0")
	withFastNotice(t)

	origInstall := installUserInputWatcher
	origOK := userWatcherOK.Load()
	origLast := lastRealUserInputNano.Load()
	origActiveWindow := userActiveWindowMS
	origMaxWait := userYieldMaxWait
	t.Cleanup(func() {
		installUserInputWatcher = origInstall
		userWatcherOK.Store(origOK)
		lastRealUserInputNano.Store(origLast)
		userActiveWindowMS = origActiveWindow
		userYieldMaxWait = origMaxWait
		lastSyntheticInput.Store(0)
	})

	installUserInputWatcher = func() {}
	userWatcherOK.Store(true)
	lastRealUserInputNano.Store(0) // never observed
	userActiveWindowMS = 50
	userYieldMaxWait = 2 * time.Second
	lastSyntheticInput.Store(0)

	start := time.Now()
	err := beginSyntheticInput()
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected beginSyntheticInput to proceed without yielding, got error: %v", err)
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("beginSyntheticInput took %v — looks like it yielded/blocked on a never-observed timestamp instead of proceeding", elapsed)
	}
}
