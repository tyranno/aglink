package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func resetHealth(start time.Time, telegram bool) {
	healthState.Lock()
	healthState.startedAt = start
	healthState.telegram = telegram
	healthState.lastPollOK = time.Time{}
	healthState.lastPollErr, healthState.lastPollErrAt = "", time.Time{}
	healthState.lastTurnEnd = time.Time{}
	healthState.Unlock()
}

func TestHealthJudgesTheTelegramPollLoop(t *testing.T) {
	start := time.Now().Add(-time.Hour)
	defer resetHealth(time.Now(), false)

	// Polling recently: healthy.
	resetHealth(start, true)
	notePollOK()
	if r := buildHealth(time.Now().Add(40*time.Second), 1, 0); !r.OK || r.Telegram.LastPollAgoSec < 39 || r.ActiveTurns != 1 {
		t.Fatalf("fresh poll: %+v", r)
	}

	// No successful poll for longer than pollStaleAfter — the hang the
	// 2026-09-07 outage was: unhealthy, with the reason and the last error.
	notePollErr(errDummy("getUpdates: dial tcp: i/o timeout"))
	r := buildHealth(time.Now().Add(pollStaleAfter+time.Minute), 0, 0)
	if r.OK || len(r.Problems) != 1 || !strings.Contains(r.Problems[0], "i/o timeout") {
		t.Fatalf("stale poll: %+v", r)
	}

	// Never polled since start-up, and start-up was long ago: unhealthy too.
	resetHealth(start, true)
	if r := buildHealth(time.Now(), 0, 0); r.OK {
		t.Fatalf("never polled: %+v", r)
	}

	// Web-chat-only host (no Telegram): nothing to judge, healthy.
	resetHealth(start, false)
	if r := buildHealth(time.Now(), 0, 0); !r.OK || r.Telegram.Enabled {
		t.Fatalf("no telegram: %+v", r)
	}
}

func TestHealthNeverCarriesTheBotToken(t *testing.T) {
	defer resetHealth(time.Now(), false)
	resetHealth(time.Now().Add(-time.Hour), true)
	notePollErr(errDummy(`Post "https://api.telegram.org/bot1234567890:AAH0abcdefghijklmnopqrstuvwxyz12345/getUpdates": EOF`))
	r := buildHealth(time.Now(), 0, 0)
	if strings.Contains(r.Telegram.LastError, "AAH0abcdef") {
		t.Fatalf("token in health: %s", r.Telegram.LastError)
	}
}

func TestLoopbackOnlyHealthAddr(t *testing.T) {
	for h, want := range map[string]bool{"127.0.0.1": true, "::1": true, "localhost": true, "0.0.0.0": false, "192.168.0.5": false} {
		if got := isLoopbackHost(h); got != want {
			t.Errorf("isLoopbackHost(%q) = %v", h, got)
		}
	}
}

func TestScheduledTaskSaysWhenItsProjectIsGone(t *testing.T) {
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	_ = st.Load()
	alDir := t.TempDir()
	if err := st.AddProject("alpha", alDir); err != nil {
		t.Fatal(err)
	}
	fc := &fakeClaude{runRes: RunResult{Text: "ok"}}
	m := NewManager(fc, nil, st, NewConfigHolder(&Config{ManagerAlways: true}))
	f := &fakeSender{}
	m.HandleScheduledTask(context.Background(), 1, "nightly", "deleted-project", f)
	if fc.runCalls != 1 || fc.lastRun.WorkDir != alDir {
		t.Fatalf("fallback run: calls=%d dir=%q", fc.runCalls, fc.lastRun.WorkDir)
	}
	if !strings.Contains(strings.Join(f.sent, "\n"), "'deleted-project' 가 없어 'alpha' 에서 실행") {
		t.Fatalf("the user must be told: %q", f.sent)
	}
}

type errDummy string

func (e errDummy) Error() string { return string(e) }
