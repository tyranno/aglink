package main

import (
	"fmt"
	"strings"
)

// Design Ref: §5.2 — output relay (4096-char chunking, routing header). Presentation layer.

const telegramMaxLen = 4096

// MessageSender is the minimal Telegram surface relay needs (implemented in bot.go).
type MessageSender interface {
	Send(chatID int64, text string) error
	Typing(chatID int64)
	// Done signals that the current Worker turn has finished (success, error,
	// or cancellation) so channels that show a live "working" indicator (web
	// chat) can clear it. Telegram has no such indicator, so it's a no-op there.
	Done(chatID int64)
}

// sendChunked splits text and sends it as one or more Telegram messages.
func sendChunked(s MessageSender, chatID int64, text string) error {
	if strings.TrimSpace(text) == "" {
		text = "(빈 응답)"
	}
	for _, chunk := range chunkText(text, telegramMaxLen) {
		if err := s.Send(chatID, chunk); err != nil {
			return err
		}
	}
	return nil
}

// chunkText splits s into pieces of at most max runes, preferring newline boundaries.
func chunkText(s string, max int) []string {
	if max <= 0 {
		return []string{s}
	}
	r := []rune(s)
	if len(r) <= max {
		return []string{s}
	}
	var chunks []string
	for len(r) > max {
		cut := max
		// Prefer breaking at the last newline within the window for readability.
		if nl := lastIndexRune(r[:max], '\n'); nl > max/2 {
			cut = nl + 1 // include the newline in this chunk
		}
		chunks = append(chunks, string(r[:cut]))
		r = r[cut:]
	}
	if len(r) > 0 {
		chunks = append(chunks, string(r))
	}
	return chunks
}

func lastIndexRune(r []rune, target rune) int {
	for i := len(r) - 1; i >= 0; i-- {
		if r[i] == target {
			return i
		}
	}
	return -1
}

// routingHeader renders the "📂 project · 💬 conversation" status line.
func routingHeader(project, convTitle string, isNew bool) string {
	tag := "이어가기"
	if isNew {
		tag = "새 대화"
	}
	return fmt.Sprintf("📂 %s · 💬 %s (%s)", project, convTitle, tag)
}
