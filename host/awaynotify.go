package main

import (
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Away notifications: a desktop (or web chat) turn that finishes while the
// user is away from the PC sends a short Telegram line, so a phone tells them
// the long job is done ("🔔 [대화이름] 작업 끝남 · 12분 걸림 · 답변 첫 줄…").
//
// "Away" is the OS idle time — no keyboard/mouse input for
// notify.away_idle_minutes (default 5) — read through GetLastInputInfo on
// Windows (useridle_windows.go). Elsewhere the idle time is unknown and nothing
// is sent: a Linux host is not the PC the user sits at.
//
// Only web-conversation turns are reported. The Telegram stream already
// answers in Telegram, so a notice there would only repeat the answer.
// Approvals need no notice of their own: askApproval already sends the full
// prompt (with buttons) to Telegram whenever Telegram is configured; it now
// names the conversation (approvalConvTitle).
//
// Telegram not configured (no bot / no allowed users) → silently nothing.

const defaultAwayIdleMinutes = 5

type awayNotifier struct {
	cfg func() *Config
	// idle reports how long the user has been idle; ok=false when unknown
	// (non-Windows, API failure) — then nothing is sent.
	idle func() (time.Duration, bool)
	// send delivers one text to one Telegram chat, and nothing else (no web
	// fan-out). nil → Telegram not configured.
	send func(chatID int64, text string) error
	// spawn runs the delivery; tests run it inline.
	spawn func(func())
}

// newTelegramAwayNotifier wires the notifier to the Telegram API directly —
// not through Bot.Send, which also fans the telegram stream out to the desktop
// and would drop the notice into the user's Telegram conversation view there.
func newTelegramAwayNotifier(cfg func() *Config, api *tgbotapi.BotAPI) *awayNotifier {
	n := &awayNotifier{cfg: cfg, idle: userIdleDuration}
	if api != nil {
		n.send = func(chatID int64, text string) error {
			_, err := api.Send(tgbotapi.NewMessage(chatID, text))
			return err
		}
	}
	return n
}

// awayThreshold is the idle time after which the user counts as away.
func awayThreshold(c *Config) time.Duration {
	m := c.AwayIdleMinutes
	if m <= 0 {
		m = defaultAwayIdleMinutes
	}
	return time.Duration(m) * time.Minute
}

// userAway reports whether a notice should go out now: enabled, Telegram set
// up, and the user idle for at least the threshold.
func (n *awayNotifier) userAway() bool {
	if n == nil || n.send == nil || n.cfg == nil || n.idle == nil {
		return false
	}
	c := n.cfg()
	if c == nil || !c.AwayNotify || len(c.AllowedUserIDs) == 0 {
		return false
	}
	d, ok := n.idle()
	return ok && d >= awayThreshold(c)
}

// notify sends msg to every allowed Telegram user when the user is away.
// Reports whether it was sent (queued).
func (n *awayNotifier) notify(msg string) bool {
	if !n.userAway() {
		return false
	}
	ids := append([]int64(nil), n.cfg().AllowedUserIDs...)
	run := n.spawn
	if run == nil {
		run = func(f func()) { go f() }
	}
	run(func() {
		for _, id := range ids {
			if err := n.send(id, msg); err != nil {
				log.Printf("[away] telegram send: %v", err)
			}
		}
	})
	return true
}

// turnDone reports a finished web-conversation turn. failed carries the error
// text for a failed turn ("" = success; text is then the answer).
func (n *awayNotifier) turnDone(title string, elapsed time.Duration, text string, failed bool) bool {
	if n == nil {
		return false
	}
	var msg string
	if failed {
		msg = formatAwayFailed(title, elapsed, text)
	} else {
		msg = formatAwayDone(title, elapsed, text)
	}
	return n.notify(msg)
}

// formatAwayDone: "🔔 [제목] 작업 끝남 · 12분 걸림 · 답변 첫 줄…".
func formatAwayDone(title string, elapsed time.Duration, answer string) string {
	msg := fmt.Sprintf("🔔 [%s] 작업 끝남 · %s 걸림", awayTitle(title), awayDuration(elapsed))
	if line := firstLine(answer, 80); line != "" {
		msg += " · " + line
	}
	return msg
}

// formatAwayFailed: "⚠️ [제목] 작업 실패 · 3분 걸림 · 이유".
func formatAwayFailed(title string, elapsed time.Duration, reason string) string {
	msg := fmt.Sprintf("⚠️ [%s] 작업 실패 · %s 걸림", awayTitle(title), awayDuration(elapsed))
	if line := firstLine(reason, 120); line != "" {
		msg += " · " + line
	}
	return msg
}

func awayTitle(t string) string {
	t = strings.TrimSpace(t)
	if t == "" {
		return "대화"
	}
	return truncateRunes(t, 40)
}

// awayDuration: "40초", "12분", "1시간 5분".
func awayDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d초", int(d.Seconds()))
	}
	mins := int(d.Minutes())
	if mins < 60 {
		return fmt.Sprintf("%d분", mins)
	}
	if mins%60 == 0 {
		return fmt.Sprintf("%d시간", mins/60)
	}
	return fmt.Sprintf("%d시간 %d분", mins/60, mins%60)
}

// firstLine is the first non-blank line of s without markdown decoration,
// cut to max runes with "…".
func firstLine(s string, max int) string {
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "```") {
			continue
		}
		l = strings.TrimLeft(l, "#>*-` ")
		l = strings.TrimSpace(strings.ReplaceAll(l, "**", ""))
		if l == "" {
			continue
		}
		return truncateRunes(l, max)
	}
	return ""
}

func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max]) + "…"
}

// approvalConvTitle names the conversation an approval came from (session is
// the AGLINK_OWNER_LABEL, "chat:<id>/conv:<id>"), "" when it is unknown.
func (b *Bot) approvalConvTitle(session string) string {
	if b == nil || b.store == nil || session == "" {
		return ""
	}
	i := strings.LastIndex(session, "conv:")
	if i < 0 {
		return ""
	}
	id := session[i+len("conv:"):]
	if c, ok := b.store.GetWebConv(id); ok && c != nil {
		return strings.TrimSpace(c.Title)
	}
	if c := b.store.TelegramConversation(); c != nil && c.ID == id {
		return strings.TrimSpace(c.Title)
	}
	return ""
}
