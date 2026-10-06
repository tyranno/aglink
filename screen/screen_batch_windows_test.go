//go:build windows

package main

import (
	"testing"
	"time"
)

func TestRunSequence_InvalidJSON(t *testing.T) {
	_, err := runSequence("not json", 0)
	if err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
}

func TestRunSequence_EmptySteps(t *testing.T) {
	_, err := runSequence("[]", 0)
	if err == nil {
		t.Fatal("expected an error for an empty steps array")
	}
}

// TestRunSequence_UnknownActionStopsImmediately pins that an unknown action
// name fails without touching the desktop (no real click/type/key dispatch
// for a name the switch doesn't recognize), and that runSequence reports it
// as step 0 rather than silently skipping it.
func TestRunSequence_UnknownActionStopsImmediately(t *testing.T) {
	results, err := runSequence(`[{"action":"levitate","x":1,"y":2}]`, 0)
	if err == nil {
		t.Fatal("expected an error for an unknown action")
	}
	if len(results) != 1 {
		t.Fatalf("expected exactly 1 result (the failed step), got %d", len(results))
	}
	if results[0].OK {
		t.Error("expected the unknown-action step to be marked failed")
	}
	if results[0].Action != "levitate" {
		t.Errorf("result action = %q, want levitate", results[0].Action)
	}
}

func TestRunSequence_ScrollRejectsZeroDelta(t *testing.T) {
	_, err := runSequence(`[{"action":"scroll"}]`, 0)
	if err == nil {
		t.Fatal("expected an error for scroll with dx=dy=0")
	}
}

func TestRunSequence_WaitStep(t *testing.T) {
	start := time.Now()
	results, err := runSequence(`[{"action":"wait","ms":120}]`, 0)
	if err != nil || len(results) != 1 || !results[0].OK || results[0].Detail != "waited 120ms" {
		t.Fatalf("results=%+v err=%v", results, err)
	}
	if time.Since(start) < 120*time.Millisecond {
		t.Fatal("wait returned early")
	}
	if _, err := runSequence(`[{"action":"wait"}]`, 0); err == nil {
		t.Fatal("wait without ms must fail")
	}
}

func TestRunSequence_WaitMsAndDelayPause(t *testing.T) {
	start := time.Now()
	// Two 1ms waits, a 100ms wait_ms after the first, and a 100ms delay between them.
	if _, err := runSequence(`[{"action":"wait","ms":1,"wait_ms":100},{"action":"wait","ms":1}]`, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el < 200*time.Millisecond {
		t.Fatalf("pauses not applied: %s", el)
	}
}

func TestSeqDelayFromEnv(t *testing.T) {
	t.Setenv("AGLINK_SCREEN_STEP_DELAY_MS", "250")
	if d := seqDelayFromEnv(); d != 250*time.Millisecond {
		t.Fatalf("got %s", d)
	}
	t.Setenv("AGLINK_SCREEN_STEP_DELAY_MS", "999999")
	if d := seqDelayFromEnv(); d != maxSeqWait {
		t.Fatalf("not capped: %s", d)
	}
	t.Setenv("AGLINK_SCREEN_STEP_DELAY_MS", "")
	if d := seqDelayFromEnv(); d != 0 {
		t.Fatalf("unset: %s", d)
	}
}
