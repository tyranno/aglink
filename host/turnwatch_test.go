package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTurnTimeoutCheck(t *testing.T) {
	t0 := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	idle := 10 * time.Minute
	deadline := t0.Add(30 * time.Minute)

	// Fresh activity: wait until the idle window expires.
	if wait, cause := turnTimeoutCheck(t0.Add(time.Minute), t0.Add(time.Minute), idle, deadline); cause != "" || wait != idle {
		t.Errorf("fresh activity: wait=%v cause=%q, want %v/\"\"", wait, cause, idle)
	}
	// Silent past the idle window → idle.
	if _, cause := turnTimeoutCheck(t0.Add(11*time.Minute), t0, idle, deadline); cause != turnTimeoutIdle {
		t.Errorf("silent: cause=%q, want idle", cause)
	}
	// Active, but the cap comes before the idle expiry → wait only until the cap.
	if wait, cause := turnTimeoutCheck(t0.Add(25*time.Minute), t0.Add(25*time.Minute), idle, deadline); cause != "" || wait != 5*time.Minute {
		t.Errorf("near cap: wait=%v cause=%q, want 5m/\"\"", wait, cause)
	}
	// At the cap → cap, even with activity just now.
	if _, cause := turnTimeoutCheck(deadline, deadline, idle, deadline); cause != turnTimeoutCap {
		t.Errorf("at cap: cause=%q, want cap", cause)
	}
}

func TestConfigTurnLimits(t *testing.T) {
	cases := []struct {
		timeout, maxTurn int
		idle, cap        time.Duration
	}{
		{30, 0, 30 * time.Minute, 90 * time.Minute},    // production config: idle 30, cap 3×
		{30, 120, 30 * time.Minute, 120 * time.Minute}, // explicit cap
		{1, 0, 12 * time.Minute, 12 * time.Minute},     // floored idle; cap never below idle
		{0, 0, 12 * time.Minute, 30 * time.Minute},     // unset → default 10, floored; cap 3×10
		{30, 20, 30 * time.Minute, 30 * time.Minute},   // cap below idle → raised to idle
	}
	for _, c := range cases {
		idle, maxTotal := (&Config{TimeoutMinutes: c.timeout, MaxTurnMinutes: c.maxTurn}).turnLimits()
		if idle != c.idle || maxTotal != c.cap {
			t.Errorf("timeout=%d max=%d: got %v/%v, want %v/%v", c.timeout, c.maxTurn, idle, maxTotal, c.idle, c.cap)
		}
	}
}

// watchClient is a ClaudeClient whose Run is a test-supplied function, so a
// turn can stream "activity" on its own schedule through the real Bot.runTurn
// watchdog.
type watchClient struct {
	run func(ctx context.Context) error

	mu     sync.Mutex
	ctxErr error
	done   chan struct{}
}

func (c *watchClient) Route(context.Context, RouteRequest) (RouteDecision, error) {
	return RouteDecision{}, nil
}

func (c *watchClient) Run(ctx context.Context, _ RunRequest) (RunResult, error) {
	err := c.run(ctx)
	c.mu.Lock()
	c.ctxErr = ctx.Err()
	c.mu.Unlock()
	close(c.done)
	if err != nil {
		return RunResult{}, err
	}
	return RunResult{Text: "done"}, nil
}

// runWatchedTurn dispatches one turn through Bot.runTurn with the given limits
// and returns the client's view of ctx.Err() at the end of Run plus every text
// the bot sent.
func runWatchedTurn(t *testing.T, idle, maxTotal time.Duration, run func(ctx context.Context) error) (error, []string) {
	t.Helper()
	wc := &watchClient{run: run, done: make(chan struct{})}
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	b := &Bot{
		cfgh:           NewConfigHolder(&Config{MaxWorkers: 1, TimeoutMinutes: 1}),
		cancels:        make(map[int]*cancelEntry),
		lanes:          make(map[string]*lane),
		store:          st,
		turnLimitsHook: func() (time.Duration, time.Duration) { return idle, maxTotal },
	}
	b.manager = NewManager(wc, nil, st, NewConfigHolder(&Config{ManagerAlways: true}))
	b.out = NewHub()
	ch := &hookCh{}
	b.out.Register(1, ch)

	tgt := TelegramTarget()
	b.dispatch(queuedMsg{chatID: 1, text: "work", origin: OriginTelegram, target: &tgt})
	select {
	case <-wc.done:
	case <-time.After(10 * time.Second):
		t.Fatal("turn never finished")
	}
	waitFor(t, func() bool { return b.active() == 0 })
	ch.mu.Lock()
	defer ch.mu.Unlock()
	wc.mu.Lock()
	defer wc.mu.Unlock()
	return wc.ctxErr, append([]string(nil), ch.texts...)
}

func sentContaining(texts []string, sub string) bool {
	for _, s := range texts {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// A turn that keeps producing output for well beyond the idle window — several
// times over in total — must not be cancelled: only silence counts.
func TestTurnWatchdog_SteadyActivityIsNotCancelled(t *testing.T) {
	idle := 200 * time.Millisecond
	ctxErr, texts := runWatchedTurn(t, idle, 10*time.Second, func(ctx context.Context) error {
		act := turnActivityFrom(ctx)
		if act == nil {
			return fmt.Errorf("turn ctx carries no activity tracker")
		}
		for end := time.Now().Add(5 * idle); time.Now().Before(end); {
			act.Touch()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(idle / 4):
			}
		}
		return nil
	})
	if ctxErr != nil {
		t.Fatalf("steadily active turn was cancelled: %v (sent %q)", ctxErr, texts)
	}
	if sentContaining(texts, "⏱") {
		t.Errorf("unexpected timeout notice: %q", texts)
	}
}

// A turn that goes silent is stopped after the idle window, and the notice says
// it was idle (not the absolute cap).
func TestTurnWatchdog_SilentTurnIsCancelledAfterIdle(t *testing.T) {
	idle := 200 * time.Millisecond
	var silentFor time.Duration
	ctxErr, texts := runWatchedTurn(t, idle, 10*time.Second, func(ctx context.Context) error {
		turnActivityFrom(ctx).Touch()
		start := time.Now()
		select {
		case <-ctx.Done():
			silentFor = time.Since(start)
			return ctx.Err()
		case <-time.After(5 * time.Second):
			return nil
		}
	})
	if ctxErr == nil {
		t.Fatal("silent turn was not cancelled")
	}
	if silentFor < idle || silentFor > idle+time.Second {
		t.Errorf("cancelled after %v of silence, want ≈%v", silentFor, idle)
	}
	if !sentContaining(texts, "아무 진행") || sentContaining(texts, "최대 작업 시간") {
		t.Errorf("timeout notice should name the idle stop, got %q", texts)
	}
}

// The absolute cap still stops a turn that never goes quiet.
func TestTurnWatchdog_AbsoluteCapStillApplies(t *testing.T) {
	idle, maxTotal := 200*time.Millisecond, 600*time.Millisecond
	start := time.Now()
	ctxErr, texts := runWatchedTurn(t, idle, maxTotal, func(ctx context.Context) error {
		act := turnActivityFrom(ctx)
		for {
			act.Touch()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(idle / 4):
			}
		}
	})
	if ctxErr == nil {
		t.Fatal("endlessly active turn was not stopped by the cap")
	}
	if el := time.Since(start); el < maxTotal || el > maxTotal+2*time.Second {
		t.Errorf("stopped after %v, want ≈%v", el, maxTotal)
	}
	if !sentContaining(texts, "최대 작업 시간") {
		t.Errorf("timeout notice should name the absolute cap, got %q", texts)
	}
}

// --- real claude execStream: every stdout line counts as activity ---

// TestHelperFakeClaudeStream is not a real test: it is the fake CLI process the
// execStream tests below spawn (os.Args[0] -test.run=^TestHelperFakeClaudeStream$).
func TestHelperFakeClaudeStream(t *testing.T) {
	mode := os.Getenv("AGLINK_FAKE_CLAUDE_STREAM")
	if mode == "" {
		return
	}
	switch mode {
	case "steady":
		for i := 0; i < 12; i++ {
			fmt.Printf("{\"type\":\"assistant\",\"n\":%d}\n", i)
			time.Sleep(400 * time.Millisecond)
		}
		fmt.Println(`{"type":"result","subtype":"success","result":"ok","session_id":"s"}`)
	case "silent":
		fmt.Println(`{"type":"system","subtype":"init"}`)
		time.Sleep(20 * time.Second)
	}
	os.Exit(0)
}

// watchCtx is a minimal stand-in for Bot.runTurn's watchdog around a ctx.
func watchCtx(idle time.Duration) (context.Context, func() string) {
	base, cancel := context.WithCancel(context.Background())
	start := time.Now()
	act := newTurnActivity(start, idle, start.Add(time.Hour))
	var mu sync.Mutex
	cause := ""
	go func() {
		for base.Err() == nil {
			if _, c := turnTimeoutCheck(time.Now(), act.Last(), idle, act.Deadline()); c != "" {
				mu.Lock()
				cause = c
				mu.Unlock()
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	return withTurnActivity(base, act), func() string {
		cancel()
		mu.Lock()
		defer mu.Unlock()
		return cause
	}
}

func runFakeStream(t *testing.T, mode string, idle time.Duration) (cause string, stdout string, err error) {
	t.Helper()
	t.Setenv("AGLINK_FAKE_CLAUDE_STREAM", mode)
	r := NewClaudeRunner(os.Args[0], NewConfigHolder(&Config{}))
	ctx, stop := watchCtx(idle)
	stdout, _, err = r.execStream(ctx, t.TempDir(), []string{"-test.run=^TestHelperFakeClaudeStream$"}, "", "", nil)
	return stop(), stdout, err
}

// 12 lines 400ms apart (~4.8s total) under a 2s idle window (generous enough to
// absorb the helper process start-up): the stream runs more than twice the
// window but is never quiet that long, so it completes.
func TestExecStream_SteadyLinesKeepTurnAlive(t *testing.T) {
	cause, stdout, err := runFakeStream(t, "steady", 2*time.Second)
	if cause != "" || err != nil {
		t.Fatalf("steady stream was stopped: cause=%q err=%v", cause, err)
	}
	if !strings.Contains(stdout, `"type":"result"`) {
		t.Errorf("stream incomplete: %q", stdout)
	}
}

// One line then silence: the idle window expires and the process is killed.
func TestExecStream_SilentStreamIsStopped(t *testing.T) {
	start := time.Now()
	cause, _, _ := runFakeStream(t, "silent", 2*time.Second)
	if cause != turnTimeoutIdle {
		t.Fatalf("cause = %q, want idle", cause)
	}
	if el := time.Since(start); el > 15*time.Second {
		t.Errorf("silent stream took %v to stop", el)
	}
}

func TestTurnTimeoutMessage(t *testing.T) {
	if m := turnTimeoutMessage(turnTimeoutIdle, 30*time.Minute, 90*time.Minute); !strings.Contains(m, "30분 동안 아무 진행") {
		t.Errorf("idle message = %q", m)
	}
	if m := turnTimeoutMessage(turnTimeoutCap, 30*time.Minute, 90*time.Minute); !strings.Contains(m, "최대 작업 시간(90분)") {
		t.Errorf("cap message = %q", m)
	}
}

func TestHeartbeatMessage(t *testing.T) {
	start := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	act := newTurnActivity(start, 30*time.Minute, start.Add(90*time.Minute))

	act.last.Store(start.Add(10 * time.Minute).UnixNano())
	if m := heartbeatMessage("작업 진행 중", start.Add(10*time.Minute), start, act); m != "⏳ 작업 진행 중... (10분 0초 경과)" {
		t.Errorf("routine = %q", m)
	}
	if m := heartbeatMessage("작업 진행 중", start.Add(15*time.Minute), start, act); !strings.Contains(m, "마지막 활동 5분 전") {
		t.Errorf("quiet = %q", m)
	}
	if m := heartbeatMessage("작업 진행 중", start.Add(39*time.Minute), start, act); !strings.Contains(m, "활동이 없으면 중단") {
		t.Errorf("idle warning = %q", m)
	}
	act.last.Store(start.Add(89 * time.Minute).UnixNano())
	if m := heartbeatMessage("작업 진행 중", start.Add(89*time.Minute), start, act); !strings.Contains(m, "최대 작업 시간(90분)") {
		t.Errorf("cap warning = %q", m)
	}
	if m := heartbeatMessage("x", start.Add(time.Minute), start, nil); m != "⏳ x... (1분 0초 경과)" {
		t.Errorf("no tracker = %q", m)
	}
}
