package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- item 1: real context size, not summed usage ----

func TestStreamTurnSignals_LastMainThreadRoundTrip(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"assistant","parent_tool_use_id":null,"message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}],"usage":{"input_tokens":10,"cache_read_input_tokens":90000,"cache_creation_input_tokens":1000}}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}`,
		// sub-agent (Task) message: its own context — must not count, but its screen tool use does.
		`{"type":"assistant","parent_tool_use_id":"toolu_1","message":{"content":[{"type":"tool_use","name":"mcp__screen__snapshot","input":{}}],"usage":{"input_tokens":5,"cache_read_input_tokens":500000,"cache_creation_input_tokens":0}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta"}}`,
		`{"type":"assistant","parent_tool_use_id":null,"message":{"content":[{"type":"text","text":"done"}],"usage":{"input_tokens":5,"cache_read_input_tokens":95000,"cache_creation_input_tokens":2000}}}`,
		// summed over the turn's round-trips: ~1M although the context is ~97k
		`{"type":"result","subtype":"success","result":"done","is_error":false,"usage":{"input_tokens":15,"cache_read_input_tokens":1000000,"cache_creation_input_tokens":3000,"output_tokens":400},"total_cost_usd":0.6}`,
	}
	res, err := parseStreamResult(strings.Join(lines, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if res.ContextTokens != 97005 {
		t.Errorf("ContextTokens = %d, want 97005 (last main-thread round-trip)", res.ContextTokens)
	}
	if res.CacheReadTokens != 1000000 {
		t.Errorf("summed usage must still be reported for cost accounting, got read=%d", res.CacheReadTokens)
	}
	if !res.ScreenToolUsed {
		t.Error("mcp__screen__ tool use not detected")
	}
}

func TestStreamTurnSignals_NoUsage(t *testing.T) {
	ctx, screen := streamTurnSignals([]string{`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`})
	if ctx != 0 || screen {
		t.Errorf("got ctx=%d screen=%v, want 0/false", ctx, screen)
	}
}

func TestWorkerBaseArgs_StreamFlagForcesStreamJSON(t *testing.T) {
	args := workerBaseArgs(&Config{}, RunRequest{Stream: true, SessionID: "s"}, "", "", "")
	if !strings.Contains(strings.Join(args, " "), "--output-format stream-json") {
		t.Errorf("Stream=true must request stream-json, got %v", args)
	}
}

// scriptedClient returns queued results and records every request. It also
// implements textSummarizer.
type scriptedClient struct {
	mu        sync.Mutex
	calls     []RunRequest
	results   []RunResult
	summary   string
	summErr   error
	summCalls []string
	summDone  chan struct{}
}

func (c *scriptedClient) Route(context.Context, RouteRequest) (RouteDecision, error) {
	return RouteDecision{}, errors.New("not used")
}

func (c *scriptedClient) Run(_ context.Context, req RunRequest) (RunResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, req)
	if len(c.results) == 0 {
		return RunResult{Text: "ok"}, nil
	}
	r := c.results[0]
	if len(c.results) > 1 {
		c.results = c.results[1:]
	}
	return r, nil
}

func (c *scriptedClient) Summarize(_ context.Context, prompt string) (string, error) {
	c.mu.Lock()
	c.summCalls = append(c.summCalls, prompt)
	done := c.summDone
	c.mu.Unlock()
	if done != nil {
		defer func() { done <- struct{}{} }()
	}
	return c.summary, c.summErr
}

func tokenFixture(t *testing.T, c ClaudeClient, cfg *Config) (*Manager, *fileStore) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	return NewManager(c, nil, st, NewConfigHolder(cfg)), st
}

func seedTelegram(t *testing.T, st *fileStore, mut func(c *Conversation)) {
	t.Helper()
	tc := st.TelegramConversation()
	tc.Started = true
	tc.SessionID = "live-session"
	mut(tc)
	if err := st.UpdateTelegramConversation(tc); err != nil {
		t.Fatal(err)
	}
}

// The live bug: a turn with many tool round-trips reported ~1M summed usage on a
// ~90k context and the next turn reset the session. Now only the real context
// size (last round-trip) drives the reset.
func TestRunWorker_ResetDecisionUsesRealContextSize(t *testing.T) {
	fc := &scriptedClient{results: []RunResult{
		{Text: "a", CacheReadTokens: 1_000_000, OutputTokens: 100, ContextTokens: 90_000},
		{Text: "b", CacheReadTokens: 1_000_000, OutputTokens: 100, ContextTokens: 160_000},
		{Text: "c", OutputTokens: 100, ContextTokens: 5_000, SessionID: "fresh"},
	}}
	m, st := tokenFixture(t, fc, &Config{ManagerAlways: true})
	seedTelegram(t, st, func(c *Conversation) {})

	for i := 0; i < 3; i++ {
		m.HandleWebTarget(context.Background(), 1, fmt.Sprintf("계속 %d", i), TelegramTarget(), &fakeSender{})
	}
	if len(fc.calls) != 3 {
		t.Fatalf("calls = %d", len(fc.calls))
	}
	if !fc.calls[0].Resume || !fc.calls[1].Resume {
		t.Fatalf("turns 1-2 must resume (1M summed usage is not context size): %v %v", fc.calls[0].Resume, fc.calls[1].Resume)
	}
	if fc.calls[2].Resume {
		t.Fatal("turn 3 must reset: previous turn's real context was 160k ≥ 150k")
	}
	if fc.calls[2].SessionID == "live-session" {
		t.Error("a reset must mint a new session id")
	}
	tc := st.TelegramConversation()
	if tc.ClaudeContextTokens != 5_000 {
		t.Errorf("ClaudeContextTokens = %d, want 5000", tc.ClaudeContextTokens)
	}
	if tc.ResetCount != 1 || tc.TurnCount != 3 || len(tc.TurnStats) != 3 || !tc.TurnStats[2].Reset {
		t.Errorf("stats: resets=%d turns=%d stats=%+v", tc.ResetCount, tc.TurnCount, tc.TurnStats)
	}
	if tc.UsageTotal.Read != 2_000_000 {
		t.Errorf("summed usage must still accumulate for cost: read=%d", tc.UsageTotal.Read)
	}
}

// ---- item 2: tiering + pin ----

func TestPickWorkerModel(t *testing.T) {
	m := &Manager{cfgh: NewConfigHolder(&Config{WorkerModel: "opus", WorkerModelLight: "sonnet", CodexModel: "gpt-5"})}

	if got, tier := m.pickWorkerModel("claude", "이 버그 고쳐줘", &Conversation{PinnedModel: "haiku"}, true); got != "haiku" || tier != "pinned" {
		t.Errorf("pin must win: %q/%q", got, tier)
	}
	if got, _ := m.pickWorkerModel("codex", "고마워", &Conversation{PinnedModel: "sonnet"}, true); got != "gpt-5" {
		t.Errorf("a claude pin must not reach codex: %q", got)
	}
	if got, tier := m.pickWorkerModel("claude", "고마워", &Conversation{ClaudeContextTokens: 5000, LastModel: "opus"}, true); got != "sonnet" || tier != "light" {
		t.Errorf("small context light turn → light: %q/%q", got, tier)
	}
	if got, _ := m.pickWorkerModel("claude", "고마워", &Conversation{ClaudeContextTokens: 90000, LastModel: "opus"}, true); got != "opus" {
		t.Errorf("large warm opus session must not downgrade (cache is per model): %q", got)
	}
	if got, _ := m.pickWorkerModel("claude", "고마워", &Conversation{ClaudeContextTokens: 90000, LastModel: "sonnet"}, true); got != "sonnet" {
		t.Errorf("already on light model → stay light: %q", got)
	}
	if got, _ := m.pickWorkerModel("claude", "이 버그 고쳐줘", &Conversation{ClaudeContextTokens: 90000, LastModel: "sonnet"}, true); got != "opus" {
		t.Errorf("heavy request must escalate: %q", got)
	}
	if got, _ := m.pickWorkerModel("claude", "고마워", &Conversation{ClaudeContextTokens: 90000, LastModel: "opus"}, false); got != "sonnet" {
		t.Errorf("fresh session has no cache to lose → light: %q", got)
	}
	off := &Manager{cfgh: NewConfigHolder(&Config{WorkerModel: "opus"})}
	if got, tier := off.pickWorkerModel("claude", "고마워", nil, false); got != "opus" || tier != "" {
		t.Errorf("tiering off: %q/%q", got, tier)
	}
}

func TestSetConvModel_PinAppliesAndClears(t *testing.T) {
	fc := &scriptedClient{}
	m, st := tokenFixture(t, fc, &Config{ManagerAlways: true, WorkerModel: "opus"})
	seedTelegram(t, st, func(c *Conversation) {})

	if got, err := m.SetConvModel(TelegramTarget(), " sonnet "); err != nil || got != "sonnet" {
		t.Fatalf("SetConvModel: %q %v", got, err)
	}
	m.HandleWebTarget(context.Background(), 1, "hi", TelegramTarget(), &fakeSender{})
	if fc.calls[0].Model != "sonnet" {
		t.Errorf("pinned model not used: %q", fc.calls[0].Model)
	}
	tc := st.TelegramConversation()
	if tc.LastModel != "sonnet" || tc.TurnStats[0].Tier != "pinned" || tc.PinnedModel != "sonnet" {
		t.Errorf("bookkeeping: last=%q tier=%q pin=%q", tc.LastModel, tc.TurnStats[0].Tier, tc.PinnedModel)
	}
	if got, _ := m.SetConvModel(TelegramTarget(), "default"); got != "" {
		t.Errorf("default must clear, got %q", got)
	}
	m.HandleWebTarget(context.Background(), 1, "hi", TelegramTarget(), &fakeSender{})
	if fc.calls[1].Model != "opus" {
		t.Errorf("after unpin → config model, got %q", fc.calls[1].Model)
	}
	if _, err := m.SetConvModel(WebTarget("nope"), "sonnet"); err == nil {
		t.Error("unknown web conversation must error")
	}
}

// ---- item 3: rolling summary ----

func historyN(n int) []ConversationTurn {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h := make([]ConversationTurn, n)
	for i := range h {
		h[i] = ConversationTurn{Timestamp: base.Add(time.Duration(i) * time.Minute), Prompt: fmt.Sprintf("PROMPT-%02d", i), Response: fmt.Sprintf("RESP-%02d", i)}
	}
	return h
}

func TestTurnsToSummarize(t *testing.T) {
	h := historyN(12)
	got := turnsToSummarize(h, time.Time{}, 5)
	if len(got) != 7 || got[0].Prompt != "PROMPT-00" || got[6].Prompt != "PROMPT-06" {
		t.Fatalf("no summary yet → all but the tail: %+v", got)
	}
	got = turnsToSummarize(h, h[3].Timestamp, 5)
	if len(got) != 3 || got[0].Prompt != "PROMPT-04" {
		t.Fatalf("covered turns excluded: %+v", got)
	}
	if got := turnsToSummarize(h, h[8].Timestamp, 5); got != nil {
		t.Fatalf("fully covered → nil, got %+v", got)
	}
	if got := turnsToSummarize(h[:4], time.Time{}, 5); got != nil {
		t.Fatalf("shorter than tail → nil, got %+v", got)
	}
	p := buildSummaryPrompt("OLD-SUMMARY", h[:2])
	if !strings.Contains(p, "OLD-SUMMARY") || !strings.Contains(p, "PROMPT-01") {
		t.Errorf("summary prompt missing parts: %s", p)
	}
}

func TestRunWorker_ResetInjectsRollingSummary(t *testing.T) {
	fc := &scriptedClient{summary: "SUMMARY-X", results: []RunResult{{Text: "ok", SessionID: "fresh", ContextTokens: 4000}}}
	m, st := tokenFixture(t, fc, &Config{ManagerAlways: true, SummaryOnReset: true})
	h := historyN(20)
	seedTelegram(t, st, func(c *Conversation) { c.History = h; c.ClaudeContextTokens = 200_000 })

	m.HandleWebTarget(context.Background(), 1, "다음 작업", TelegramTarget(), &fakeSender{})

	if len(fc.calls) != 1 || fc.calls[0].Resume {
		t.Fatalf("expected one fresh (reset) run, got %+v", fc.calls)
	}
	p := fc.calls[0].Prompt
	if !strings.Contains(p, "SUMMARY-X") || !strings.Contains(p, "PROMPT-19") || !strings.Contains(p, "PROMPT-15") {
		t.Errorf("prompt must carry summary + last 5 turns:\n%s", p)
	}
	if strings.Contains(p, "PROMPT-14") {
		t.Errorf("turns older than the tail must come via the summary only:\n%s", p)
	}
	if len(fc.summCalls) != 1 || !strings.Contains(fc.summCalls[0], "PROMPT-00") || strings.Contains(fc.summCalls[0], "PROMPT-15") {
		t.Errorf("summarizer input should be turns 0..14: %v", fc.summCalls)
	}
	tc := st.TelegramConversation()
	if tc.RollingSummary != "SUMMARY-X" || !tc.RollingSummaryUpTo.Equal(h[14].Timestamp) {
		t.Errorf("summary not persisted: %q %v", tc.RollingSummary, tc.RollingSummaryUpTo)
	}
	last := tc.TurnStats[len(tc.TurnStats)-1]
	if !last.SummaryUsed || !last.Reset {
		t.Errorf("turn stat flags: %+v", last)
	}
}

func TestRunWorker_SummaryFailureFallsBackToHistory(t *testing.T) {
	fc := &scriptedClient{summErr: errors.New("boom"), results: []RunResult{{Text: "ok", SessionID: "fresh"}}}
	m, st := tokenFixture(t, fc, &Config{ManagerAlways: true, SummaryOnReset: true})
	seedTelegram(t, st, func(c *Conversation) { c.History = historyN(20); c.ClaudeContextTokens = 200_000 })

	m.HandleWebTarget(context.Background(), 1, "다음 작업", TelegramTarget(), &fakeSender{})

	if p := fc.calls[0].Prompt; !strings.Contains(p, "PROMPT-05") {
		t.Errorf("fallback must inline the long history slice:\n%s", p)
	}
	tc := st.TelegramConversation()
	if tc.TurnStats[0].SummaryUsed || tc.RollingSummary != "" {
		t.Errorf("no summary must be recorded on failure: %+v %q", tc.TurnStats[0], tc.RollingSummary)
	}
}

func TestRunWorker_SummaryDisabledKeepsOldBehavior(t *testing.T) {
	fc := &scriptedClient{summary: "SUMMARY-X", results: []RunResult{{Text: "ok", SessionID: "fresh"}}}
	m, st := tokenFixture(t, fc, &Config{ManagerAlways: true, SummaryOnReset: false})
	seedTelegram(t, st, func(c *Conversation) { c.History = historyN(20); c.ClaudeContextTokens = 200_000 })
	m.HandleWebTarget(context.Background(), 1, "다음 작업", TelegramTarget(), &fakeSender{})
	if len(fc.summCalls) != 0 || strings.Contains(fc.calls[0].Prompt, "SUMMARY-X") {
		t.Error("summary_on_reset=false must not summarize")
	}
}

func TestMaybePrewarmSummary_AsyncThenAdopted(t *testing.T) {
	fc := &scriptedClient{summary: "PREWARM", summDone: make(chan struct{}, 1)}
	m, _ := tokenFixture(t, fc, &Config{SummaryOnReset: true})
	c := &Conversation{ID: "telegram", History: historyN(12)}

	m.maybePrewarmSummary("k", c.ID, c, 10_000, claudeContextResetTokens) // well below 60%
	select {
	case <-fc.summDone:
		t.Fatal("must not prewarm on a small context")
	case <-time.After(50 * time.Millisecond):
	}

	m.maybePrewarmSummary("k", c.ID, c, 100_000, claudeContextResetTokens)
	select {
	case <-fc.summDone:
	case <-time.After(5 * time.Second):
		t.Fatal("prewarm summary never ran")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if text, _ := m.bestSummary("k", c); text == "PREWARM" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("prewarmed summary not cached")
		}
		time.Sleep(10 * time.Millisecond)
	}
	m.adoptCachedSummary("k", c)
	if c.RollingSummary != "PREWARM" || !c.RollingSummaryUpTo.Equal(c.History[6].Timestamp) {
		t.Errorf("adopt: %q %v", c.RollingSummary, c.RollingSummaryUpTo)
	}
}

// ---- item 5: conditional screen guidance ----

func TestScreenBriefFor(t *testing.T) {
	off := &Manager{cfgh: NewConfigHolder(&Config{ScreenControl: false, ScreenPromptAdaptive: true})}
	if off.screenBriefFor(&Conversation{}, "hi") {
		t.Error("screen disabled → nothing to condense")
	}
	notAdaptive := &Manager{cfgh: NewConfigHolder(&Config{ScreenControl: true})}
	if notAdaptive.screenBriefFor(&Conversation{}, "hi") {
		t.Error("adaptive off → always full guidance")
	}
	m := &Manager{cfgh: NewConfigHolder(&Config{ScreenControl: true, ScreenPromptAdaptive: true})}
	c := &Conversation{}
	if !m.screenBriefFor(c, "오늘 일정 정리해줘") {
		t.Error("non-screen request on unused conversation → brief")
	}
	if m.screenBriefFor(c, "메모장 창을 열어서 저장 버튼 눌러줘") || !c.ScreenUsed {
		t.Error("screen request → full + sticky")
	}
	if m.screenBriefFor(c, "고마워") {
		t.Error("sticky: once used, always full")
	}
}

func TestPluginWorkerArgs_ScreenBrief(t *testing.T) {
	full := appendSystemPrompt(pluginWorkerArgsOpts(&Config{ScreenControl: true}, "screen.exe", "", "", false))
	brief := appendSystemPrompt(pluginWorkerArgsOpts(&Config{ScreenControl: true}, "screen.exe", "", "", true))
	if !strings.Contains(full, screenSystemPrompt()) {
		t.Error("full mode must carry the full guidance")
	}
	if strings.Contains(brief, screenSystemPrompt()) || !strings.Contains(brief, screenSystemPromptBrief()) {
		t.Error("brief mode must carry only the brief guidance")
	}
	if len(brief) >= len(full)/2 {
		t.Errorf("brief should be much shorter: %d vs %d", len(brief), len(full))
	}
	for _, kw := range []string{"snapshot", "get_text", "SCREEN_BUSY", "return_desktop"} {
		if !strings.Contains(screenSystemPromptBrief(), kw) {
			t.Errorf("brief guidance missing %q", kw)
		}
	}
}

// ---- item 6: usage stats ----

func TestRecordTurnStat_Bounded(t *testing.T) {
	c := &Conversation{}
	for i := 0; i < maxTurnStats+30; i++ {
		recordTurnStat(c, TurnStat{Output: i, Reset: i%10 == 0})
	}
	if len(c.TurnStats) != maxTurnStats || c.TurnCount != maxTurnStats+30 || c.ResetCount != 23 {
		t.Fatalf("len=%d count=%d resets=%d", len(c.TurnStats), c.TurnCount, c.ResetCount)
	}
	if c.TurnStats[0].Output != 30 {
		t.Errorf("oldest kept should be #30, got %d", c.TurnStats[0].Output)
	}
}

func TestChatControl_UsageStatsAndSetConvModel(t *testing.T) {
	_, st := tokenFixture(t, &scriptedClient{}, &Config{})
	seedTelegram(t, st, func(c *Conversation) {
		c.UsageTotal = CumUsage{Input: 100, Read: 800, Write: 100, Output: 50, Cost: 0.5}
		recordTurnStat(c, TurnStat{Model: "opus", Tier: "heavy", Input: 100, CacheRead: 800, CacheWrite: 100, Output: 50, CostUSD: 0.5, ContextTokens: 1000})
		recordTurnStat(c, TurnStat{Model: "sonnet", Tier: "light", Reset: true, ContextTokens: 3000})
	})
	wc, _ := st.NewWebConv("unused topic")
	_ = wc

	mgr := NewManager(&scriptedClient{}, nil, st, NewConfigHolder(&Config{}))
	b := &Bot{store: st, manager: mgr}
	s := &chatControlServer{ownerChatID: 7, bot: b}
	ch := &remoteChatChannel{send: make(chan controlOut, 8), cancel: func() {}}

	s.handleInbound(ch, controlIn{Type: "get_usage_stats", ReqID: "u1"})
	outs := drainControlOut(ch.send)
	if len(outs) != 1 || outs[0].ReqID != "u1" {
		t.Fatalf("replies: %+v", outs)
	}
	var resp usageStatsResponse
	if err := json.Unmarshal(outs[0].Data, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Conversations) != 1 {
		t.Fatalf("only conversations with usage are listed, got %d", len(resp.Conversations))
	}
	cv := resp.Conversations[0]
	if cv.Kind != "telegram" || cv.Turns != 2 || cv.Resets != 1 || cv.TotalTokens != 1050 || cv.CacheHitRatio != 0.8 {
		t.Errorf("conv stats: %+v", cv)
	}
	if cv.Recent.LightTurns != 1 || cv.Recent.MaxContext != 3000 || cv.Recent.AvgContext != 2000 || cv.Recent.Models["opus"] != 1 || len(cv.RecentTurns) != 2 {
		t.Errorf("recent: %+v turns=%d", cv.Recent, len(cv.RecentTurns))
	}
	if resp.Totals.Conversations != 1 || resp.Totals.Usage.CostUSD != 0.5 {
		t.Errorf("totals: %+v", resp.Totals)
	}
	// raw JSON keys the desktop UI relies on
	for _, key := range []string{`"conversations"`, `"totals"`, `"cacheHitRatio"`, `"recentTurns"`, `"contextTokens"`, `"costUsd"`} {
		if !strings.Contains(string(outs[0].Data), key) {
			t.Errorf("reply missing key %s", key)
		}
	}

	s.handleInbound(ch, controlIn{Type: "set_conv_model", ReqID: "m1", Model: "sonnet"})
	outs = drainControlOut(ch.send)
	var setResp map[string]any
	_ = json.Unmarshal(outs[0].Data, &setResp)
	if setResp["ok"] != true || setResp["model"] != "sonnet" {
		t.Fatalf("set_conv_model reply: %s", outs[0].Data)
	}
	if st.TelegramConversation().PinnedModel != "sonnet" {
		t.Error("pin not stored")
	}
	tgt := WebTarget("missing")
	s.handleInbound(ch, controlIn{Type: "set_conv_model", ReqID: "m2", Model: "sonnet", Target: &tgt})
	outs = drainControlOut(ch.send)
	_ = json.Unmarshal(outs[0].Data, &setResp)
	if setResp["ok"] != false {
		t.Errorf("unknown target must fail: %s", outs[0].Data)
	}

	tg := TelegramTarget()
	s.handleInbound(ch, controlIn{Type: "get_usage_stats", ReqID: "u2", Target: &tg, Limit: 1})
	outs = drainControlOut(ch.send)
	resp = usageStatsResponse{}
	_ = json.Unmarshal(outs[0].Data, &resp)
	if len(resp.Conversations) != 1 || len(resp.Conversations[0].RecentTurns) != 1 || resp.Conversations[0].PinnedModel != "sonnet" {
		t.Errorf("targeted stats: %+v", resp.Conversations)
	}
}

func TestSettings_TokenSavingKeys(t *testing.T) {
	cfg := &Config{TelegramBotToken: "x", AllowedUserIDs: []int64{1}}
	if err := applySettings(cfg, map[string]any{"context.summary_on_reset": true, "screen_control.adaptive_prompt": true}); err != nil {
		t.Fatal(err)
	}
	if !cfg.SummaryOnReset || !cfg.ScreenPromptAdaptive {
		t.Errorf("settings not applied: %+v", cfg)
	}
	y, err := marshalConfigYAML(cfg)
	if err != nil {
		t.Fatal(err)
	}
	back, err := unmarshalConfigYAML(y)
	if err != nil || !back.SummaryOnReset || !back.ScreenPromptAdaptive {
		t.Errorf("round-trip lost values: %v", err)
	}
	const base = "telegram:\n  bot_token: x\n  allowed_user_ids: [1]\n"
	def, err := unmarshalConfigYAML([]byte(base + "models:\n  worker: opus\n"))
	if err != nil || !def.SummaryOnReset || !def.ScreenPromptAdaptive {
		t.Errorf("both default to true when unset: %v", err)
	}
	off, err := unmarshalConfigYAML([]byte(base + "context:\n  summary_on_reset: false\nscreen_control:\n  adaptive_prompt: false\n"))
	if err != nil || off.SummaryOnReset || off.ScreenPromptAdaptive {
		t.Errorf("explicit false must stick: %v", err)
	}
}

func TestClaudeTurnCost_DiffsCumulativeSessionCost(t *testing.T) {
	c := &Conversation{}
	// Measured CLI values: 0.0016345 → 0.0026594 → 0.0036443 over three resumes.
	if got := claudeTurnCost(c, "s1", 0.0016345, true); got != 0.0016345 {
		t.Fatalf("fresh turn = %v, want full cumulative", got)
	}
	if got := claudeTurnCost(c, "s1", 0.0026594, false); math.Abs(got-0.0010249) > 1e-9 {
		t.Fatalf("resumed turn = %v, want 0.0010249", got)
	}
	// Session reset → new id: cumulative restarts, taken as-is.
	if got := claudeTurnCost(c, "s2", 0.5, false); got != 0.5 {
		t.Fatalf("new session turn = %v, want 0.5", got)
	}
	// Same id but the CLI started over (lower cumulative): taken as-is.
	if got := claudeTurnCost(c, "s2", 0.1, false); got != 0.1 {
		t.Fatalf("restarted session turn = %v, want 0.1", got)
	}
	// Session-loss recovery reuses the id but is fresh.
	claudeTurnCost(c, "s3", 2.0, false)
	if got := claudeTurnCost(c, "s3", 2.5, true); got != 2.5 {
		t.Fatalf("recovered turn = %v, want 2.5", got)
	}
}
