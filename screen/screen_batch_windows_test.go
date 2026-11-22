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

// The new UIA steps validate their arguments before touching the desktop.
func TestRunSequence_UIAStepArgErrors(t *testing.T) {
	for _, steps := range []string{
		`[{"action":"set_state","name":"Wrap","nth":-1,"checked":true}]`,
		`[{"action":"set_state","name":"Vol","value":"loud"}]`,
		`[{"action":"invoke","name":"OK","control_type":"Gizmo"}]`,
		`[{"action":"wait_for_control","name":"OK","until":"vanish"}]`,
		`[{"action":"wait_for_control","name":"OK","until":"value"}]`,
		`[{"action":"set_value","name":"F","text":"x","match":"regex"}]`,
	} {
		if _, err := runSequence(steps, 0); err == nil {
			t.Errorf("%s: expected an argument error", steps)
		}
	}
}

func TestSeqStepValue(t *testing.T) {
	var st seqStep
	if _, set, err := st.valueString(); set || err != nil {
		t.Fatal("absent value must be unset")
	}
	if n, err := st.valueNumber(); n != nil || err != nil {
		t.Fatal("absent number must be nil")
	}
	st.Value = []byte(`"100%"`)
	if v, set, err := st.valueString(); !set || err != nil || v != "100%" {
		t.Fatalf("string: %q %v %v", v, set, err)
	}
	st.Value = []byte(`42.5`)
	if v, _, _ := st.valueString(); v != "42.5" {
		t.Fatalf("number as string: %q", v)
	}
	if n, err := st.valueNumber(); err != nil || *n != 42.5 {
		t.Fatalf("number: %v %v", n, err)
	}
	st.Value = []byte(`" 7 "`)
	if n, err := st.valueNumber(); err != nil || *n != 7 {
		t.Fatalf("numeric string: %v %v", n, err)
	}
	st.Value = []byte(`true`)
	if _, _, err := st.valueString(); err == nil {
		t.Fatal("bool value must fail")
	}
}

func TestOptBoolFloat(t *testing.T) {
	args := map[string]any{"a": true, "b": "false", "c": 3.5, "d": "2", "e": "nope", "f": nil}
	if v, ok, err := optBool(args, "a"); !v || !ok || err != nil {
		t.Error("bool true")
	}
	if v, ok, err := optBool(args, "b"); v || !ok || err != nil {
		t.Error("string false")
	}
	if _, ok, _ := optBool(args, "missing"); ok {
		t.Error("missing bool must be absent")
	}
	if _, ok, _ := optBool(args, "f"); ok {
		t.Error("null bool must be absent")
	}
	if _, _, err := optBool(args, "e"); err == nil {
		t.Error("bad bool must fail")
	}
	if v, ok, err := optFloat(args, "c"); v != 3.5 || !ok || err != nil {
		t.Error("float")
	}
	if v, ok, err := optFloat(args, "d"); v != 2 || !ok || err != nil {
		t.Error("numeric string")
	}
	if _, _, err := optFloat(args, "e"); err == nil {
		t.Error("bad float must fail")
	}
}
