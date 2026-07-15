package main

import (
	"context"
	"testing"
)

// progressSender is a MessageSender that also implements the optional
// Progress(chatID, text) method runWorker type-asserts for, mirroring how
// aglink-desktop's control-API sender will receive live tool-use progress.
type progressSender struct {
	progressed []string
}

func (p *progressSender) Send(int64, string) error { return nil }
func (p *progressSender) Typing(int64)              {}
func (p *progressSender) Done(int64)                {}
func (p *progressSender) Progress(_ int64, text string) {
	p.progressed = append(p.progressed, text)
}

// noProgressSender is a MessageSender WITHOUT Progress — the Telegram-shaped
// case that must not panic and must simply get a nil OnProgress.
type noProgressSender struct{}

func (n *noProgressSender) Send(int64, string) error { return nil }
func (n *noProgressSender) Typing(int64)              {}
func (n *noProgressSender) Done(int64)                {}

// A sender that supports Progress must have it wired into the RunRequest, and
// a progress line the worker emits must actually reach that sender.
func TestRunWorker_WiresProgressToSenderThatSupportsIt(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "REPLY"}}
	m, _ := recoveryFixture(t, fc)

	sender := &progressSender{}
	m.HandleWebTarget(context.Background(), 1, "hello", TelegramTarget(), sender)

	if fc.lastRun.OnProgress == nil {
		t.Fatal("runWorker did not wire OnProgress even though the sender supports Progress")
	}
	fc.lastRun.OnProgress("🔧 Bash: go test ./...")
	if len(sender.progressed) != 1 || sender.progressed[0] != "🔧 Bash: go test ./..." {
		t.Errorf("progress message did not reach the sender: %+v", sender.progressed)
	}
}

// A sender that does not implement Progress (Telegram's shape) must leave
// OnProgress nil rather than panicking on a failed type assertion.
func TestRunWorker_OnProgressNilWhenSenderLacksIt(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "REPLY"}}
	m, _ := recoveryFixture(t, fc)

	m.HandleWebTarget(context.Background(), 1, "hello", TelegramTarget(), &noProgressSender{})

	if fc.lastRun.OnProgress != nil {
		t.Error("OnProgress must be nil when the sender does not support Progress")
	}
}
