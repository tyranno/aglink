package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func turns(n int) []ConversationTurn {
	out := make([]ConversationTurn, n)
	for i := range out {
		out[i] = ConversationTurn{Prompt: "p", Response: "r"}
	}
	return out
}

// A resuming turn inlines only a short reminder; a non-resuming turn inlines a
// much larger slice, because then the store is the only context there is.
func TestHistoryForContext_ResumeVsRecovery(t *testing.T) {
	h := turns(50)

	resume := historyForContext(h, true)
	if len(resume) != maxHistoryInPromptResume {
		t.Errorf("resume history = %d turns, want %d", len(resume), maxHistoryInPromptResume)
	}

	recovery := historyForContext(h, false)
	if len(recovery) != maxHistoryOnRecovery {
		t.Errorf("recovery history = %d turns, want the %d-turn cap", len(recovery), maxHistoryOnRecovery)
	}
	// Both are trailing slices (most recent kept).
	if &recovery[len(recovery)-1] != &h[len(h)-1] {
		t.Error("recovery slice must end at the most recent turn")
	}
}

// codexContextTooLarge gates codex session auto-reset: at or above the
// threshold a resumed thread has ballooned enough to reset, below it we keep
// resuming.
func TestCodexContextTooLarge(t *testing.T) {
	if codexContextTooLarge(0) {
		t.Error("0 tokens (unknown) must not trigger a reset")
	}
	if codexContextTooLarge(codexContextResetTokens - 1) {
		t.Error("just below threshold must not trigger a reset")
	}
	if !codexContextTooLarge(codexContextResetTokens) {
		t.Error("at threshold must trigger a reset")
	}
	if !codexContextTooLarge(9_800_000) {
		t.Error("a ballooned rollout must trigger a reset")
	}
}

func TestTailTurns_CharBudget(t *testing.T) {
	// 100 turns of 1000 chars each; a 24k budget admits ~24 of them.
	h := make([]ConversationTurn, 100)
	for i := range h {
		h[i] = ConversationTurn{Prompt: strings.Repeat("x", 500), Response: strings.Repeat("y", 500)}
	}
	got := tailTurns(h, 1000, 24000)
	total := 0
	for _, tt := range got {
		total += len(tt.Prompt) + len(tt.Response)
	}
	if total > 24000+1000 { // allow the "always keep the last" overshoot of one turn
		t.Errorf("char budget exceeded: %d chars over 24000", total)
	}
	if len(got) == 0 {
		t.Error("must keep at least the most recent turn")
	}
	// The kept slice must be the tail.
	if got[len(got)-1].Prompt != h[len(h)-1].Prompt {
		t.Error("kept slice must end at the most recent turn")
	}
}

func TestTailTurns_KeepsLastEvenIfOverBudget(t *testing.T) {
	h := []ConversationTurn{{Prompt: strings.Repeat("x", 100000)}}
	got := tailTurns(h, 40, 24000)
	if len(got) != 1 {
		t.Errorf("a single over-budget turn must still be kept, got %d turns", len(got))
	}
}

func TestTailTurns_Empty(t *testing.T) {
	if got := tailTurns(nil, 40, 24000); got != nil {
		t.Errorf("nil history → nil, got %v", got)
	}
	if got := tailTurns(turns(5), 0, 0); got != nil {
		t.Errorf("maxTurns 0 → nil, got %v", got)
	}
}

// captureRecoverClient fails the resuming attempt (session lost) and captures
// the prompt of the fresh retry, so a test can assert what history it carried.
type captureRecoverClient struct{ recoveryPrompt string }

func (c *captureRecoverClient) Route(_ context.Context, _ RouteRequest) (RouteDecision, error) {
	return RouteDecision{}, nil
}
func (c *captureRecoverClient) Run(_ context.Context, req RunRequest) (RunResult, error) {
	if req.Resume {
		return RunResult{}, errors.New("No conversation found with session ID: dead")
	}
	c.recoveryPrompt = req.Prompt
	return RunResult{Text: "recovered ok", SessionID: "fresh"}, nil
}

// The point of the fix: when the CLI session is lost, the recovery retry must
// carry the stored history — not just the 3-turn reminder the resuming attempt
// used — so the conversation doesn't forget everything older than three turns.
func TestRunWorker_RecoveryRebuildsPromptWithFullerHistory(t *testing.T) {
	fc := &captureRecoverClient{}
	m, st := recoveryFixture(t, fc)

	tc := st.TelegramConversation()
	tc.Started = true
	tc.SessionID = "dead"
	for i := 0; i < 20; i++ {
		tc.History = append(tc.History, ConversationTurn{
			Prompt:   fmt.Sprintf("PROMPT_%d", i),
			Response: fmt.Sprintf("REPLY_%d", i),
		})
	}
	if err := st.UpdateTelegramConversation(tc); err != nil {
		t.Fatalf("seed: %v", err)
	}

	m.HandleWebTarget(context.Background(), 1, "새 질문", TelegramTarget(), &fakeSender{})

	if fc.recoveryPrompt == "" {
		t.Fatal("recovery retry never ran")
	}
	// PROMPT_5 is well beyond the last 3 turns — present now, dropped by the old
	// code (which reused the 3-turn reminder on recovery).
	if !strings.Contains(fc.recoveryPrompt, "PROMPT_5") {
		t.Error("recovery prompt is missing old history (PROMPT_5) — context would be lost")
	}
	if !strings.Contains(fc.recoveryPrompt, "PROMPT_19") {
		t.Error("recovery prompt is missing the most recent history")
	}
}
