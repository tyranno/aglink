package main

import (
	"testing"
	"time"
)

// doneCh records Done calls per Target so a test can assert the working
// indicator is always cleared, and cleared for the right conversation.
type doneCh struct {
	dones []Target
}

func (d *doneCh) Send(Target, int64, string) error              { return nil }
func (d *doneCh) SendPhoto(Target, int64, []byte, string) error { return nil }
func (d *doneCh) Typing(Target, int64)                          {}
func (d *doneCh) Done(tgt Target, _ int64)                      { d.dones = append(d.dones, tgt) }
func (d *doneCh) Progress(Target, int64, string)                {}
func (d *doneCh) EchoUser(Target, int64, string, string)        {}

// The web client shows its working indicator the moment it sends anything, and
// only a Done clears it. A "!" command runs no worker, so nothing used to clear
// it — the indicator spun forever behind an answer that had already arrived.
func TestHandleCommand_AlwaysSignalsDone(t *testing.T) {
	b := &Bot{}
	b.out = NewHub()
	web := &doneCh{}
	b.out.Register(7, web)

	tgt := Target{Kind: TargetWeb, ID: "c1"}
	b.handleCommand(7, "!help", OriginWeb, tgt)

	if len(web.dones) != 1 {
		t.Fatalf("a command must signal Done exactly once, got %d", len(web.dones))
	}
	if !web.dones[0].IsWeb() || web.dones[0].ID != "c1" {
		t.Errorf("Done target = %+v, want web/c1", web.dones[0])
	}
}

// An unknown command still ends the turn: the indicator must not survive it.
func TestHandleCommand_UnknownCommandStillSignalsDone(t *testing.T) {
	b := &Bot{}
	b.out = NewHub()
	web := &doneCh{}
	b.out.Register(7, web)

	b.handleCommand(7, "!definitely-not-a-command", OriginWeb, Target{Kind: TargetWeb, ID: "c1"})

	if len(web.dones) != 1 {
		t.Errorf("unknown command must still signal Done, got %d", len(web.dones))
	}
}

// buildActiveWorkersResponse is what a client polls to correct an indicator that
// a dropped Done frame left spinning. Conversation IDs must survive verbatim, so
// the client can key on them.
func TestBuildActiveWorkersResponse(t *testing.T) {
	started := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	resp := buildActiveWorkersResponse([]WorkerStatus{
		{Project: "", ConversationID: "telegram", Title: "텔레그램 대화", Status: "running", StartTime: started},
		{Project: "alpha", ConversationID: "4", Title: "topic", Status: "running"},
	})

	if len(resp.Workers) != 2 {
		t.Fatalf("workers = %d, want 2", len(resp.Workers))
	}
	if resp.Workers[0].ConversationID != "telegram" {
		t.Errorf("telegram stream conv id = %q, want %q", resp.Workers[0].ConversationID, "telegram")
	}
	if resp.Workers[0].StartedAt != "2026-07-10T12:00:00Z" {
		t.Errorf("startedAt = %q", resp.Workers[0].StartedAt)
	}
	if resp.Workers[1].Project != "alpha" || resp.Workers[1].ConversationID != "4" {
		t.Errorf("second worker = %+v", resp.Workers[1])
	}
	// A zero StartTime must not emit a bogus timestamp.
	if resp.Workers[1].StartedAt != "" {
		t.Errorf("zero StartTime should omit startedAt, got %q", resp.Workers[1].StartedAt)
	}
}

// An empty active list must marshal as an empty array, not null — the client
// iterates it directly.
func TestBuildActiveWorkersResponse_EmptyIsNotNull(t *testing.T) {
	resp := buildActiveWorkersResponse(nil)
	if resp.Workers == nil {
		t.Error("Workers must be an empty slice, not nil")
	}
	if len(resp.Workers) != 0 {
		t.Errorf("workers = %d, want 0", len(resp.Workers))
	}
}
