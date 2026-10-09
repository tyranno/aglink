package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// Rolling per-conversation summary (Config.SummaryOnReset).
//
// When a CLI session is reset (claude/codex context too large) or lost
// (session-not-found recovery), the new session knows nothing, and aglink used to
// re-inline the last ~40 turns (up to maxHistoryCharsOnRecovery = 24k chars) of
// stored history. That is both expensive (re-sent on every API round-trip of the
// first fresh turn and then cached into the new session) and lossy (anything
// older than the window is gone).
//
// Instead, keep a compact summary of the conversation, produced by the cheap
// manager model (haiku), and inject summary + the last summaryTailTurns turns.
// The summary is pre-computed asynchronously once a session's context
// approaches its reset threshold (maybePrewarmSummary), so the reset turn
// itself usually needs no extra call; if it is stale, the reset turn brings it
// up to date synchronously (bounded by summarySyncTimeout). Any failure falls
// back to the previous behavior (historyForContext).

// textSummarizer is implemented by claudeRunner (manager-model one-shot call).
type textSummarizer interface {
	Summarize(ctx context.Context, prompt string) (string, error)
}

const (
	// summaryTailTurns verbatim recent turns injected next to the summary.
	summaryTailTurns = 5
	// summaryMaxChars bounds the stored/injected summary.
	summaryMaxChars = 4000
	// summaryInputBudgetChars bounds the turn text sent to the summarizer in one
	// call (newest turns kept when over budget). Haiku-priced, so generous.
	summaryInputBudgetChars = 60000
	// summaryTurnChars bounds each side of one turn inside the summarizer input.
	summaryTurnChars = 1500
	// summaryPrewarmRatio: start pre-computing the summary once the session's
	// context reaches this fraction of its reset threshold.
	summaryPrewarmRatio = 0.6
	// summaryPrewarmMinTurns: don't spend a call until at least this many turns
	// accumulated beyond the existing summary (and the verbatim tail).
	summaryPrewarmMinTurns = 3
	// summaryReuseMaxTurns / summaryReuseMaxChars: a fresh session reuses an
	// existing summary without a synchronous refresh when the turns it does not
	// cover number at most this many, or their raw text fits this budget
	// (summaryGapReusable); those turns are injected verbatim instead.
	summaryReuseMaxTurns = 6
	summaryReuseMaxChars = 12000
	summarySyncTimeout   = 90 * time.Second
	summaryAsyncTimeout  = 3 * time.Minute
)

// convSummaryState is a summary computed off the turn path (prewarm), held by
// the Manager until the conversation's next save picks it up — the background
// goroutine never touches a live Conversation.
type convSummaryState struct {
	text string
	upTo time.Time
}

// summaryKey identifies a conversation across sinks (project conversation IDs
// are only unique within a project).
func summaryKey(sink convSink, convID string) string {
	return sink.label() + "\x00" + convID
}

// summarizerClient returns the manager-model summarizer, or nil when the claude
// CLI isn't available (codex/opencode-only installs fall back to the old path).
func (m *Manager) summarizerClient() textSummarizer {
	m.backendMu.RLock()
	defer m.backendMu.RUnlock()
	if s, ok := m.claudeClient.(textSummarizer); ok && m.claudeClient != nil {
		return s
	}
	return nil
}

// bestSummary returns the newer of the conversation's persisted summary and a
// prewarmed one cached on the Manager.
func (m *Manager) bestSummary(key string, c *Conversation) (string, time.Time) {
	text, upTo := c.RollingSummary, c.RollingSummaryUpTo
	m.summaryMu.Lock()
	st, ok := m.summaries[key]
	m.summaryMu.Unlock()
	if ok && st.text != "" && (text == "" || st.upTo.After(upTo)) {
		text, upTo = st.text, st.upTo
	}
	return text, upTo
}

// adoptCachedSummary copies a newer prewarmed summary onto c so it is persisted
// with the conversation's next save (survives restarts).
func (m *Manager) adoptCachedSummary(key string, c *Conversation) {
	text, upTo := m.bestSummary(key, c)
	if text != "" && (c.RollingSummary == "" || upTo.After(c.RollingSummaryUpTo)) {
		c.RollingSummary, c.RollingSummaryUpTo = text, upTo
	}
}

func (m *Manager) storeSummary(key, text string, upTo time.Time) {
	m.summaryMu.Lock()
	defer m.summaryMu.Unlock()
	if m.summaries == nil {
		m.summaries = map[string]convSummaryState{}
	}
	if cur, ok := m.summaries[key]; ok && !upTo.After(cur.upTo) {
		return // a newer one already landed
	}
	m.summaries[key] = convSummaryState{text: text, upTo: upTo}
}

// turnsToSummarize returns the turns newer than upTo, excluding the last tail
// turns (those are injected verbatim anyway).
func turnsToSummarize(history []ConversationTurn, upTo time.Time, tail int) []ConversationTurn {
	end := len(history) - tail
	if end <= 0 {
		return nil
	}
	start := 0
	if !upTo.IsZero() {
		for start < end && !history[start].Timestamp.After(upTo) {
			start++
		}
	}
	if start >= end {
		return nil
	}
	return history[start:end]
}

// buildSummaryPrompt renders the summarizer instruction: fold `turns` into the
// existing summary `old`. Turns over summaryInputBudgetChars are dropped from the
// front (oldest first) — the old summary still covers what came before them.
func buildSummaryPrompt(old string, turns []ConversationTurn) string {
	start, chars := len(turns), 0
	for start > 0 {
		t := turns[start-1]
		n := len(truncate(t.Prompt, summaryTurnChars)) + len(truncate(t.Response, summaryTurnChars))
		if chars+n > summaryInputBudgetChars && start < len(turns) {
			break
		}
		chars += n
		start--
	}
	var b strings.Builder
	b.WriteString("너는 대화 요약기다. 아래 '기존 요약'과 그 이후의 '새 대화 기록'을 합쳐, 이 대화를 이어서 작업할 AI 에이전트가 읽을 갱신된 요약을 작성하라.\n")
	b.WriteString("규칙:\n")
	b.WriteString("- 한국어, 불릿 위주, 최대 2500자.\n")
	b.WriteString("- 반드시 포함: 사용자의 목표, 확정된 결정사항, 현재 진행 상태, 남은 할 일/미해결 문제, 중요한 파일·경로·명령·설정값, 사용자의 선호/금지사항.\n")
	b.WriteString("- 이미 끝나 더 이상 의미 없는 세부 과정, 인사말, 중복은 버린다. 기존 요약 내용 중 여전히 유효한 것은 유지한다.\n")
	b.WriteString("- 요약 본문만 출력한다. 머리말/맺음말 금지.\n\n")
	b.WriteString("## 기존 요약\n")
	if strings.TrimSpace(old) == "" {
		b.WriteString("(없음)\n")
	} else {
		b.WriteString(old + "\n")
	}
	b.WriteString("\n## 새 대화 기록\n")
	if start > 0 {
		fmt.Fprintf(&b, "(오래된 %d턴은 길이 제한으로 생략)\n", start)
	}
	for _, t := range turns[start:] {
		fmt.Fprintf(&b, "\n[%s]\n사용자: %s\n응답: %s\n",
			t.Timestamp.Format("2006-01-02 15:04"), truncate(t.Prompt, summaryTurnChars), truncate(t.Response, summaryTurnChars))
	}
	return b.String()
}

// summaryForFreshSession builds the context for a turn that runs on a fresh CLI
// session over an existing conversation: the rolling summary plus recent history
// verbatim. ok is false when the feature is off, the history is short enough
// that the old path is already cheap, or summarizing failed — the caller then
// falls back to historyForContext(history, false). On success
// c.RollingSummary/UpTo are updated (persisted with the turn's save).
//
// Three cases, by how many turns the existing summary does not yet cover
// (excluding the last summaryTailTurns, which are always injected verbatim):
//   - none: summary + tail, no call.
//   - a small gap (summaryGapReusable): summary + the uncovered turns + tail,
//     verbatim, with no synchronous call — the haiku call used to put 20–27s on
//     the reset turn's critical path. The summary is refreshed in the background
//     (refreshSummaryAsync) so the next fresh session finds it current.
//   - no summary yet, or a large gap: bring it up to date synchronously
//     (bounded by summarySyncTimeout).
func (m *Manager) summaryForFreshSession(ctx context.Context, key string, c *Conversation, history []ConversationTurn) (summary string, tail []ConversationTurn, ok bool) {
	if !m.cfg().SummaryOnReset || len(history) <= summaryTailTurns {
		return "", nil, false
	}
	summary, upTo := m.bestSummary(key, c)
	pending := turnsToSummarize(history, upTo, summaryTailTurns)
	if len(pending) > 0 && summary != "" && summaryGapReusable(pending) {
		// pending is the contiguous run just before the tail, so pending + tail
		// is everything after the summary's coverage.
		c.RollingSummary, c.RollingSummaryUpTo = summary, upTo
		if s := m.summarizerClient(); s != nil {
			m.refreshSummaryAsync(s, key, c.ID, summary, pending)
		}
		log.Printf("[summary] conv %s 요약 재사용 (미반영 %d턴 원문 첨부, 비동기 갱신)", c.ID, len(pending))
		return summary, history[len(history)-summaryTailTurns-len(pending):], true
	}
	if len(pending) > 0 {
		s := m.summarizerClient()
		if s == nil {
			return "", nil, false
		}
		sctx, cancel := context.WithTimeout(ctx, summarySyncTimeout)
		out, err := s.Summarize(sctx, buildSummaryPrompt(summary, pending))
		cancel()
		out = strings.TrimSpace(out)
		if err != nil || out == "" {
			log.Printf("[summary] conv %s 요약 갱신 실패 — 기존 방식(최근 기록 주입)으로 폴백: %v", c.ID, err)
			return "", nil, false
		}
		summary, upTo = truncate(out, summaryMaxChars), pending[len(pending)-1].Timestamp
		m.storeSummary(key, summary, upTo)
		log.Printf("[summary] conv %s 요약 갱신 (동기, %d턴 반영, %d자)", c.ID, len(pending), len(summary))
	}
	if summary == "" {
		return "", nil, false
	}
	c.RollingSummary, c.RollingSummaryUpTo = summary, upTo
	return summary, tailTurns(history, summaryTailTurns, maxHistoryCharsOnRecovery), true
}

// summaryGapReusable reports whether the turns an existing summary doesn't cover
// are few enough to inject verbatim instead of summarizing them synchronously:
// at most summaryReuseMaxTurns turns, or raw text within summaryReuseMaxChars.
func summaryGapReusable(pending []ConversationTurn) bool {
	if len(pending) <= summaryReuseMaxTurns {
		return true
	}
	chars := 0
	for _, t := range pending {
		chars += len(t.Prompt) + len(t.Response)
		if chars > summaryReuseMaxChars {
			return false
		}
	}
	return true
}

// maybePrewarmSummary refreshes the rolling summary in the background once the
// session's context nears its reset threshold, so the eventual reset turn finds
// it current and needs no synchronous call. history must be a snapshot the
// caller won't mutate concurrently (the goroutine only reads its own copy).
func (m *Manager) maybePrewarmSummary(key, convID string, c *Conversation, contextTokens, resetThreshold int) {
	if !m.cfg().SummaryOnReset || resetThreshold <= 0 ||
		float64(contextTokens) < float64(resetThreshold)*summaryPrewarmRatio {
		return
	}
	old, upTo := m.bestSummary(key, c)
	pending := turnsToSummarize(c.History, upTo, summaryTailTurns)
	if len(pending) < summaryPrewarmMinTurns {
		return
	}
	s := m.summarizerClient()
	if s == nil {
		return
	}
	m.refreshSummaryAsync(s, key, convID, old, pending)
}

// refreshSummaryAsync folds pending into old in the background and caches the
// result on the Manager (storeSummary); the conversation picks it up on its
// next save (adoptCachedSummary). At most one refresh per conversation is in
// flight; a call while one runs is a no-op. pending is copied, so the caller may
// keep mutating its history.
func (m *Manager) refreshSummaryAsync(s textSummarizer, key, convID, old string, pending []ConversationTurn) {
	if len(pending) == 0 {
		return
	}
	m.summaryMu.Lock()
	if m.summaryInflight == nil {
		m.summaryInflight = map[string]bool{}
	}
	if m.summaryInflight[key] {
		m.summaryMu.Unlock()
		return
	}
	m.summaryInflight[key] = true
	m.summaryMu.Unlock()

	turns := append([]ConversationTurn(nil), pending...)
	newUpTo := turns[len(turns)-1].Timestamp
	go func() {
		defer func() {
			m.summaryMu.Lock()
			delete(m.summaryInflight, key)
			m.summaryMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), summaryAsyncTimeout)
		defer cancel()
		out, err := s.Summarize(ctx, buildSummaryPrompt(old, turns))
		out = strings.TrimSpace(out)
		if err != nil || out == "" {
			log.Printf("[summary] conv %s 비동기 요약 실패 (다음 리셋 시 재시도): %v", convID, err)
			return
		}
		m.storeSummary(key, truncate(out, summaryMaxChars), newUpTo)
		log.Printf("[summary] conv %s 비동기 요약 완료 (%d턴 반영)", convID, len(turns))
	}()
}

// joinSummaries combines a continuation's parent summary with the rolling
// summary for the "이전 대화 요약" prompt section.
func joinSummaries(parent, rolling string) string {
	switch {
	case parent == "":
		return rolling
	case rolling == "":
		return parent
	default:
		return parent + "\n\n" + rolling
	}
}
