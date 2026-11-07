package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNarrationTracker_EmitsTextOnlyOnceAToolFollows(t *testing.T) {
	var tr narrationTracker
	lines := []string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"먼저 테스트를 돌려볼게요."}]},"parent_tool_use_id":null}`,
		`{"type":"stream_event","event":{"type":"content_block_delta"}}`,
	}
	for _, l := range lines {
		if got := tr.feed(l); got != "" {
			t.Fatalf("text must be held until a tool call follows, got %q on %s", got, l)
		}
	}
	if got := tr.feed(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test"}}]},"parent_tool_use_id":null}`); got != "먼저 테스트를 돌려볼게요." {
		t.Fatalf("narration on tool_use = %q", got)
	}
	// Tool result, then the final answer: never narration.
	_ = tr.feed(`{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}`)
	if got := tr.feed(`{"type":"assistant","message":{"content":[{"type":"text","text":"테스트 모두 통과했어요."}]},"parent_tool_use_id":null}`); got != "" {
		t.Fatalf("final text leaked early: %q", got)
	}
	if got := tr.feed(`{"type":"result","subtype":"success","result":"테스트 모두 통과했어요."}`); got != "" {
		t.Fatalf("result must not emit narration, got %q", got)
	}
	if len(tr.pending) != 0 {
		t.Fatalf("result must discard the held final text, still %v", tr.pending)
	}
}

func TestNarrationTracker_IgnoresSubAgentsAndTextInSameMessage(t *testing.T) {
	var tr narrationTracker
	// A sub-agent's text and tool calls are not the main thread speaking.
	if got := tr.feed(`{"type":"assistant","message":{"content":[{"type":"text","text":"sub"},{"type":"tool_use","name":"Read"}]},"parent_tool_use_id":"toolu_1"}`); got != "" {
		t.Fatalf("sub-agent narration relayed: %q", got)
	}
	// Text and tool_use in one message: the text is narration.
	if got := tr.feed(`{"type":"assistant","message":{"content":[{"type":"text","text":"파일을 볼게요"},{"type":"tool_use","name":"Read"}]}}`); got != "파일을 볼게요" {
		t.Fatalf("same-message narration = %q", got)
	}
	// A tool call with no text before it emits nothing.
	if got := tr.feed(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read"}]}}`); got != "" {
		t.Fatalf("empty narration = %q", got)
	}
}

func TestNarrationGate_MergesBurstAndCapsLength(t *testing.T) {
	var mu sync.Mutex
	var got []string
	g := newNarrationGate(func(s string) { mu.Lock(); got = append(got, s); mu.Unlock() })
	g.window = time.Hour // nothing after the first flushes until Close

	g.Add("하나")
	g.Add("둘")
	g.Add(strings.Repeat("가", 500))
	if g.LastSent().IsZero() {
		t.Fatal("first narration should go out at once")
	}
	g.Close()
	g.Add("닫힌 뒤") // dropped

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0] != "하나" {
		t.Fatalf("got %q, want first sent alone then the rest merged", got)
	}
	if !strings.HasPrefix(got[1], "둘\n가") || len([]rune(got[1])) != narrationMaxRunes {
		t.Fatalf("merged narration not joined/capped: %d runes %q…", len([]rune(got[1])), truncate(got[1], 10))
	}
}

func TestNarrationGate_TimerFlushesAfterWindow(t *testing.T) {
	ch := make(chan string, 4)
	g := newNarrationGate(func(s string) { ch <- s })
	g.window = 30 * time.Millisecond
	g.Add("a")
	g.Add("b")
	g.Add("c")
	if s := <-ch; s != "a" {
		t.Fatalf("first = %q", s)
	}
	select {
	case s := <-ch:
		if s != "b\nc" {
			t.Fatalf("merged = %q", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("merged narration never flushed")
	}
	g.Close()
}

func TestNarratedRecently(t *testing.T) {
	now := time.Now()
	if narratedRecently(time.Time{}, now) {
		t.Error("no narration yet must not count as recent")
	}
	if !narratedRecently(now.Add(-time.Minute), now) {
		t.Error("narration a minute ago is recent")
	}
	if narratedRecently(now.Add(-3*time.Minute), now) {
		t.Error("narration 3 minutes ago is not recent")
	}
	var g *narrationGate
	if !g.LastSent().IsZero() {
		t.Error("nil gate LastSent must be zero")
	}
}

func TestWithChatStylePrompt(t *testing.T) {
	plain := withChatStylePrompt(nil)
	if !slices.Equal(plain, []string{"--append-system-prompt", chatStylePrompt}) {
		t.Fatalf("no plugin args: %v", plain)
	}
	if strings.ContainsAny(chatStylePrompt, "\"\r\n%&|<>") {
		t.Fatal("chatStylePrompt must stay one line without shell-special characters")
	}
	in := []string{"--mcp-config", "x.json", "--append-system-prompt", "TOOLS"}
	out := withChatStylePrompt(in)
	if in[3] != "TOOLS" {
		t.Fatal("input slice was modified")
	}
	if n := strings.Count(strings.Join(out, " "), "--append-system-prompt"); n != 1 {
		t.Fatalf("want exactly one --append-system-prompt, got %d: %v", n, out)
	}
	if out[3] != chatStylePrompt+"\n\nTOOLS" {
		t.Fatalf("style must lead the plugin prompt, got %q", out[3])
	}
	// Every claude worker turn carries it.
	args := workerBaseArgs(&Config{}, RunRequest{SessionID: "s"}, "", "", "")
	if i := slices.Index(args, "--append-system-prompt"); i < 0 || args[i+1] != chatStylePrompt {
		t.Fatalf("workerBaseArgs missing the style prompt: %v", args)
	}
}

// narrationClient calls OnNarration mid-turn like the claude runner does.
type narrationClient struct{ fakeClaude }

func (n *narrationClient) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	if req.OnNarration != nil {
		req.OnNarration("먼저 확인해 볼게요.")
	}
	return n.fakeClaude.Run(ctx, req)
}

type narrSender struct {
	fakeSender
	events []string
}

func (p *narrSender) Send(id int64, text string) error {
	p.events = append(p.events, "send:"+text)
	return p.fakeSender.Send(id, text)
}
func (p *narrSender) Progress(_ int64, text string) { p.events = append(p.events, "progress:"+text) }

func TestRunWorker_RelaysNarrationBeforeAnswer(t *testing.T) {
	nc := &narrationClient{fakeClaude{runRes: RunResult{Text: "다 됐어요."}}}
	m, _, _ := tgManager(t, &nc.fakeClaude)
	m.client, m.claudeClient = nc, nc
	p := &narrSender{}
	m.Handle(context.Background(), 1, "확인해줘", OriginTelegram, p)

	ni := slices.Index(p.events, "progress:"+narrationPrefix+"먼저 확인해 볼게요.")
	ai := slices.Index(p.events, "send:다 됐어요.")
	if ni < 0 || ai < 0 || ni > ai {
		t.Fatalf("want narration relayed before the answer, events: %v", p.events)
	}
}

func TestRemoteChatChannel_NarrationFrame(t *testing.T) {
	r := &remoteChatChannel{send: make(chan controlOut, 4), cancel: func() {}}
	r.Progress(TelegramTarget(), 1, narrationPrefix+"테스트 돌려볼게요")
	r.Progress(TelegramTarget(), 1, "🔧 Bash: go test")
	first, second := <-r.send, <-r.send
	if first.Frame.Type != "narration" || first.Frame.Text != "테스트 돌려볼게요" {
		t.Fatalf("narration frame = %+v", first.Frame)
	}
	if second.Frame.Type != "progress" {
		t.Fatalf("tool line frame = %+v", second.Frame)
	}
}

func TestTelegramChannel_ProgressToolLinesStaySilent(t *testing.T) {
	tc := &telegramChannel{api: nil} // a Send would panic on the nil api
	tc.Progress(TelegramTarget(), 1, "🔧 Bash: go test")
}

func TestTypingLoops_RefreshUntilStop(t *testing.T) {
	var mu sync.Mutex
	n := 0
	l := newTypingLoops(func(int64) { mu.Lock(); n++; mu.Unlock() })
	l.interval = 10 * time.Millisecond
	l.start(7)
	l.start(7) // already running: one extra send, no second loop
	time.Sleep(60 * time.Millisecond)
	l.stop(7)
	if l.running(7) {
		t.Fatal("loop still registered after stop")
	}
	mu.Lock()
	after := n
	mu.Unlock()
	if after < 4 {
		t.Fatalf("expected repeated typing actions, got %d", after)
	}
	time.Sleep(40 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if n != after {
		t.Fatalf("typing continued after stop: %d → %d", after, n)
	}
}

func TestTypingLoops_MaxLifeEnds(t *testing.T) {
	l := newTypingLoops(func(int64) {})
	l.interval = time.Hour
	l.maxLife = 20 * time.Millisecond
	l.start(1)
	waitFor(t, func() bool { return !l.running(1) })
	l.stop(1) // no-op, must not panic
}

func TestReactionParams(t *testing.T) {
	p := reactionParams(-100123, 42, reactionSeen)
	if p["chat_id"] != "-100123" || p["message_id"] != "42" {
		t.Fatalf("params = %v", p)
	}
	var r []map[string]string
	if err := json.Unmarshal([]byte(p["reaction"]), &r); err != nil || len(r) != 1 || r[0]["type"] != "emoji" || r[0]["emoji"] != "👀" {
		t.Fatalf("reaction = %q (%v)", p["reaction"], err)
	}
}

func TestMergeQueuedHead_KeepsTelegramMessageIDs(t *testing.T) {
	q := []queuedMsg{
		{chatID: 1, text: "a", origin: OriginTelegram, tgMsgIDs: []int{1}},
		{chatID: 1, text: "b", origin: OriginTelegram, tgMsgIDs: []int{2}},
	}
	out := mergeQueuedHead(q)
	if len(out) != 1 || !slices.Equal(out[0].tgMsgIDs, []int{1, 2}) {
		t.Fatalf("merged ids = %+v", out)
	}
}

func TestConfigYAML_TurnStats(t *testing.T) {
	c, err := unmarshalConfigYAML([]byte("telegram:\n  bot_token: x\n  allowed_user_ids: [1]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.TurnStats {
		t.Fatal("ui.turn_stats must default to off")
	}
	c.TurnStats = true
	b, err := marshalConfigYAML(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "turn_stats: true") {
		t.Fatalf("marshalled yaml lacks ui.turn_stats:\n%s", b)
	}
	c2, err := unmarshalConfigYAML(b)
	if err != nil || !c2.TurnStats {
		t.Fatalf("round trip lost ui.turn_stats (%v)", err)
	}
}
