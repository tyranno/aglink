package main

import (
	"testing"
	"time"
)

// recordCompletedTurn must fill in the response on the pending prompt-only turn
// in place (no duplicate user message on reload).
func TestRecordCompletedTurn_UpdatesInPlace(t *testing.T) {
	now := time.Now()
	history := []ConversationTurn{
		{Prompt: "q1", Response: "a1"},
		{Prompt: "q2", Response: ""}, // pending (persisted at run start)
	}
	got := recordCompletedTurn(history, 1, "q2", "a2", now)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (updated in place, not appended)", len(got))
	}
	if got[1].Prompt != "q2" || got[1].Response != "a2" {
		t.Errorf("pending turn = %+v, want {q2, a2}", got[1])
	}
}

// If the pending slot no longer holds the matching pending turn (dropped by the
// cap, shifted by a concurrent turn, or already completed), append instead of
// clobbering the wrong turn.
func TestRecordCompletedTurn_AppendsWhenSlotStale(t *testing.T) {
	now := time.Now()
	base := []ConversationTurn{{Prompt: "q1", Response: "a1"}}

	// pendingIdx out of range → append.
	got := recordCompletedTurn(base, 5, "q2", "a2", now)
	if len(got) != 2 || got[1].Prompt != "q2" || got[1].Response != "a2" {
		t.Errorf("out-of-range pendingIdx should append, got %+v", got)
	}

	// pendingIdx points at an already-completed turn (Response != "") → append,
	// must not overwrite it.
	got = recordCompletedTurn([]ConversationTurn{{Prompt: "q1", Response: "a1"}}, 0, "q2", "a2", now)
	if len(got) != 2 || got[0].Response != "a1" {
		t.Errorf("must not clobber a completed turn, got %+v", got)
	}

	// pendingIdx points at a pending turn with a DIFFERENT prompt → append.
	got = recordCompletedTurn([]ConversationTurn{{Prompt: "other", Response: ""}}, 0, "q2", "a2", now)
	if len(got) != 2 {
		t.Errorf("mismatched pending prompt should append, got %+v", got)
	}
}

// The reload-survival property: a pending turn (prompt persisted, response not
// yet) is visible in /api/history as a lone user message, so a web reload during
// the in-flight window no longer wipes the just-sent message.
func TestBuildHistoryResponse_PendingPromptShowsUser(t *testing.T) {
	st := histStore(t)
	tc := st.TelegramConversation()
	tc.History = []ConversationTurn{
		{Prompt: "done", Response: "ok"},
		{Prompt: "in-flight", Response: ""}, // pending
	}
	_ = st.UpdateTelegramConversation(tc)

	resp := buildHistoryResponse(st, Target{Kind: "telegram"})
	// done(user)+done(assistant)+in-flight(user) = 3; the pending turn must NOT
	// add an empty assistant bubble.
	if len(resp.Turns) != 3 {
		t.Fatalf("turns = %d, want 3 (%+v)", len(resp.Turns), resp.Turns)
	}
	last := resp.Turns[len(resp.Turns)-1]
	if last.Role != "user" || last.Text != "in-flight" {
		t.Errorf("last turn = %+v, want the pending user message", last)
	}
}
