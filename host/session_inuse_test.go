package main

import "testing"

// A restart landing between a conversation's first turn (which creates the CLI
// session) and store.json recording Started=true leaves aglink re-creating a
// session id the CLI already holds. The CLI rejects it with "Session ID <id> is
// already in use." isSessionAlreadyInUse must recognise exactly that so the worker
// path can recover by resuming the existing session instead of failing the turn.

func TestIsSessionAlreadyInUseDetectsCLIMessage(t *testing.T) {
	// The exact shape the claude CLI emits (seen live).
	msg := "worker 실행 실패: exit status 1 (Error: Session ID 1c638281-7c9e-41ca-8e6a-910536b16445 is already in use.)"
	if !isSessionAlreadyInUse(msg) {
		t.Fatalf("should detect the CLI's already-in-use message: %q", msg)
	}
}

func TestIsSessionAlreadyInUseIgnoresUnrelated(t *testing.T) {
	for _, msg := range []string{
		"",
		"exit status 1: some other failure",
		"prompt is too long",
		"resource already in use",       // "already in use" but not about a session id
		"No conversation found with session ID: abc", // the *opposite* condition
	} {
		if isSessionAlreadyInUse(msg) {
			t.Errorf("must not treat as session-in-use: %q", msg)
		}
	}
}

// The two session-recovery detectors must stay mutually exclusive — one triggers a
// retry-as-fresh, the other a retry-as-resume; overlap would let a turn ping-pong
// between both recoveries.
func TestSessionDetectorsAreDisjoint(t *testing.T) {
	inUse := "Session ID abc is already in use."
	notFound := "No conversation found with session ID: abc"

	if isSessionNotFound(inUse) {
		t.Error("already-in-use must not read as session-not-found")
	}
	if isSessionAlreadyInUse(notFound) {
		t.Error("session-not-found must not read as already-in-use")
	}
}
