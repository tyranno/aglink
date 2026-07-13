package main

import (
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// tokenOf underpins the web-chat-only setup path introduced alongside the
// codex-only work: when the wizard skips Telegram entirely, api is nil, and the
// config's bot token must come back as "" rather than crash. Nothing else tests
// this — the RunSetup wizard is interactive — so the one nil-guard the audit
// item asks about ("does every path that assumed api non-nil still hold?") had
// no coverage.
func TestTokenOf(t *testing.T) {
	if got := tokenOf(nil); got != "" {
		t.Errorf("tokenOf(nil) = %q, want empty (web-only setup has no bot)", got)
	}
	api := &tgbotapi.BotAPI{Token: "123:ABC"}
	if got := tokenOf(api); got != "123:ABC" {
		t.Errorf("tokenOf(api) = %q, want the bot token", got)
	}
}
