package main

import (
	"encoding/json"
	"testing"
	"time"
)

func drainControlOut(ch chan controlOut) []controlOut {
	var out []controlOut
	for {
		select {
		case o := <-ch:
			out = append(out, o)
		default:
			return out
		}
	}
}

// remoteChatChannel serializes Hub output to control frames; web-origin echo is a
// no-op (mirrors webChannel).
func TestRemoteChatChannel_Frames(t *testing.T) {
	r := &remoteChatChannel{send: make(chan controlOut, 16), cancel: func() {}}
	tg := TelegramTarget()
	_ = r.Send(tg, 7, "hi")
	r.Typing(tg, 7)
	r.Done(tg, 7)
	r.EchoUser(tg, 7, "from telegram", OriginTelegram)
	r.EchoUser(tg, 7, "from web", OriginWeb) // no-op

	outs := drainControlOut(r.send)
	if len(outs) != 4 {
		t.Fatalf("expected 4 frames (web-origin echo is a no-op), got %d: %+v", len(outs), outs)
	}
	if outs[0].Kind != "frame" || outs[0].Frame == nil || outs[0].Frame.Type != "text" || outs[0].Frame.Text != "hi" {
		t.Errorf("send frame = %+v", outs[0])
	}
	if outs[1].Frame.Type != "typing" || outs[2].Frame.Type != "done" {
		t.Errorf("typing/done wrong: %+v %+v", outs[1].Frame, outs[2].Frame)
	}
	if outs[3].Frame.Type != "user" || outs[3].Frame.Text != "from telegram" {
		t.Errorf("echo frame = %+v", outs[3].Frame)
	}
}

// Every frame carries the Target it belongs to, so the browser can file it under
// the right conversation instead of appending it to whatever is on screen.
func TestRemoteChatChannel_FramesCarryTarget(t *testing.T) {
	r := &remoteChatChannel{send: make(chan controlOut, 16), cancel: func() {}}
	wt := Target{Kind: TargetWeb, ID: "conv-9"}
	_ = r.Send(wt, 7, "hi")
	_ = r.SendPhoto(wt, 7, []byte("png"), "cap")
	r.Typing(wt, 7)
	r.Done(wt, 7)

	outs := drainControlOut(r.send)
	if len(outs) != 4 {
		t.Fatalf("expected 4 frames, got %d", len(outs))
	}
	for i, o := range outs {
		if o.Frame.Target == nil {
			t.Fatalf("frame %d (%s) has no target", i, o.Frame.Type)
		}
		if !o.Frame.Target.IsWeb() || o.Frame.Target.ID != "conv-9" {
			t.Errorf("frame %d target = %+v, want web/conv-9", i, *o.Frame.Target)
		}
	}
}

// send_text with no explicit target routes through dispatchTargeted (default:
// telegram stream) rather than the LLM-routed dispatchText/dispatchHook path
// (Task 5), so routing is observed via the telegram conversation's history.
func TestChatControl_SendText_Dispatches(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "ok"}}
	m, st, _ := webTgtManager(t, fc)
	b := &Bot{manager: m, store: st, cfgh: NewConfigHolder(&Config{MaxWorkers: 3, TimeoutMinutes: 1}), cancels: make(map[int]*cancelEntry)}
	b.out = NewHub()
	s := &chatControlServer{ownerChatID: 7, bot: b, hub: b.out}
	ch := &remoteChatChannel{send: make(chan controlOut, 4), cancel: func() {}}

	s.handleInbound(ch, controlIn{Type: "send_text", Text: "hello", Origin: OriginWeb})

	deadline := time.Now().Add(2 * time.Second) // dispatch runs in a goroutine
	for len(st.TelegramConversation().History) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(st.TelegramConversation().History) != 1 {
		t.Errorf("send_text should route to telegram target via dispatchTargeted, history len=%d", len(st.TelegramConversation().History))
	}
}

// list_conversations returns a reply carrying the conversations payload.
func TestChatControl_ListConversations_Replies(t *testing.T) {
	st := originStore(t) // registers one project "p"
	b := &Bot{store: st}
	s := &chatControlServer{ownerChatID: 7, bot: b}
	ch := &remoteChatChannel{send: make(chan controlOut, 4), cancel: func() {}}

	s.handleInbound(ch, controlIn{Type: "list_conversations", ReqID: "r1"})

	outs := drainControlOut(ch.send)
	if len(outs) != 1 || outs[0].Kind != "reply" || outs[0].ReqID != "r1" {
		t.Fatalf("expected one reply for r1, got %+v", outs)
	}
	var resp webConversationsResponse
	if err := json.Unmarshal(outs[0].Data, &resp); err != nil {
		t.Fatalf("reply data is not a conversations response: %v", err)
	}
}
