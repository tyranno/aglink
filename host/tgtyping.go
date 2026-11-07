package main

import (
	"encoding/json"
	"log"
	"strconv"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Telegram clears a "typing…" chat action after ~5 seconds, so one action at
// turn start (and one per 2-minute heartbeat) left the chat looking idle for
// almost the whole turn. typingLoops re-sends it every typingRefresh from the
// first Typing until Done, per chat.
const (
	typingRefresh = 4 * time.Second
	// typingMaxLife stops a loop whose Done never came (a code path that
	// signalled Typing without a matching Done) instead of typing forever.
	typingMaxLife = 3 * time.Hour
)

type typingLoops struct {
	mu       sync.Mutex
	stops    map[int64]chan struct{}
	interval time.Duration
	maxLife  time.Duration
	send     func(chatID int64)
}

func newTypingLoops(send func(chatID int64)) *typingLoops {
	return &typingLoops{stops: map[int64]chan struct{}{}, interval: typingRefresh, maxLife: typingMaxLife, send: send}
}

// start sends the action now and keeps refreshing it until stop. A second start
// for a chat that is already typing only re-sends once.
func (l *typingLoops) start(chatID int64) {
	l.mu.Lock()
	if _, running := l.stops[chatID]; running {
		l.mu.Unlock()
		l.send(chatID)
		return
	}
	stop := make(chan struct{})
	l.stops[chatID] = stop
	l.mu.Unlock()

	l.send(chatID)
	go func() {
		ticker := time.NewTicker(l.interval)
		defer ticker.Stop()
		deadline := time.NewTimer(l.maxLife)
		defer deadline.Stop()
		for {
			select {
			case <-ticker.C:
				select {
				case <-stop: // stopped while the tick was pending
					return
				default:
				}
				l.send(chatID)
			case <-stop:
				return
			case <-deadline.C:
				l.mu.Lock()
				if l.stops[chatID] == stop {
					delete(l.stops, chatID)
				}
				l.mu.Unlock()
				return
			}
		}
	}()
}

// stop ends chatID's loop, if any.
func (l *typingLoops) stop(chatID int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if stop, ok := l.stops[chatID]; ok {
		close(stop)
		delete(l.stops, chatID)
	}
}

// running reports whether chatID has a live loop (tests).
func (l *typingLoops) running(chatID int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.stops[chatID]
	return ok
}

// Telegram reactions used as receipt marks on the user's own message.
const (
	reactionSeen = "👀" // the message was accepted and a turn will answer it
	reactionDone = "👍" // that turn finished
)

// reactionParams builds the setMessageReaction request. The pinned tgbotapi
// (v5.5.1) predates the method, so it goes through the raw request API.
func reactionParams(chatID int64, msgID int, emoji string) tgbotapi.Params {
	reaction, _ := json.Marshal([]map[string]string{{"type": "emoji", "emoji": emoji}})
	return tgbotapi.Params{
		"chat_id":    strconv.FormatInt(chatID, 10),
		"message_id": strconv.Itoa(msgID),
		"reaction":   string(reaction),
	}
}

// react sets emoji as the bot's reaction on a user message, in the background.
// Failures (old clients, reactions disabled in the chat) are only logged.
func (b *Bot) react(chatID int64, msgIDs []int, emoji string) {
	if b == nil || b.api == nil || len(msgIDs) == 0 {
		return
	}
	api := b.api
	ids := append([]int(nil), msgIDs...)
	go func() {
		for _, id := range ids {
			if id == 0 {
				continue
			}
			if _, err := api.MakeRequest("setMessageReaction", reactionParams(chatID, id, emoji)); err != nil {
				log.Printf("[tg] reaction %s on msg %d failed: %v", emoji, id, err)
			}
		}
	}()
}
