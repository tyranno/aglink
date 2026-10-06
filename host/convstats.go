package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Per-conversation usage statistics, exposed by the control verb
// get_usage_stats (chatcontrol.go). Counters and the bounded per-turn records
// live on the Conversation itself, so they persist in store.json alongside
// UsageTotal and survive restarts.

// maxTurnStats bounds Conversation.TurnStats (most recent records kept).
const maxTurnStats = 200

// recordTurnStat folds one finished turn into c's statistics.
func recordTurnStat(c *Conversation, st TurnStat) {
	c.TurnCount++
	if st.Reset {
		c.ResetCount++
	}
	c.TurnStats = append(c.TurnStats, st)
	if over := len(c.TurnStats) - maxTurnStats; over > 0 {
		c.TurnStats = append([]TurnStat(nil), c.TurnStats[over:]...)
	}
}

// usageTotals is CumUsage without omitempty, so a UI always sees every field.
type usageTotals struct {
	Input      int     `json:"input"`
	CacheRead  int     `json:"cacheRead"`
	CacheWrite int     `json:"cacheWrite"`
	Output     int     `json:"output"`
	CostUSD    float64 `json:"costUsd"`
}

func usageTotalsOf(c CumUsage) usageTotals {
	return usageTotals{Input: c.Input, CacheRead: c.Read, CacheWrite: c.Write, Output: c.Output, CostUSD: c.Cost}
}

func (u usageTotals) total() int { return u.Input + u.CacheRead + u.CacheWrite + u.Output }

// cacheHitRatio is cache-read tokens over all prompt tokens (input + cache read +
// cache write); 0 when there were none.
func cacheHitRatio(input, read, write int) float64 {
	den := input + read + write
	if den == 0 {
		return 0
	}
	return float64(read) / float64(den)
}

// usageStatsRecent aggregates the conversation's retained TurnStats window.
type usageStatsRecent struct {
	Turns         int            `json:"turns"`
	CostUSD       float64        `json:"costUsd"`
	TotalTokens   int            `json:"totalTokens"`
	CacheHitRatio float64        `json:"cacheHitRatio"`
	Resets        int            `json:"resets"`
	Recovered     int            `json:"recovered"`
	SummaryUsed   int            `json:"summaryUsed"`
	LightTurns    int            `json:"lightTurns"`
	AvgContext    int            `json:"avgContextTokens"`
	MaxContext    int            `json:"maxContextTokens"`
	Models        map[string]int `json:"models"` // model → turns
}

type usageStatsConv struct {
	Kind          string           `json:"kind"` // "telegram" | "web" | "project"
	Project       string           `json:"project,omitempty"`
	ID            string           `json:"id"`
	Title         string           `json:"title"`
	Backend       string           `json:"backend,omitempty"`
	PinnedModel   string           `json:"pinnedModel,omitempty"`
	LastModel     string           `json:"lastModel,omitempty"`
	Turns         int              `json:"turns"`  // all-time
	Resets        int              `json:"resets"` // all-time context-size resets
	Usage         usageTotals      `json:"usage"`  // all-time
	TotalTokens   int              `json:"totalTokens"`
	CacheHitRatio float64          `json:"cacheHitRatio"`
	ContextTokens int              `json:"contextTokens"` // current session context size (claude: last round-trip; codex: last input_tokens)
	HasSummary    bool             `json:"hasSummary"`
	ScreenUsed    bool             `json:"screenUsed"`
	LastActivity  time.Time        `json:"lastActivity"`
	Recent        usageStatsRecent `json:"recent"`
	RecentTurns   []TurnStat       `json:"recentTurns"` // oldest → newest, at most `limit`
}

type usageStatsTotals struct {
	Conversations int         `json:"conversations"`
	Turns         int         `json:"turns"`
	Resets        int         `json:"resets"`
	Usage         usageTotals `json:"usage"`
	TotalTokens   int         `json:"totalTokens"`
	CacheHitRatio float64     `json:"cacheHitRatio"`
}

type usageStatsResponse struct {
	GeneratedAt   time.Time        `json:"generatedAt"`
	Totals        usageStatsTotals `json:"totals"`
	Conversations []usageStatsConv `json:"conversations"`
	Error         string           `json:"error,omitempty"`
}

const (
	usageStatsDefaultLimit = 20  // recent turns per conversation in the all-conversations view
	usageStatsTargetLimit  = 200 // … when a single conversation is requested
)

func buildUsageStatsConv(kind, project string, c *Conversation, limit int) usageStatsConv {
	u := usageTotalsOf(c.UsageTotal)
	ctx := c.ClaudeContextTokens
	if ctx == 0 {
		ctx = c.CodexContextTokens
	}
	out := usageStatsConv{
		Kind: kind, Project: project, ID: c.ID, Title: c.Title, Backend: c.Backend,
		PinnedModel: c.PinnedModel, LastModel: c.LastModel,
		Turns: c.TurnCount, Resets: c.ResetCount,
		Usage: u, TotalTokens: u.total(),
		CacheHitRatio: cacheHitRatio(u.Input, u.CacheRead, u.CacheWrite),
		ContextTokens: ctx, HasSummary: c.RollingSummary != "", ScreenUsed: c.ScreenUsed,
		LastActivity: c.LastActivity,
		Recent:       usageStatsRecent{Models: map[string]int{}},
		RecentTurns:  []TurnStat{},
	}
	var in, rd, wr, ctxSum, ctxN int
	for _, t := range c.TurnStats {
		r := &out.Recent
		r.Turns++
		r.CostUSD += t.CostUSD
		r.TotalTokens += t.Input + t.CacheRead + t.CacheWrite + t.Output
		in, rd, wr = in+t.Input, rd+t.CacheRead, wr+t.CacheWrite
		if t.Reset {
			r.Resets++
		}
		if t.Recovered {
			r.Recovered++
		}
		if t.SummaryUsed {
			r.SummaryUsed++
		}
		if t.Tier == "light" {
			r.LightTurns++
		}
		if t.ContextTokens > 0 {
			ctxSum += t.ContextTokens
			ctxN++
			if t.ContextTokens > r.MaxContext {
				r.MaxContext = t.ContextTokens
			}
		}
		if t.Model != "" {
			r.Models[t.Model]++
		}
	}
	out.Recent.CacheHitRatio = cacheHitRatio(in, rd, wr)
	if ctxN > 0 {
		out.Recent.AvgContext = ctxSum / ctxN
	}
	if limit > 0 {
		from := len(c.TurnStats) - limit
		if from < 0 {
			from = 0
		}
		out.RecentTurns = append(out.RecentTurns, c.TurnStats[from:]...)
	}
	return out
}

// buildUsageStatsResponse aggregates usage across conversations. tgt != nil
// selects one conversation (telegram stream or web topic); nil returns every
// conversation that has recorded any usage, newest activity first. limit caps
// recentTurns per conversation (<=0 → default for the view).
func buildUsageStatsResponse(store StoreRepo, tgt *Target, limit int) usageStatsResponse {
	resp := usageStatsResponse{GeneratedAt: time.Now().UTC(), Conversations: []usageStatsConv{}}
	if tgt != nil {
		if limit <= 0 {
			limit = usageStatsTargetLimit
		}
		var c *Conversation
		kind := TargetTelegram
		if tgt.IsWeb() {
			kind = TargetWeb
			wc, ok := store.GetWebConv(tgt.ID)
			if !ok || wc == nil {
				resp.Error = fmt.Sprintf("web conversation not found: %s", tgt.ID)
				return resp
			}
			c = wc
		} else {
			c = store.TelegramConversation()
		}
		resp.Conversations = append(resp.Conversations, buildUsageStatsConv(kind, "", c, limit))
	} else {
		if limit <= 0 {
			limit = usageStatsDefaultLimit
		}
		hasUsage := func(c *Conversation) bool {
			return c != nil && (c.TurnCount > 0 || c.UsageTotal.Total() > 0)
		}
		if tc := store.TelegramConversation(); hasUsage(tc) {
			resp.Conversations = append(resp.Conversations, buildUsageStatsConv(TargetTelegram, "", tc, limit))
		}
		for _, c := range store.ListWebConvs() {
			if hasUsage(c) {
				resp.Conversations = append(resp.Conversations, buildUsageStatsConv(TargetWeb, "", c, limit))
			}
		}
		for name, p := range store.ListProjects() {
			for _, c := range p.Conversations {
				if hasUsage(c) {
					resp.Conversations = append(resp.Conversations, buildUsageStatsConv("project", name, c, limit))
				}
			}
		}
		sort.SliceStable(resp.Conversations, func(i, j int) bool {
			return resp.Conversations[i].LastActivity.After(resp.Conversations[j].LastActivity)
		})
	}
	t := &resp.Totals
	for _, c := range resp.Conversations {
		t.Conversations++
		t.Turns += c.Turns
		t.Resets += c.Resets
		t.Usage.Input += c.Usage.Input
		t.Usage.CacheRead += c.Usage.CacheRead
		t.Usage.CacheWrite += c.Usage.CacheWrite
		t.Usage.Output += c.Usage.Output
		t.Usage.CostUSD += c.Usage.CostUSD
	}
	t.TotalTokens = t.Usage.total()
	t.CacheHitRatio = cacheHitRatio(t.Usage.Input, t.Usage.CacheRead, t.Usage.CacheWrite)
	return resp
}

// normalizePinnedModel maps the "clear the pin" spellings to "".
func normalizePinnedModel(model string) string {
	m := strings.TrimSpace(model)
	switch strings.ToLower(m) {
	case "", "default", "off", "clear", "none", "auto", "기본":
		return ""
	}
	return m
}

// isClaudeModelName reports whether model names a claude model (alias or id), so
// a claude pin is not forced onto a codex/opencode turn.
func isClaudeModelName(model string) bool {
	l := strings.ToLower(model)
	for _, p := range []string{"opus", "sonnet", "haiku", "claude", "fable"} {
		if strings.HasPrefix(l, p) {
			return true
		}
	}
	return false
}
