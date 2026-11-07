package main

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// Narration is the model's own mid-turn commentary ("먼저 테스트를 돌려볼게요")
// relayed to the user while the turn is still running, so a long job reads as a
// back-and-forth instead of minutes of silence followed by one answer.
//
// It travels over the existing Progress path tagged with narrationPrefix; each
// channel decides how to show it (Telegram: a normal message; the web chat and
// the desktop app: a "narration" frame rendered apart from the final answer).
const narrationPrefix = "💬 "

// narrationMaxRunes caps one relayed narration message.
const narrationMaxRunes = 300

// narrationMergeWindow: narrations arriving this soon after the last one sent
// are merged into one message, delivered when the window ends.
const narrationMergeWindow = 4 * time.Second

// isNarration reports whether a Progress line is narration (not a tool line).
func isNarration(text string) bool { return strings.HasPrefix(text, narrationPrefix) }

// narrationBody strips the narration tag.
func narrationBody(text string) string { return strings.TrimPrefix(text, narrationPrefix) }

// narrationTracker picks narration out of one turn's stream-json lines. A
// completed main-thread assistant text block is held back until a later
// tool_use proves it was not the turn's last word; the result line discards
// whatever is still held, because that text is the final answer, which is sent
// on its own. Sub-agent messages (parent_tool_use_id set) are ignored.
type narrationTracker struct {
	pending []string
}

// feed consumes one stream-json line and returns narration ready to relay, or "".
func (t *narrationTracker) feed(line string) string {
	if !strings.Contains(line, `"assistant"`) && !strings.Contains(line, `"result"`) {
		return "" // cheap pre-filter: partial deltas and tool results are the bulk
	}
	var m struct {
		Type            string  `json:"type"`
		ParentToolUseID *string `json:"parent_tool_use_id"`
		Message         *struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal([]byte(line), &m) != nil {
		return ""
	}
	switch m.Type {
	case "result":
		t.pending = nil
	case "assistant":
		if m.Message == nil || (m.ParentToolUseID != nil && *m.ParentToolUseID != "") {
			return ""
		}
		out := ""
		for _, b := range m.Message.Content {
			switch b.Type {
			case "text":
				if s := strings.TrimSpace(b.Text); s != "" {
					t.pending = append(t.pending, s)
				}
			case "tool_use":
				if len(t.pending) > 0 {
					if out != "" {
						out += "\n"
					}
					out += strings.Join(t.pending, "\n")
					t.pending = nil
				}
			}
		}
		return out
	}
	return ""
}

// narrationGate throttles narration delivery for one turn: the first message
// goes out at once, later ones arriving within narrationMergeWindow of the last
// send are merged and sent when the window ends. Close flushes what is left
// (it was said before the final answer, so it still belongs in front of it).
type narrationGate struct {
	mu     sync.Mutex
	send   func(string)
	window time.Duration
	last   time.Time
	buf    []string
	timer  *time.Timer
	closed bool
}

func newNarrationGate(send func(string)) *narrationGate {
	return &narrationGate{send: send, window: narrationMergeWindow}
}

// Add queues one narration.
func (g *narrationGate) Add(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return
	}
	g.buf = append(g.buf, text)
	if g.timer != nil {
		return // a merged flush is already scheduled
	}
	wait := g.window - time.Since(g.last)
	if wait <= 0 {
		g.flushLocked()
		return
	}
	g.timer = time.AfterFunc(wait, func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.timer = nil
		if !g.closed {
			g.flushLocked()
		}
	})
}

// LastSent is when narration last reached the user (zero if never).
func (g *narrationGate) LastSent() time.Time {
	if g == nil {
		return time.Time{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.last
}

// Close flushes anything pending and stops further delivery.
func (g *narrationGate) Close() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return
	}
	if g.timer != nil {
		g.timer.Stop()
		g.timer = nil
	}
	g.flushLocked()
	g.closed = true
}

func (g *narrationGate) flushLocked() {
	if len(g.buf) == 0 {
		return
	}
	text := truncate(strings.Join(g.buf, "\n"), narrationMaxRunes)
	g.buf = nil
	g.last = time.Now()
	g.send(text)
}
