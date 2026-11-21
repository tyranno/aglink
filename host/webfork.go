package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// "여기서 갈라 새 대화" — fork a conversation at one of its messages.
//
// The fork is a new top-level web conversation whose History is the source's
// history up to (and including) the chosen message, with the same working
// folder, backend override and pinned model. It gets a fresh SessionID and
// Started=false, so its first turn takes the ordinary fresh-session path in
// runWorker: historyForContext(…, resume=false) inlines the copied turns (or the
// rolling summary + tail when summary_on_reset is on) ahead of the new request.
// That is the same mechanism a context reset or session-loss recovery uses, so
// the AI picks up exactly where the fork point left off without any extra
// prompt plumbing.

// forkPoint identifies the message to fork at. The desktop sends what it shows:
// the bubble's role and text, its time (the turn timestamp for history loaded
// from the host, a local clock for live frames) and how many later bubbles of
// the same role carry the identical text (0 = the last such bubble).
type forkPoint struct {
	Role       string `json:"role"` // "user" | "assistant"
	Text       string `json:"text"`
	At         int64  `json:"at,omitempty"`
	Occurrence int    `json:"occurrence,omitempty"`
}

func forkNormalize(s string) string {
	s = strings.TrimPrefix(s, voiceInputPrefix)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.TrimSpace(s)
}

func forkField(t ConversationTurn, role string) string {
	if role == "user" {
		return forkNormalize(t.Prompt)
	}
	return forkNormalize(t.Response)
}

// forkTimestamp mirrors webshared.go's historyTurn.At: a user bubble's time is
// the turn's prompt timestamp, an assistant bubble's is when it was answered
// (falling back to the prompt time if that was never recorded).
func forkTimestamp(t ConversationTurn, role string) time.Time {
	if role == "user" || t.RespondedAt.IsZero() {
		return t.Timestamp
	}
	return t.RespondedAt
}

// findForkTurn returns the index of the history turn holding p. Tried in order:
//  1. same timestamp and same text (a bubble loaded from the host's history),
//  2. the p.Occurrence-th exact text match counted from the end (live bubbles,
//     whose time is the client's clock),
//  3. the latest turn whose text contains p.Text (a long live reply delivered
//     as several bubbles).
func findForkTurn(history []ConversationTurn, p forkPoint) (int, bool) {
	if p.Role != "user" && p.Role != "assistant" {
		return -1, false
	}
	want := forkNormalize(p.Text)
	if want == "" {
		return -1, false
	}
	if p.At != 0 {
		for i := len(history) - 1; i >= 0; i-- {
			ts := forkTimestamp(history[i], p.Role)
			if !ts.IsZero() && ts.UnixMilli() == p.At && forkField(history[i], p.Role) == want {
				return i, true
			}
		}
	}
	seen := 0
	for i := len(history) - 1; i >= 0; i-- {
		if forkField(history[i], p.Role) != want {
			continue
		}
		if seen == p.Occurrence {
			return i, true
		}
		seen++
	}
	for i := len(history) - 1; i >= 0; i-- {
		if strings.Contains(forkField(history[i], p.Role), want) {
			return i, true
		}
	}
	return -1, false
}

// forkedHistory copies history[:idx+1]. Forking at a user message keeps that
// request but not the answer it got, so the fork can take the conversation
// somewhere else from there.
func forkedHistory(history []ConversationTurn, idx int, role string) []ConversationTurn {
	out := make([]ConversationTurn, idx+1)
	copy(out, history[:idx+1])
	if role == "user" {
		last := &out[idx]
		last.Response = ""
		last.Images = nil
		last.Changes = nil
	}
	return out
}

var forkSuffixRe = regexp.MustCompile(` \(갈래(?: (\d+))?\)$`)

// forkTitle names the fork after its source: "제목" → "제목 (갈래)", and forking
// a fork again counts up — "제목 (갈래)" → "제목 (갈래 2)" — instead of stacking
// suffixes.
func forkTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "대화"
	}
	m := forkSuffixRe.FindStringSubmatch(title)
	if m == nil {
		return title + " (갈래)"
	}
	n := 1
	if m[1] != "" {
		n, _ = strconv.Atoi(m[1])
	}
	return fmt.Sprintf("%s (갈래 %d)", strings.TrimSuffix(title, m[0]), n+1)
}

// sourceConversation returns the conversation behind tgt (a copy), for the
// settings the fork inherits.
func (b *Bot) sourceConversation(tgt Target) (*Conversation, bool) {
	switch {
	case tgt.Kind == "telegram":
		c := b.store.TelegramConversation()
		return c, c != nil
	case tgt.Kind == "web" && tgt.Project == "":
		return b.store.GetWebConv(tgt.ID)
	default:
		return b.store.GetConversation(tgt.Project, tgt.ID)
	}
}

// webFork creates the fork described above and makes it the active web
// conversation. It is synchronous so the control API can acknowledge it with
// the new conversation's id, which the desktop then opens.
func (b *Bot) webFork(src Target, p forkPoint) (*Conversation, error) {
	source, ok := b.sourceConversation(src)
	if !ok || source == nil {
		return nil, fmt.Errorf("원래 대화를 찾을 수 없습니다")
	}
	history := b.store.HistorySnapshot(src)
	idx, ok := findForkTurn(history, p)
	if !ok {
		return nil, fmt.Errorf("갈라낼 메시지를 대화 기록에서 찾지 못했습니다")
	}
	turns := forkedHistory(history, idx, p.Role)

	created, err := b.store.NewWebConv(forkTitle(source.Title))
	if err != nil {
		return nil, fmt.Errorf("새 대화를 만들지 못했습니다: %w", err)
	}
	// NewWebConv hands back the stored pointer; edit a copy and save that.
	c, ok := b.store.GetWebConv(created.ID)
	if !ok || c == nil {
		return nil, fmt.Errorf("새 대화를 만들지 못했습니다")
	}
	c.History = turns
	c.WorkDir = source.WorkDir
	c.Backend = source.Backend
	c.PinnedModel = source.PinnedModel
	c.Interactive = source.Interactive
	c.InteractiveSet = source.InteractiveSet
	c.ScreenUsed = source.ScreenUsed
	// Reuse the source's rolling summary only if it covers nothing past the
	// fork point; otherwise the fork would "remember" turns it doesn't have.
	if source.RollingSummary != "" && !source.RollingSummaryUpTo.IsZero() {
		last := turns[len(turns)-1].Timestamp
		if source.RollingSummaryUpTo.Before(last) || (p.Role == "assistant" && source.RollingSummaryUpTo.Equal(last)) {
			c.RollingSummary = source.RollingSummary
			c.RollingSummaryUpTo = source.RollingSummaryUpTo
		}
	}
	c.LastActivity = time.Now().UTC()
	if err := b.store.UpdateWebConv(c); err != nil {
		_ = b.store.DeleteWebConv(c.ID)
		return nil, fmt.Errorf("새 대화를 저장하지 못했습니다: %w", err)
	}
	_ = b.store.SetActive("", c.ID)
	return c, nil
}

// webForkResult is the control-API reply for web_fork: {ok,id,title} or
// {ok:false,error}.
func webForkResult(c *Conversation, err error) json.RawMessage {
	out := map[string]any{"ok": err == nil}
	if err != nil {
		out["error"] = err.Error()
	} else {
		out["id"] = c.ID
		out["title"] = c.Title
	}
	b, _ := json.Marshal(out)
	return b
}
