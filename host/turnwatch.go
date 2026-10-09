package main

import (
	"context"
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

// Turn timeouts are activity-based, not wall-clock.
//
// A worker turn used to be killed a fixed TimeoutMinutes after it started, even
// while the CLI was still productively working — e.g. opus writing one 15–26k
// token file in a single Write tool call (150–270s with no stream line in
// between) late in a long turn. The kill threw the whole turn's output away.
//
// Now a turn is cancelled only when
//   - it shows NO stream activity (any stdout line from the CLI: system,
//     assistant, tool_use, tool_result, partial deltas …) for the idle window
//     (runtime.timeout_minutes, floored at minTurnIdleMinutes), or
//   - it reaches the absolute cap (runtime.max_turn_minutes; 0 = 3× the idle
//     window), which still bounds a turn that streams forever.
//
// The tracker travels in the turn's context (withTurnActivity) so every runner
// that reads its CLI's output can report progress (touchActivity / Touch)
// without changing the ClaudeClient interface, and every Run issued with that
// ctx — the first attempt, the session-recovery retries, and the
// context-overflow retry in Manager.runWorker — is covered by the same watchdog.

// minTurnIdleMinutes floors the idle window. timeout_minutes has no useful lower
// bound in config (legacy TIMEOUT_MINUTES must be >0, the YAML/settings path
// accepts anything), but a short idle window would kill healthy turns: one
// silent tool call can legitimately last long — claude's Bash tool runs up to
// 10 minutes (600000ms max) before it emits its tool_result, and a large Write
// without --include-partial-messages produces no line for minutes. 12 minutes
// clears both with margin.
const minTurnIdleMinutes = 12

// turnCapMultiplier derives the absolute cap from the idle window when
// runtime.max_turn_minutes is unset (0).
const turnCapMultiplier = 3

// turnLimits returns the idle window and the absolute cap for a worker turn.
// The cap is never below the idle window.
func (c *Config) turnLimits() (idle, maxTotal time.Duration) {
	idleMin := c.TimeoutMinutes
	if idleMin <= 0 {
		idleMin = 10 // config default
	}
	if idleMin < minTurnIdleMinutes {
		idleMin = minTurnIdleMinutes
	}
	capMin := c.MaxTurnMinutes
	if capMin <= 0 {
		base := c.TimeoutMinutes
		if base <= 0 {
			base = 10
		}
		capMin = turnCapMultiplier * base
	}
	if capMin < idleMin {
		capMin = idleMin
	}
	return time.Duration(idleMin) * time.Minute, time.Duration(capMin) * time.Minute
}

// turnActivity is the per-turn progress tracker shared between the runner
// (which touches it per output line) and the watchdog/heartbeat (which read
// it). Lock-free so touching it per NDJSON line costs nothing.
type turnActivity struct {
	start    time.Time
	idle     time.Duration
	last     atomic.Int64 // unix nanos of the latest activity
	deadline atomic.Int64 // unix nanos of the absolute cap (moves with !timeout)
}

func newTurnActivity(start time.Time, idle time.Duration, deadline time.Time) *turnActivity {
	a := &turnActivity{start: start, idle: idle}
	a.last.Store(start.UnixNano())
	a.deadline.Store(deadline.UnixNano())
	return a
}

// Touch records stream activity now. Nil-safe.
func (a *turnActivity) Touch() {
	if a != nil {
		a.last.Store(time.Now().UnixNano())
	}
}

func (a *turnActivity) Last() time.Time     { return time.Unix(0, a.last.Load()) }
func (a *turnActivity) Deadline() time.Time { return time.Unix(0, a.deadline.Load()) }

type turnActivityKey struct{}

func withTurnActivity(ctx context.Context, a *turnActivity) context.Context {
	return context.WithValue(ctx, turnActivityKey{}, a)
}

// turnActivityFrom returns the tracker carried by ctx, or nil (a ctx not
// created by Bot.runTurn — tests, routing, summaries). Callers that touch per
// line should look it up once, not per line.
func turnActivityFrom(ctx context.Context) *turnActivity {
	if ctx == nil {
		return nil
	}
	a, _ := ctx.Value(turnActivityKey{}).(*turnActivity)
	return a
}

// activityWriter forwards to w and touches a on every write. Used by runners
// whose output isn't consumed line by line (opencode buffers its stdout).
type activityWriter struct {
	w io.Writer
	a *turnActivity
}

func (aw activityWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		aw.a.Touch()
	}
	return aw.w.Write(p)
}

// formatLimit renders a limit as "N분" ("N초" under a minute — only tests use
// such short limits).
func formatLimit(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d초", int(d.Round(time.Second)/time.Second))
	}
	return fmt.Sprintf("%d분", int(d.Round(time.Minute)/time.Minute))
}

// turnTimeoutMessage is the user-facing notice for a watchdog stop: it says
// whether the turn went silent (idle) or hit the absolute cap while working.
func turnTimeoutMessage(cause string, idle, maxTotal time.Duration) string {
	const resume = " 다시 메시지를 보내시면 이어서 진행됩니다(같은 대화 세션이라 지금까지 맥락은 유지됩니다)."
	if cause == turnTimeoutCap {
		return fmt.Sprintf("⏱ 최대 작업 시간(%s)에 도달해 작업을 중단했습니다 — 진행은 계속되고 있었지만 상한을 넘겼습니다.%s 더 오래 걸리는 작업이면 실행 중에 !timeout +<분> 으로 상한을 늘릴 수 있습니다.",
			formatLimit(maxTotal), resume)
	}
	return fmt.Sprintf("⏱ %s 동안 아무 진행(출력·도구 호출)이 없어 멈춘 것으로 보고 작업을 중단했습니다.%s",
		formatLimit(idle), resume)
}

// Timeout causes reported by turnTimeoutCheck.
const (
	turnTimeoutIdle = "idle"
	turnTimeoutCap  = "cap"
)

// turnTimeoutCheck decides, at now, whether a turn whose latest activity was
// at last must be stopped. cause is "" while the turn may continue, in which
// case wait is how long until the next check is due (the earlier of the idle
// expiry and the cap). Pure, for tests.
func turnTimeoutCheck(now, last time.Time, idle time.Duration, deadline time.Time) (wait time.Duration, cause string) {
	if !now.Before(deadline) {
		return 0, turnTimeoutCap
	}
	idleAt := last.Add(idle)
	if !now.Before(idleAt) {
		return 0, turnTimeoutIdle
	}
	next := idleAt
	if deadline.Before(next) {
		next = deadline
	}
	return next.Sub(now), ""
}
