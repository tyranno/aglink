package main

import (
	"context"
	"strings"
	"testing"
)

// persistCheckSender records, at the instant the RESPONSE is sent live, whether
// that turn is already durably in the store. That is the ordering the web client
// depends on: a concurrent /api/history read must never see a snapshot missing a
// turn whose live frame has already gone out. Earlier Sends (the "작업 시작"
// estimate, typing, echoes) are ignored — only the reply text matters.
type persistCheckSender struct {
	st                  *fileStore
	wantResp            string
	sendCalled          bool
	persistedBeforeSend bool
}

func (p *persistCheckSender) Send(_ int64, text string) error {
	if !p.sendCalled && strings.Contains(text, p.wantResp) {
		p.sendCalled = true
		for _, turn := range p.st.TelegramConversation().History {
			if turn.Response == p.wantResp {
				p.persistedBeforeSend = true
			}
		}
	}
	return nil
}
func (p *persistCheckSender) Typing(int64) {}
func (p *persistCheckSender) Done(int64)   {}

// runWorker must append the turn to History and save it BEFORE sending it live.
// The reverse order (the pre-c6ad619 code) let a web client's /api/history read
// race the send and fetch a snapshot that omitted the turn it just watched
// stream in, which the client's replaceChildren() then silently erased.
func TestRunWorker_PersistsBeforeSending(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "PERSISTED-REPLY"}}
	m, st := recoveryFixture(t, fc)

	sender := &persistCheckSender{st: st, wantResp: "PERSISTED-REPLY"}
	m.HandleWebTarget(context.Background(), 1, "hello", TelegramTarget(), sender)

	if !sender.sendCalled {
		t.Fatal("the turn was never sent")
	}
	if !sender.persistedBeforeSend {
		t.Error("the turn was sent live before it was persisted — a concurrent " +
			"/api/history read could fetch a snapshot missing it")
	}
	// And it must genuinely be persisted afterward.
	found := false
	for _, turn := range st.TelegramConversation().History {
		if turn.Response == "PERSISTED-REPLY" {
			found = true
		}
	}
	if !found {
		t.Error("the turn is not in the store after the worker ran")
	}
}
