//go:build windows

package main

import "testing"

func TestRunSequence_InvalidJSON(t *testing.T) {
	_, err := runSequence("not json")
	if err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
}

func TestRunSequence_EmptySteps(t *testing.T) {
	_, err := runSequence("[]")
	if err == nil {
		t.Fatal("expected an error for an empty steps array")
	}
}

// TestRunSequence_UnknownActionStopsImmediately pins that an unknown action
// name fails without touching the desktop (no real click/type/key dispatch
// for a name the switch doesn't recognize), and that runSequence reports it
// as step 0 rather than silently skipping it.
func TestRunSequence_UnknownActionStopsImmediately(t *testing.T) {
	results, err := runSequence(`[{"action":"levitate","x":1,"y":2}]`)
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
	_, err := runSequence(`[{"action":"scroll"}]`)
	if err == nil {
		t.Fatal("expected an error for scroll with dx=dy=0")
	}
}
