package main

import (
	"log"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// TurnStatus is the in-place "still working" snapshot of a running Worker turn.
// runHeartbeat emits it instead of posting a new "⏳ 작업 진행 중..." message
// every tick, so a long turn shows ONE live status that updates in place:
//   - web/desktop render it in the working indicator at the bottom of the pane
//     (a "status" frame; the elapsed timer ticks client-side from Start);
//   - Telegram posts it once and then edits that same message (statusMessages),
//     deleting it when the turn ends.
//
// Warnings (the turn is about to hit its cap / idle stop) are NOT carried here —
// runHeartbeat still Sends them as ordinary messages so the user notices.
type TurnStatus struct {
	Label        string    // "작업 진행 중" | "세션 복구 진행 중" | "재시작 진행 중"
	Start        time.Time // turn start (the client ticks elapsed from here)
	LastActivity time.Time // latest stream activity; zero = unknown (no watchdog)
	// Text is the rendered routine line ("⏳ 작업 진행 중... (4분 0초 경과)") for
	// channels that show the status as text (Telegram, legacy fallback).
	Text string
	// Tick marks a heartbeat-cadence update (every heartbeatInterval). Between
	// ticks runHeartbeat sends lighter refreshes (statusInterval) that only
	// live-view channels care about; text channels act on ticks only.
	Tick bool
	// SkipText: a NEW text message must not be created for this update — the
	// model's narration reached the user recently, or a warning is being sent
	// as its own message. Editing an existing status message is still fine.
	SkipText bool
}

// statusChannel is the optional ChannelSender extension for TurnStatus. A
// channel that doesn't implement it gets the routine line as a plain Send on
// heartbeat ticks — the pre-status behavior — via Hub.Status.
type statusChannel interface {
	Status(tgt Target, chatID int64, st TurnStatus)
}

// turnStatusSender is the MessageSender-side view (target already bound) that
// runHeartbeat type-asserts; boundSender implements it. A sender without it
// (test fakes, legacy) keeps getting the heartbeat as text.
type turnStatusSender interface {
	Status(chatID int64, st TurnStatus)
}

// Status fans a TurnStatus out. Channels without statusChannel fall back to the
// old behavior: the routine line as a new message, on heartbeat ticks only.
func (h *Hub) Status(tgt Target, chatID int64, st TurnStatus) {
	for _, ch := range h.targets(tgt, chatID) {
		if sc, ok := ch.(statusChannel); ok {
			sc.Status(tgt, chatID, st)
			continue
		}
		if st.Tick && !st.SkipText && st.Text != "" {
			if err := ch.Send(tgt, chatID, st.Text); err != nil {
				log.Printf("[hub] channel status send error: %v", err)
			}
		}
	}
}

func (s boundSender) Status(chatID int64, st TurnStatus) { s.hub.Status(s.tgt, chatID, st) }

// statusFrame is the "status" wsFrame for web/desktop. Times are unix millis;
// At is the host's clock when the frame was built so a client on another
// machine can correct for clock skew. Older clients ignore the unknown type.
func statusFrame(tgt Target, st TurnStatus, now time.Time) *wsFrame {
	f := &wsFrame{Type: "status", Label: st.Label, StartedAt: st.Start.UnixMilli(), At: now.UnixMilli(), Target: &tgt}
	if !st.LastActivity.IsZero() {
		f.LastActivityAt = st.LastActivity.UnixMilli()
	}
	return f
}

// statusMessages keeps one editable Telegram status message per chat. The
// transport is injected (send/edit/del) so the logic is testable without a
// Telegram API.
type statusMessages struct {
	send func(chatID int64, text string) (int, error)
	edit func(chatID int64, msgID int, text string) error
	del  func(chatID int64, msgID int) error

	mu     sync.Mutex
	byChat map[int64]statusMsg
	// ended remembers the Start of the turn whose status was last cleared, so a
	// heartbeat tick racing with Done can't re-create a message nobody deletes.
	ended map[int64]time.Time
}

type statusMsg struct {
	id    int
	start time.Time
	text  string
}

func newStatusMessages(send func(int64, string) (int, error), edit func(int64, int, string) error, del func(int64, int) error) *statusMessages {
	return &statusMessages{send: send, edit: edit, del: del, byChat: map[int64]statusMsg{}, ended: map[int64]time.Time{}}
}

// update posts the status once, then edits that message on later ticks. If an
// edit fails (message deleted by the user, too old), it posts a fresh one.
func (m *statusMessages) update(chatID int64, st TurnStatus) {
	if !st.Tick || st.Text == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if end, ok := m.ended[chatID]; ok && end.Equal(st.Start) {
		return // late tick of a turn that already ended
	}
	cur, ok := m.byChat[chatID]
	if ok {
		if cur.text == st.Text {
			return
		}
		err := m.edit(chatID, cur.id, st.Text)
		if err == nil || strings.Contains(err.Error(), "message is not modified") {
			cur.text, cur.start = st.Text, st.Start
			m.byChat[chatID] = cur
			return
		}
		log.Printf("[tg] status edit failed, posting anew: %v", err)
	}
	if st.SkipText && !ok {
		return
	}
	id, err := m.send(chatID, st.Text)
	if err != nil {
		return
	}
	m.byChat[chatID] = statusMsg{id: id, start: st.Start, text: st.Text}
}

// clear deletes the chat's status message (turn ended).
func (m *statusMessages) clear(chatID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.byChat[chatID]
	if !ok {
		return
	}
	delete(m.byChat, chatID)
	m.ended[chatID] = cur.start
	if err := m.del(chatID, cur.id); err != nil {
		log.Printf("[tg] status delete error: %v", err)
	}
}

// newTelegramStatusMessages wires statusMessages to the Bot API.
func newTelegramStatusMessages(api *tgbotapi.BotAPI) *statusMessages {
	return newStatusMessages(
		func(chatID int64, text string) (int, error) {
			msg, err := api.Send(tgbotapi.NewMessage(chatID, text))
			if err != nil {
				log.Printf("[tg] status send error: %v", err)
				return 0, err
			}
			return msg.MessageID, nil
		},
		func(chatID int64, msgID int, text string) error {
			_, err := api.Request(tgbotapi.NewEditMessageText(chatID, msgID, text))
			return err
		},
		func(chatID int64, msgID int) error {
			_, err := api.Request(tgbotapi.NewDeleteMessage(chatID, msgID))
			return err
		},
	)
}

// Status shows the routine heartbeat as one Telegram message edited in place.
// Without a status tracker (tests) it falls back to a plain Send on ticks.
func (t *telegramChannel) Status(tgt Target, chatID int64, st TurnStatus) {
	if t.status == nil {
		if st.Tick && !st.SkipText && st.Text != "" && t.api != nil {
			_ = t.Send(tgt, chatID, st.Text)
		}
		return
	}
	t.status.update(chatID, st)
}

func (r *remoteChatChannel) Status(tgt Target, _ int64, st TurnStatus) {
	r.push(controlOut{Kind: "frame", Frame: statusFrame(tgt, st, time.Now())})
}

// Status shows a turn's in-place status on the telegram stream (Bot is the
// default MessageSender for telegram-originated work).
func (b *Bot) Status(chatID int64, st TurnStatus) { b.out.Status(TelegramTarget(), chatID, st) }
