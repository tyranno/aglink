package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Design Ref: §2.1, §4.3 — Telegram polling, auth, command dispatch, queue + /cancel.
// Presentation layer; implements MessageSender for relay.

type queuedMsg struct {
	chatID  int64
	text    string
	isTask  bool    // true = scheduled task (bypass manager routing)
	project string  // isTask only: project pinned on the Task at creation time
	origin  string  // "telegram"|"web" — channel that sent it (tags new conversations)
	target  *Target // non-nil for web sends with an explicit target (telegram stream vs web topic)
	laneKey string
}

// conversation is where this message's queue/timeout/cancel notices belong: the
// conversation it was sent from. A message with no explicit target (Telegram
// input, scheduled tasks) belongs to the telegram stream.
func (m queuedMsg) conversation() Target {
	if m.target != nil {
		return *m.target
	}
	return TelegramTarget()
}

func (m queuedMsg) queueKey() string {
	if m.laneKey != "" {
		return m.laneKey
	}
	return laneKeyOf(m.conversation())
}

// Bot dispatches Telegram messages to concurrent Workers.
// Up to cfg.MaxWorkers (default 3) can run at the same time; extras are queued.
type Bot struct {
	api         *tgbotapi.BotAPI
	cfgh        *ConfigHolder
	store       StoreRepo
	manager     *Manager
	scheduler   *Scheduler
	rateLimiter *RateLimiter
	userStore   *UserStore
	playbooks   *PlaybookStore // reusable work routines (업무 관리); nil in tests that don't need it
	onReady     func() // called once after GetUpdatesChan starts (handoff signal)
	out         *Hub   // output fan-out: telegram (global) + web channels (per-chat)

	dispatchHook func(chatID int64, text string) // test seam; nil in production
	commandHook  func(chatID int64, text string) // test seam; nil in production

	// Per-conversation lanes. Each conversation (the telegram stream, or a web
	// topic) is a lane: turns within a lane run strictly one at a time and in
	// order (a turn's context depends on the previous turn's result), while
	// different lanes run in parallel. cfg.MaxWorkers bounds how many lanes run
	// concurrently — it no longer serializes unrelated conversations behind one
	// global pool, which is what stopped them running in parallel.
	mu           sync.Mutex
	workerSeq    int                  // monotonic counter for worker IDs
	cancels      map[int]*cancelEntry // workerID → (laneKey, cancel, deadline) for !cancel / !timeout
	lanes        map[string]*lane    // laneKey → its queue + running flag
	readyLanes   []string            // idle lanes with queued work, waiting for a global slot (FIFO)
	runningLanes int                 // lanes currently executing a turn (== global concurrency)

	// cmdMu serializes handleCommand. Telegram's Run() loop already calls
	// handleCommand from a single goroutine, so this is uncontended there.
	// The web-chat reader loop spawns a goroutine per inbound message
	// (go s.inject), so without this lock two rapid "!update" (or other
	// command) messages from the browser could run handleCommand concurrently
	// — e.g. two overlapping self-rebuild+restart flows racing on the same
	// newExe/readyFile. handleCommand's doc comment ("processes commands
	// synchronously") is the invariant this lock restores.
	cmdMu sync.Mutex
}

func NewBot(api *tgbotapi.BotAPI, cfgh *ConfigHolder, store StoreRepo, manager *Manager, scheduler *Scheduler, userStore *UserStore) *Bot {
	hub := NewHub()
	// In web-chat-only mode api is nil: register no Telegram channel, so the
	// telegram stream simply has no global sink (web channels register separately).
	if api != nil {
		hub.RegisterGlobal(newTelegramChannel(api))
	}
	return &Bot{
		api:         api,
		cfgh:        cfgh,
		store:       store,
		manager:     manager,
		scheduler:   scheduler,
		rateLimiter: NewRateLimiter(cfgh.Get().RateLimitPerMin),
		userStore:   userStore,
		cancels:     make(map[int]*cancelEntry),
		lanes:       make(map[string]*lane),
		out:         hub,
	}
}

// lane is one conversation's work: normally a FIFO of pending turns that never
// overlap (running/queue), but an interactive lane (Manager.IsInteractive, set
// once when the lane is created — see dispatch) instead lets turns run
// concurrently against the same live ConPTY session, steering a message in
// while an earlier one is still producing output instead of making it wait.
// inFlight counts turns currently executing in this lane: always 0 or 1 for a
// normal lane, but can be >1 for an interactive one. Known caveat: two
// runWorker calls racing on the same *Conversation's persisted History/
// SessionID can clobber each other's write (last one to save wins) — the live
// claude.exe session itself still holds full context either way, so this can
// at worst drop a turn from the on-disk history log, not from the
// conversation's actual context.
type lane struct {
	queue       []queuedMsg
	running     bool
	interactive bool
	inFlight    int
}

// cancelEntry ties a running worker's cancel func to the lane it belongs to, so
// !cancel can target only the caller's own conversation instead of every lane.
// It also carries a resettable enforcement timer so !timeout can push (or pull,
// down to the base) a single running turn's deadline. All fields are guarded by
// Bot.mu; the timer's AfterFunc callback re-locks Bot.mu before touching them.
type cancelEntry struct {
	key      string
	cancel   context.CancelFunc
	timer    *time.Timer // enforcement timer; !timeout calls Reset on it
	start    time.Time   // when this turn started, anchor for the effective total
	deadline time.Time   // current enforcement deadline (moves when !timeout fires)
	timedOut bool        // set by the timer before it cancels, to tell timeout apart from !cancel
}

// laneKeyOf maps a message's conversation to its lane key. All non-web targets
// are the single telegram stream ("telegram"); each web topic is its own lane
// ("web:<id>"). Matches Target.SameConversation, so two messages share a lane
// exactly when they belong to the same conversation.
func laneKeyOf(t Target) string {
	if t.IsWeb() {
		return "web:" + t.ID
	}
	return "telegram"
}

func (b *Bot) cfg() *Config { return b.cfgh.Get() }

// isAllowed checks all auth sources: config IDs, config usernames, and runtime UserStore.
func (b *Bot) isAllowed(userID int64, username string) bool {
	return b.cfg().IsAllowed(userID) ||
		b.cfg().IsAllowedByUsername(username) ||
		(b.userStore != nil && b.userStore.Contains(userID))
}

// telegramChannel is the Telegram implementation of ChannelSender. The tgbotapi
// send bodies live here (moved out of *Bot) so Bot's own Send/SendPhoto/Typing
// can delegate to the Hub. Registered in the Hub as a global channel — it can
// address any chatID, so it receives fan-out for every conversation.
type telegramChannel struct {
	api *tgbotapi.BotAPI
}

func newTelegramChannel(api *tgbotapi.BotAPI) *telegramChannel {
	return &telegramChannel{api: api}
}

// Every telegramChannel method ignores the Target: the Hub already withholds
// web-topic output from global channels (see Hub.targets), so anything reaching
// here belongs to the telegram stream by construction.

func (t *telegramChannel) Send(_ Target, chatID int64, text string) error {
	msg := tgbotapi.NewMessage(chatID, text)
	_, err := t.api.Send(msg)
	if err != nil {
		log.Printf("[tg] send error: %v", err)
	}
	return err
}

func (t *telegramChannel) SendPhoto(_ Target, chatID int64, png []byte, caption string) error {
	photo := tgbotapi.NewPhoto(chatID, tgbotapi.FileBytes{Name: "screen.png", Bytes: png})
	if caption != "" {
		photo.Caption = caption
	}
	_, err := t.api.Send(photo)
	if err != nil {
		log.Printf("[tg] photo send error: %v", err)
	}
	return err
}

func (t *telegramChannel) Typing(_ Target, chatID int64) {
	if _, err := t.api.Request(tgbotapi.NewChatAction(chatID, tgbotapi.ChatTyping)); err != nil {
		log.Printf("[tg] typing error: %v", err)
	}
}

// Done is a no-op for Telegram — it has no persistent "working" indicator to clear.
func (t *telegramChannel) Done(Target, int64) {}

// Progress is a no-op for Telegram — it has no live progress view to update.
func (t *telegramChannel) Progress(Target, int64, string) {}

// EchoUser relays user input that originated from the web to Telegram. A Telegram
// user's own message is already visible in their client, so telegram-origin is a
// no-op. Bots can't post as the user, so web input arrives as a bot-authored line.
// Only input addressed to the telegram stream reaches here, so a web topic's
// input is never mirrored into Telegram.
func (t *telegramChannel) EchoUser(tgt Target, chatID int64, text, origin string) {
	if origin == OriginWeb {
		_ = t.Send(tgt, chatID, "🌐 (웹) "+text)
	}
}

// Bot itself is a MessageSender addressing the telegram stream — the right
// default for bot commands and startup notices, which are telegram-originated.
// Work that belongs to a web topic must go through For(tgt) instead, so its
// output never reaches Telegram.

// Send delivers a plain-text message to the telegram stream's channels.
func (b *Bot) Send(chatID int64, text string) error {
	return b.out.Send(TelegramTarget(), chatID, text)
}

// SendPhoto delivers a PNG image with optional caption to the telegram stream.
func (b *Bot) SendPhoto(chatID int64, png []byte, caption string) error {
	return b.out.SendPhoto(TelegramTarget(), chatID, png, caption)
}

// Typing shows the "typing…" indicator on the telegram stream's channels.
func (b *Bot) Typing(chatID int64) {
	b.out.Typing(TelegramTarget(), chatID)
}

// Done signals turn completion on the telegram stream's channels.
func (b *Bot) Done(chatID int64) {
	b.out.Done(TelegramTarget(), chatID)
}

// For returns a sender bound to tgt. Binding the target to the sender (rather
// than threading it through every Send/Typing/Done call) keeps the manager's
// output call sites unchanged while still addressing each turn's output to the
// conversation it belongs to.
func (b *Bot) For(tgt Target) MessageSender { return boundSender{hub: b.out, tgt: tgt} }

// boundSender is a Target-bound view of the Hub. It satisfies MessageSender and
// also SendPhoto and Progress, which runWorker type-asserts for screen-capture
// images and live tool-use progress lines respectively.
type boundSender struct {
	hub *Hub
	tgt Target
}

func (s boundSender) Send(chatID int64, text string) error { return s.hub.Send(s.tgt, chatID, text) }
func (s boundSender) SendPhoto(chatID int64, png []byte, caption string) error {
	return s.hub.SendPhoto(s.tgt, chatID, png, caption)
}
func (s boundSender) Typing(chatID int64) { s.hub.Typing(s.tgt, chatID) }
func (s boundSender) Done(chatID int64)   { s.hub.Done(s.tgt, chatID) }
func (s boundSender) Progress(chatID int64, text string) {
	s.hub.Progress(s.tgt, chatID, text)
}

// replySender is the output surface a command handler needs to answer the
// conversation it was invoked from: text, images (!screen), and indicators.
type replySender interface {
	MessageSender
	SendPhoto(chatID int64, png []byte, caption string) error
}

// ReplyTo returns a sender that answers only tgt's conversation.
func (b *Bot) ReplyTo(tgt Target) replySender { return boundSender{hub: b.out, tgt: tgt} }

// targetBinder is implemented by senders that can rebind themselves to a Target.
// The manager uses it to scope a turn's output; a sender that doesn't implement
// it (test fakes) simply keeps its default addressing.
type targetBinder interface {
	For(Target) MessageSender
}

// bindTarget scopes s to tgt when it supports it, else returns s unchanged.
func bindTarget(s MessageSender, tgt Target) MessageSender {
	if tb, ok := s.(targetBinder); ok {
		return tb.For(tgt)
	}
	return s
}

// Hub returns the output fan-out hub so other transports (web chat) can register
// their own channels.
func (b *Bot) Hub() *Hub { return b.out }

// Run starts the long-polling loop. Blocks until the process exits.
// Uses GetUpdates directly (not GetUpdatesChan) so Conflict errors are visible
// and trigger an automatic restart via os.Exit(1) + systemd Restart=on-failure.
func (b *Bot) Run() {
	// Web-chat-only mode: no Telegram api → no polling. Block forever so the
	// process stays alive while the web frontend goroutines serve the browser.
	if b.api == nil {
		log.Printf("[bot] 웹채팅 전용 모드 — 텔레그램 폴링 없음. 웹 프론트엔드 대기 중.")
		if b.onReady != nil {
			b.onReady()
		}
		select {}
	}
	log.Printf("[bot] @%s online, long-polling started", b.api.Self.UserName)
	if b.onReady != nil {
		b.onReady() // fire after polling confirmed — used by handoff to signal old process
	}

	offset := 0
	for {
		cfg := tgbotapi.NewUpdate(offset)
		cfg.Timeout = 30
		updates, err := b.api.GetUpdates(cfg)
		if err != nil {
			if strings.Contains(err.Error(), "Conflict") {
				// Another instance is polling the same token.
				// Exit so systemd restarts us; killPreviousInstance() will then
				// terminate the other instance before we start polling again.
				log.Printf("[bot] Conflict — 다른 인스턴스가 polling 중. 5초 후 재시작.")
				time.Sleep(5 * time.Second)
				b.manager.CloseInteractive()
				os.Exit(1)
			}
			log.Printf("[bot] getUpdates 실패: %v — 3초 후 재시도", err)
			time.Sleep(3 * time.Second)
			continue
		}

		for _, update := range updates {
			if update.UpdateID+1 > offset {
				offset = update.UpdateID + 1
			}
			if update.Message == nil || update.Message.From == nil {
				continue
			}
			userID := update.Message.From.ID
			username := update.Message.From.UserName
			if !b.isAllowed(userID, username) {
				log.Printf("[bot] denied user %d (%s)", userID, username)
				continue
			}
			chatID := update.Message.Chat.ID

			// Attachments take priority over text — download then dispatch with caption.
			if b.hasAttachment(update.Message) {
				go b.handleAttachment(chatID, update.Message)
				continue
			}

			text := strings.TrimSpace(update.Message.Text)
			if text == "" {
				continue
			}
			// Rate-limit free-text messages only (commands are cheap, workers are expensive).
			if !strings.HasPrefix(text, "!") && !b.rateLimiter.Allow(userID) {
				_ = b.Send(chatID, "⚠️ 요청이 너무 많습니다. 잠시 후 다시 시도해 주세요.")
				log.Printf("[bot] rate-limited user %d", userID)
				continue
			}
			if strings.HasPrefix(text, "!") {
				b.handleCommand(chatID, text, OriginTelegram, TelegramTarget())
				continue
			}
			b.dispatchText(chatID, text, OriginTelegram)
		}
	}
}

// dispatchText routes a free-text message through the Manager.
// Up to cfg.MaxWorkers can run in parallel; extras are queued.
func (b *Bot) dispatchText(chatID int64, text, origin string) {
	// Mirror the user's input to the OTHER channel so both web and Telegram show
	// what was typed, wherever it was entered. Each channel no-ops for its own
	// origin (ChannelSender.EchoUser), so the origin channel never double-echoes.
	// This path carries no explicit target, so it is the telegram stream.
	if b.out != nil {
		b.out.EchoUser(TelegramTarget(), chatID, text, origin)
	}
	if b.dispatchHook != nil {
		b.dispatchHook(chatID, text)
		return
	}
	b.dispatch(queuedMsg{chatID: chatID, text: text, origin: origin})
}

// dispatchTargeted routes a web message to its explicit target: the global
// telegram stream (kind "telegram") or a specific web topic (kind "web"). A nil
// tgt (a web client that hasn't yet been updated to send a target) defaults to
// the telegram stream. Like dispatchText, this goes through the shared
// worker-slot queue (dispatch()) — so it gets the same MaxWorkers limiting,
// TimeoutMinutes deadline, !cancel registration, and panic recovery as every
// other dispatch — rather than calling the Manager directly.
func (b *Bot) dispatchTargeted(chatID int64, text string, tgt *Target) {
	t := TelegramTarget()
	if tgt != nil {
		t = *tgt
	}
	// Mirror web-typed input to the other channel, same as dispatchText. Echoing
	// to t (not the telegram stream) is what keeps a web topic's input out of
	// Telegram: Hub.targets drops the global channels for a web target.
	if b.out != nil {
		b.out.EchoUser(t, chatID, text, OriginWeb)
	}
	b.dispatch(queuedMsg{chatID: chatID, text: text, origin: OriginWeb, target: &t})
}

// dispatchScheduledTask runs a pre-scheduled task bypassing Manager LLM routing.
// Up to cfg.MaxWorkers can run in parallel; extras are queued.
func (b *Bot) dispatchScheduledTask(chatID int64, text, project string) {
	_ = b.Send(chatID, "⏰ 예약 작업 실행 중: "+truncate(text, 60))
	b.dispatch(queuedMsg{chatID: chatID, text: text, isTask: true, project: project, origin: OriginTelegram, laneKey: "task:" + newTaskID()})
}

// dispatch routes a message into its conversation's lane. A message whose lane
// is idle and can grab a global slot starts immediately; one whose lane is busy
// waits in that lane (its own earlier turn is still running); one blocked only by
// the global cap waits for a slot to free. Turns in the same lane never overlap
// — except an interactive lane (Manager.IsInteractive), where a message that
// arrives while the lane is already running is steered straight into the live
// ConPTY session as a second concurrent turn instead of FIFO-queueing behind
// the first (see runTurn/finishTurn and pendingTurn in
// runner_conpty_windows.go, which resolves each concurrent turn independently
// in submission order). Different lanes always run in parallel up to
// cfg.MaxWorkers at once.
func (b *Bot) dispatch(msg queuedMsg) {
	key := msg.queueKey()
	b.mu.Lock()
	if b.lanes == nil {
		b.lanes = make(map[string]*lane)
	}
	l := b.lanes[key]
	if l == nil {
		l = &lane{interactive: b.manager != nil && b.manager.IsInteractive(msg.conversation())}
		b.lanes[key] = l
	}

	if l.interactive && l.running {
		// Steer into the live session: same lane, same global slot it already
		// holds — no FIFO wait, no new slot consumed. queued=false: this msg
		// never touches l.queue, so finishTurn must not pop l.queue for it.
		l.inFlight++
		b.mu.Unlock()
		go b.runTurn(key, msg, false)
		return
	}

	l.queue = append(l.queue, msg)

	if l.running {
		ahead := len(l.queue) - 1 // turns of THIS conversation still ahead of it
		b.mu.Unlock()
		if !msg.isTask {
			_ = b.ReplyTo(msg.conversation()).Send(msg.chatID, fmt.Sprintf(
				"📋 이 대화가 처리 중입니다 — 앞선 요청 %d건 뒤에 이어서 실행됩니다.", ahead))
		} else {
			log.Printf("[scheduler] 예약 작업: 대화 처리 중 — 뒤에 대기")
		}
		return
	}
	if b.laneQueuedLocked(key) { // idle lane already waiting for a global slot
		b.mu.Unlock()
		return
	}
	if b.runningLanes < b.cfg().MaxWorkers {
		l.running = true
		l.inFlight = 1
		b.runningLanes++
		head := l.queue[0]
		b.mu.Unlock()
		go b.runTurn(key, head, true)
		return
	}
	// Lane is idle but every global slot is taken by another conversation.
	b.readyLanes = append(b.readyLanes, key)
	running, maxW := b.runningLanes, b.cfg().MaxWorkers
	b.mu.Unlock()
	if !msg.isTask {
		_ = b.ReplyTo(msg.conversation()).Send(msg.chatID, fmt.Sprintf(
			"📋 대기열 추가 — 동시 실행 대화 %d/%d. 앞선 대화가 끝나면 시작됩니다.", running, maxW))
	} else {
		log.Printf("[scheduler] 예약 작업 대기열 추가 — 동시 대화 %d/%d", running, maxW)
	}
}

// dispatchLoad reports the current run: how many conversations are executing a
// turn (running) and how many turns are waiting across all lanes (queued —
// same-lane backlogs plus lanes waiting for a global slot).
func (b *Bot) dispatchLoad() (running, queued int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	running = b.runningLanes
	for _, l := range b.lanes {
		q := len(l.queue)
		if l.running && q > 0 {
			q-- // the head is the one running, not waiting
		}
		queued += q
	}
	return running, queued
}

// laneQueuedLocked reports whether key is already waiting in readyLanes. Caller
// holds b.mu.
func (b *Bot) laneQueuedLocked(key string) bool {
	for _, k := range b.readyLanes {
		if k == key {
			return true
		}
	}
	return false
}

// scheduleLocked starts as many waiting lanes as free global slots allow. Caller
// holds b.mu. Skips stale ready entries (a lane drained or already running).
func (b *Bot) scheduleLocked() {
	maxW := b.cfg().MaxWorkers
	for b.runningLanes < maxW && len(b.readyLanes) > 0 {
		key := b.readyLanes[0]
		b.readyLanes = b.readyLanes[1:]
		l := b.lanes[key]
		if l == nil || l.running || len(l.queue) == 0 {
			continue
		}
		l.running = true
		l.inFlight = 1
		b.runningLanes++
		go b.runTurn(key, l.queue[0], true)
	}
}

// finishTurn ends a turn and decides the lane's next step. If the same
// conversation has more queued, that lane keeps its global slot and its next
// turn runs (serialized). Otherwise the lane is freed and a waiting lane is
// promoted into the slot. Returns the same lane's next message, if any.
// queued must be true iff the completing turn came from l.queue[0] (started
// via dispatch's immediate-start branch or scheduleLocked/chaining below) —
// false for a steered interactive turn (dispatch's interactive-branch,
// spawned directly without ever touching l.queue). Getting this wrong pops a
// queue entry that hasn't run yet (queued=true for a steered turn) or leaves
// a finished head stuck at the front of the queue forever (queued=false for
// a real queue turn).
//
// Interactive lanes can still accumulate a backlog in l.queue: any message
// that arrives before the lane's first turn actually starts (e.g. still
// waiting for a free global slot) goes through the ordinary append-to-queue
// path in dispatch, same as a non-interactive lane — only messages arriving
// after l.running is already true steer directly and skip l.queue entirely.
// So finishTurn still has to drain l.queue (chaining l.queue[0] like the
// non-interactive path) before it is safe to free the lane; it must not
// unconditionally delete the lane the moment inFlight hits 0, or a queued
// backlog message is silently dropped with no reply and no error.
func (b *Bot) finishTurn(key string, wid int, queued bool) (queuedMsg, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.cancels, wid)
	l := b.lanes[key]
	if l == nil {
		return queuedMsg{}, false
	}
	if l.interactive {
		l.inFlight--
		if queued && len(l.queue) > 0 {
			l.queue = l.queue[1:] // pop the completed FIFO head
		}
		if l.inFlight > 0 {
			return queuedMsg{}, false // other steered turns still in flight
		}
		if len(l.queue) > 0 {
			l.inFlight = 1
			return l.queue[0], true // same lane, same slot — chain the backlog
		}
		l.running = false
		delete(b.lanes, key)
		b.runningLanes--
		b.scheduleLocked()
		return queuedMsg{}, false
	}
	if len(l.queue) > 0 {
		l.queue = l.queue[1:] // pop the completed head
	}
	if len(l.queue) > 0 {
		return l.queue[0], true // same lane, same slot — next turn in order
	}
	// Lane drained: release the slot and hand it to a waiting conversation.
	l.running = false
	delete(b.lanes, key)
	b.runningLanes--
	b.scheduleLocked()
	return queuedMsg{}, false
}

// runTurn runs one turn of a lane, then either continues that lane or lets its
// slot pass to a waiting conversation. queued must match how msg was
// dispatched — see finishTurn's doc comment.
func (b *Bot) runTurn(key string, msg queuedMsg, queued bool) {
	// A plain WithTimeout deadline is immutable, so instead we cancel manually
	// from a resettable timer: this lets !timeout push the deadline out (or pull
	// it back to the base) for just this one running turn. The timer re-checks
	// the current deadline when it fires, so a deadline moved out from under it
	// (by an extend racing the fire) re-arms instead of killing live work.
	b.mu.Lock()
	b.workerSeq++
	wid := b.workerSeq
	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	base := time.Duration(b.cfg().TimeoutMinutes) * time.Minute
	entry := &cancelEntry{key: key, cancel: cancel, start: start, deadline: start.Add(base)}
	entry.timer = time.AfterFunc(base, func() {
		b.mu.Lock()
		if time.Now().Before(entry.deadline) {
			// Deadline was extended after this timer was armed — reschedule.
			entry.timer.Reset(time.Until(entry.deadline))
			b.mu.Unlock()
			return
		}
		entry.timedOut = true
		b.mu.Unlock()
		cancel()
	})
	b.cancels[wid] = entry
	b.mu.Unlock()

	defer func() {
		if r := recover(); r != nil {
			log.Printf("[bot] panic recovered (wid=%d): %v", wid, r)
			_ = b.ReplyTo(msg.conversation()).Send(msg.chatID, "⚠️ 내부 오류가 발생했습니다.")
		}
		entry.timer.Stop()
		cancel()
		next, ok := b.finishTurn(key, wid, queued)
		if ok {
			if !next.isTask {
				_ = b.ReplyTo(next.conversation()).Send(next.chatID, "▶️ 대기 중이던 요청을 시작합니다.")
			}
			go b.runTurn(key, next, true)
		}
	}()

	if msg.isTask {
		b.manager.HandleScheduledTask(ctx, msg.chatID, msg.text, msg.project, b)
	} else if msg.target != nil {
		b.manager.HandleWebTarget(ctx, msg.chatID, msg.text, *msg.target, b)
	} else {
		b.manager.Handle(ctx, msg.chatID, msg.text, msg.origin, b)
	}

	// The timer cancels the same ctx as !cancel, so ctx.Err() alone can't tell a
	// deadline from a manual cancel — entry.timedOut disambiguates. The reported
	// limit is the effective total (base plus any !timeout extension), not the
	// raw config value.
	b.mu.Lock()
	timedOut := entry.timedOut
	effMinutes := int(entry.deadline.Sub(entry.start).Round(time.Minute) / time.Minute)
	b.mu.Unlock()
	switch {
	case timedOut:
		_ = b.ReplyTo(msg.conversation()).Send(msg.chatID, fmt.Sprintf(
			"⏱ 제한 시간(%d분)을 초과해 작업을 중단했습니다 — 죽은 게 아니라 시간 안에 못 끝낸 것입니다. 다시 메시지를 보내시면 이어서 진행됩니다(같은 대화 세션이라 지금까지 맥락은 유지됩니다).",
			effMinutes))
	case ctx.Err() == context.Canceled:
		_ = b.ReplyTo(msg.conversation()).Send(msg.chatID, "🛑 작업이 취소되었습니다.")
	}
}

// handleCommand processes commands synchronously. Serialized via cmdMu so
// concurrent callers (web-chat's per-message reader goroutines) can never run
// two commands — e.g. two overlapping !update self-rebuilds — at once.
// handleCommand runs a "!" command. origin ("telegram"|"web") is the channel it
// came from; it is forwarded to handlers that create conversations (handleChat's
// "!chat new", handleParallel) so those get tagged with the right origin.
//
// tgt names the conversation the command was sent from, and every reply goes
// back through a sender bound to it instead of b.Send, which always addresses
// the telegram stream. A command issued in a web topic used to answer in
// Telegram: the answer must return only to whoever asked.
// Done is signalled on every exit path. The web client shows its working
// indicator the moment it sends anything — including a "!" command — and only a
// Done clears it. Commands never ran a worker, so nothing used to clear it and
// the indicator span forever behind an answer that had already arrived.
func (b *Bot) handleCommand(chatID int64, text, origin string, tgt Target) {
	b.cmdMu.Lock()
	defer b.cmdMu.Unlock()
	if b.out != nil {
		defer b.ReplyTo(tgt).Done(chatID)
	}
	if b.commandHook != nil {
		b.commandHook(chatID, text)
		return
	}
	reply := b.ReplyTo(tgt)
	fields := strings.Fields(text)
	switch fields[0] {
	case "!start", "!help":
		_ = reply.Send(chatID, helpText())
	case "!cancel":
		b.cancel(reply, chatID, laneKeyOf(tgt))
	case "!timeout":
		b.handleTimeout(reply, chatID, fields, laneKeyOf(tgt))
	case "!status":
		workers := b.manager.DescribeActiveWorkers()
		var msg string
		if workers == "실행 중인 작업 없음" {
			msg = b.manager.describeActive()
		} else {
			msg = workers + "\n" + b.manager.describeActive()
		}
		active, qLen := b.dispatchLoad()
		if active > 0 {
			msg += fmt.Sprintf("\n⚡ 동시 실행 대화: %d/%d", active, b.cfg().MaxWorkers)
		}
		if qLen > 0 {
			msg += fmt.Sprintf("\n📋 대기 중: %d개", qLen)
		}
		msg += "\n🔧 백엔드: " + strings.ToUpper(b.manager.Backend())
		_ = reply.Send(chatID, msg)
	case "!project":
		b.handleProject(reply, chatID, text, fields)
	case "!chat":
		b.handleChat(reply, chatID, text, fields, origin)
	case "!update":
		if active, _ := b.dispatchLoad(); active > 0 {
			_ = reply.Send(chatID, "⏳ 작업 중에는 업데이트할 수 없습니다. !cancel 후 다시 시도하세요.")
			return
		}
		b.handleUpdate(reply, chatID)
	case "!task":
		b.handleTask(reply, chatID, text, fields)
	case "!remind":
		b.handleRemind(reply, chatID, text, fields)
	case "!cron":
		b.handleCron(reply, chatID, text, fields)
	case "!history":
		b.handleHistory(reply, chatID, fields)
	case "!backend":
		b.handleBackend(reply, chatID, fields)
	case "!interactive":
		b.handleInteractive(reply, chatID, fields, tgt)
	case "!user":
		b.handleUser(reply, chatID, fields)
	case "!parallel":
		b.handleParallel(reply, chatID, text, origin)
	case "!screen":
		b.handleScreen(reply, chatID, fields)
	case "!ssh":
		b.handleSSH(reply, chatID, fields)
	case "!compact":
		if active, _ := b.dispatchLoad(); active > 0 {
			_ = reply.Send(chatID, "⏳ 작업 중에는 압축할 수 없습니다. !cancel 후 다시 시도하세요.")
			return
		}
		b.manager.CompactTelegramConversation(context.Background(), chatID, b)
	default:
		_ = reply.Send(chatID, "알 수 없는 명령입니다. !help 를 참고하세요.")
	}
}

// handleScreen runs a direct !screen subcommand, bypassing LLM routing/worker
// for fast deterministic screen control. Shells out to the standalone
// aglink-screen binary's `cmd` fast-path (see resolveScreenBinaryPath) and
// parses its JSON {"text","image","error"} result.
//
//	!screen list                  visible windows
//	!screen shot [창이름]          screenshot (cropped to a window, or full screen)
//	!screen preset save <이름>     save current cursor position
//	!screen click <프리셋이름>      click a saved preset (no LLM)
func (b *Bot) handleScreen(reply replySender, chatID int64, fields []string) {
	if len(fields) < 2 {
		_ = reply.Send(chatID, "사용법: !screen list | !screen shot [창이름] | !screen region <x> <y> <너비> <높이> [창이름] | !screen preset save <이름> | !screen click <프리셋이름>")
		return
	}
	// Honor the yaml switch: screen_control.enabled gates the MCP server for
	// worker turns (pluginWorkerArgs / codexScreenArgs) and must gate this
	// fast-path too, or !screen would drive the screen from a config that says
	// screen control is off.
	if !b.cfg().ScreenControl {
		_ = reply.Send(chatID, "❌ 화면제어가 비활성화되어 있습니다. config.yaml의 screen_control.enabled를 true로 설정하세요.")
		return
	}
	selfExe, _ := os.Executable()
	screenBin := resolveScreenBinaryPath(b.cfg(), selfExe)
	if screenBin == "" {
		_ = reply.Send(chatID, "❌ 화면제어 실행파일(aglink-screen)을 찾을 수 없습니다. screen_control.binary_path를 설정하세요.")
		return
	}

	cmdArgs := []string{"cmd"}
	if presetsPath := b.cfg().ScreenPresetsFile; presetsPath != "" {
		cmdArgs = append(cmdArgs, "--presets", presetsPath)
	}
	cmdArgs = append(cmdArgs, fields[1])
	cmdArgs = append(cmdArgs, fields[2:]...)

	// Bound the external call: !screen runs synchronously in the command-dispatch
	// path, so a hung aglink-screen would otherwise freeze message processing.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, screenBin, cmdArgs...).Output()

	// aglink-screen prints its {"text","image","error"} JSON to stdout even when it
	// exits non-zero on a command error, so parse stdout FIRST and surface that
	// error; fall back to the raw exec error only when there is no usable JSON
	// (binary missing, crash, timeout).
	var res struct {
		Text  string `json:"text"`
		Image string `json:"image"`
		Error string `json:"error"`
	}
	parsed := json.Unmarshal(out, &res) == nil

	if err != nil && !parsed {
		if ctx.Err() == context.DeadlineExceeded {
			_ = reply.Send(chatID, "❌ aglink-screen 응답 시간 초과 (30초)")
			return
		}
		msg := err.Error()
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			msg += ": " + strings.TrimSpace(string(ee.Stderr))
		}
		_ = reply.Send(chatID, "❌ aglink-screen 실행 실패: "+msg)
		return
	}
	if !parsed {
		_ = reply.Send(chatID, "❌ aglink-screen 응답 파싱 실패")
		return
	}
	if res.Error != "" {
		_ = reply.Send(chatID, "❌ "+res.Error)
		return
	}
	if res.Image != "" {
		img, derr := base64.StdEncoding.DecodeString(res.Image)
		if derr != nil {
			_ = reply.Send(chatID, "❌ 이미지 디코드 실패: "+derr.Error())
			return
		}
		if perr := reply.SendPhoto(chatID, img, res.Text); perr != nil {
			// e.g. image exceeds Telegram's photo size limit — tell the user
			// instead of silently dropping the capture.
			_ = reply.Send(chatID, "⚠️ 캡처는 됐지만 이미지 전송 실패: "+perr.Error())
		}
		return
	}
	_ = reply.Send(chatID, res.Text)
}

// handleSSH runs the !ssh command: list registered hosts, or execute a remote
// command over SSH on a named host. Gated by ssh.enabled and the host registry
// (only names in ssh.hosts are reachable), so it never becomes an arbitrary
// outbound SSH client. Runs synchronously in the command-dispatch path with a
// bounded context so a hung connection can't freeze message processing.
func (b *Bot) handleSSH(reply replySender, chatID int64, fields []string) {
	cfg := b.cfg()
	if !cfg.SSHEnabled {
		_ = reply.Send(chatID, "❌ SSH가 비활성화되어 있습니다. 설정에서 'SSH 원격 제어 허용'을 켜세요(config.yaml ssh.enabled=true).")
		return
	}
	if len(fields) < 2 {
		_ = reply.Send(chatID, "사용법: !ssh list | !ssh <호스트> <명령...> | !ssh test <호스트>")
		return
	}
	switch fields[1] {
	case "list":
		if len(cfg.SSHHosts) == 0 {
			_ = reply.Send(chatID, "등록된 SSH 호스트가 없습니다. 원본 설정편집기의 ssh.hosts에 추가하세요.")
			return
		}
		var sb strings.Builder
		sb.WriteString("🔐 등록된 SSH 호스트:\n")
		for _, h := range cfg.SSHHosts {
			port := h.Port
			if port == 0 {
				port = 22
			}
			auth := "키" // default assumption
			if h.KeyFile == "" && h.Password != "" {
				auth = "비밀번호"
			} else if h.KeyFile != "" {
				auth = "키:" + h.KeyFile
			}
			sb.WriteString(fmt.Sprintf("• %s → %s@%s:%d (%s)\n", h.Name, h.User, h.Host, port, auth))
		}
		_ = reply.Send(chatID, strings.TrimRight(sb.String(), "\n"))
		return
	case "test":
		if len(fields) < 3 {
			_ = reply.Send(chatID, "사용법: !ssh test <호스트>")
			return
		}
		b.runSSHReply(reply, chatID, fields[2], "echo aglink-ssh-ok")
		return
	default:
		host := fields[1]
		remote := strings.TrimSpace(strings.Join(fields[2:], " "))
		if remote == "" {
			_ = reply.Send(chatID, "실행할 원격 명령이 없습니다. 예: !ssh "+host+" uptime")
			return
		}
		b.runSSHReply(reply, chatID, host, remote)
	}
}

// runSSHReply executes one remote command and sends its output back, capping the
// reply length so a chatty command can't blow past Telegram's message limit.
func (b *Bot) runSSHReply(reply replySender, chatID int64, host, remote string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := runSSH(ctx, b.cfg(), host, remote)
	out = strings.TrimRight(out, "\n")
	const maxLen = 3500
	if len(out) > maxLen {
		out = out[:maxLen] + "\n…(생략)"
	}
	if err != nil {
		msg := "❌ SSH 실패: " + err.Error()
		if out != "" {
			msg += "\n" + out
		}
		_ = reply.Send(chatID, msg)
		return
	}
	if out == "" {
		out = "(출력 없음)"
	}
	_ = reply.Send(chatID, "🔐 "+host+":\n"+out)
}

// cancel stops only the running worker(s) in the caller's own lane (key),
// leaving other conversations' workers untouched. A lane runs its turns
// strictly one at a time, so at most one entry ever matches, but we scan
// defensively rather than assume that invariant here.
func (b *Bot) cancel(reply replySender, chatID int64, key string) {
	b.mu.Lock()
	fns := make([]context.CancelFunc, 0, 1)
	for _, e := range b.cancels {
		if e.key == key {
			fns = append(fns, e.cancel)
		}
	}
	b.mu.Unlock()
	if len(fns) == 0 {
		_ = reply.Send(chatID, "취소할 작업이 없습니다.")
		return
	}
	for _, fn := range fns {
		fn()
	}
	_ = reply.Send(chatID, fmt.Sprintf("🛑 %d개 작업 취소 요청됨.", len(fns)))
}

// timeoutOp describes how !timeout should move a running turn's deadline:
// reset back to the base, set an absolute total, or add a signed delta to the
// current deadline. Exactly one of the three is meaningful per call.
type timeoutOp struct {
	reset    bool          // snap the effective total back to the base
	absolute time.Duration // >0: set the effective total (from turn start) to this
	delta    time.Duration // otherwise: add this (may be negative) to the current deadline
}

// adjustTimeout moves the enforcement deadline of every running worker in the
// caller's own lane (key). The effective total (deadline minus turn start) is
// never allowed below the base TimeoutMinutes — reducing only claws back an
// earlier extension, it can't cut a turn below its configured budget. Already
// timed-out entries are skipped. Returns how many workers were adjusted and the
// resulting effective total (minutes) of the last one, for the reply.
func (b *Bot) adjustTimeout(key string, op timeoutOp) (n, effMinutes int) {
	base := time.Duration(b.cfg().TimeoutMinutes) * time.Minute
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, e := range b.cancels {
		if e.key != key || e.timedOut {
			continue
		}
		var nd time.Time
		switch {
		case op.reset:
			nd = e.start.Add(base)
		case op.absolute > 0:
			nd = e.start.Add(op.absolute)
		default:
			nd = e.deadline.Add(op.delta)
		}
		if floor := e.start.Add(base); nd.Before(floor) {
			nd = floor // 기본 설정 미만으로는 줄이지 않는다
		}
		e.deadline = nd
		rem := time.Until(nd)
		if rem < 0 {
			rem = 0 // deadline already past → fire (and thus time out) immediately
		}
		e.timer.Reset(rem)
		n++
		effMinutes = int(nd.Sub(e.start).Round(time.Minute) / time.Minute)
	}
	return
}

// handleTimeout runs !timeout: adjust the running turn's deadline for the
// caller's lane only. Forms: "+N" / "-N" (분 가감), "reset" (기본값 복귀),
// or a bare "N" (분 절대값). The desktop status-bar dropdown sends the +/-/reset
// forms; a bare number is convenient when typed directly.
func (b *Bot) handleTimeout(reply replySender, chatID int64, fields []string, key string) {
	base := b.cfg().TimeoutMinutes
	usage := fmt.Sprintf("사용법: !timeout +<분> | -<분> | reset  (실행 중인 작업의 제한 시간을 조절, 기본 %d분 미만으로는 줄지 않습니다)", base)
	if len(fields) < 2 {
		_ = reply.Send(chatID, usage)
		return
	}
	arg := strings.TrimSpace(fields[1])
	var op timeoutOp
	switch {
	case arg == "reset" || arg == "base" || arg == "default" || arg == "기본":
		op.reset = true
	case strings.HasPrefix(arg, "+") || strings.HasPrefix(arg, "-"):
		mins, err := strconv.Atoi(arg)
		if err != nil || mins == 0 {
			_ = reply.Send(chatID, "분 단위 정수를 입력하세요. 예) !timeout +10")
			return
		}
		op.delta = time.Duration(mins) * time.Minute
	default:
		mins, err := strconv.Atoi(arg)
		if err != nil || mins <= 0 {
			_ = reply.Send(chatID, "분 단위 정수를 입력하세요. 예) !timeout +10 또는 !timeout 30")
			return
		}
		op.absolute = time.Duration(mins) * time.Minute
	}
	n, eff := b.adjustTimeout(key, op)
	if n == 0 {
		_ = reply.Send(chatID, "조절할 실행 중 작업이 없습니다.")
		return
	}
	_ = reply.Send(chatID, fmt.Sprintf("⏱ 이 작업의 제한 시간을 %d분으로 조절했습니다 (이 작업에만 적용, 기본 설정 %d분은 그대로).", eff, base))
}

// handleProject: !project add <name> <path> | remove <name> | list
func (b *Bot) handleProject(reply replySender, chatID int64, text string, fields []string) {
	if len(fields) < 2 {
		_ = reply.Send(chatID, "사용법: !project add <이름> <경로> | !project remove <이름> | !project list")
		return
	}
	switch fields[1] {
	case "add":
		// SplitN keeps spaces in the Windows path intact: [!project add name path...]
		parts := strings.SplitN(text, " ", 4)
		if len(parts) < 4 {
			_ = reply.Send(chatID, "사용법: !project add <이름> <경로>")
			return
		}
		name, path := parts[2], strings.TrimSpace(parts[3])
		if err := b.store.AddProject(name, path); err != nil {
			_ = reply.Send(chatID, "⚠️ "+err.Error())
			return
		}
		_ = reply.Send(chatID, fmt.Sprintf("✅ 프로젝트 등록: %s → %s", name, path))
	case "remove":
		if len(fields) < 3 {
			_ = reply.Send(chatID, "사용법: !project remove <이름>")
			return
		}
		if err := b.store.RemoveProject(fields[2]); err != nil {
			_ = reply.Send(chatID, "⚠️ "+err.Error())
			return
		}
		_ = reply.Send(chatID, "🗑 프로젝트 제거: "+fields[2])
	case "list":
		_ = reply.Send(chatID, b.formatProjectList())
	default:
		_ = reply.Send(chatID, "사용법: !project add <이름> <경로> | !project remove <이름> | !project list")
	}
}

func (b *Bot) formatProjectList() string {
	projects := b.store.ListProjects()
	if len(projects) == 0 {
		return "등록된 프로젝트가 없습니다. !project add <이름> <경로>"
	}
	active := b.store.GetActive()
	var sb strings.Builder
	sb.WriteString("📂 프로젝트 목록\n")
	for name, p := range projects {
		marker := ""
		if name == active.Project {
			marker = " ⭐"
		}
		fmt.Fprintf(&sb, "\n• %s%s\n  %s\n", name, marker, p.Path)
		if len(p.Conversations) == 0 {
			sb.WriteString("  (대화 없음)\n")
		}
		for _, id := range sortedConvIDs(p.Conversations) {
			c := p.Conversations[id]
			cm := ""
			if name == active.Project && id == active.ConversationID {
				cm = " ⭐"
			}
			fmt.Fprintf(&sb, "  [%s] %s%s\n", id, c.Title, cm)
		}
	}
	return sb.String()
}

// handleChat: !chat new [title] | list | use <id> | use <project> <id>.
// origin tags a "!chat new" conversation with the channel that created it so the
// two channels' chat lists can be managed separately.
func (b *Bot) handleChat(reply replySender, chatID int64, text string, fields []string, origin string) {
	if origin != OriginWeb {
		_ = reply.Send(chatID, "ℹ️ 텔레그램에서는 대화 주제를 관리하지 않습니다. 대화 주제는 웹에서 관리하세요. (텔레그램은 \"이제 <프로젝트명> 하자\"로 작업 대상만 전환)")
		return
	}
	if len(fields) < 2 {
		_ = reply.Send(chatID, "사용법: !chat new [제목] | !chat list | !chat use <id> | !chat use <프로젝트> <id> | !chat rename <새 제목>")
		return
	}
	active := b.store.GetActive()
	switch fields[1] {
	case "new":
		if active.Project == "" {
			_ = reply.Send(chatID, "활성 프로젝트가 없습니다. 먼저 메시지를 보내거나 !project list 후 작업하세요.")
			return
		}
		title := ""
		if parts := strings.SplitN(text, " ", 3); len(parts) == 3 {
			title = strings.TrimSpace(parts[2])
		}
		c, err := b.store.NewConversation(active.Project, title, origin)
		if err != nil {
			_ = reply.Send(chatID, "⚠️ "+err.Error())
			return
		}
		_ = b.store.SetActive(active.Project, c.ID)
		_ = reply.Send(chatID, fmt.Sprintf("🆕 새 대화 [%s] %s (활성화됨)", c.ID, c.Title))
	case "list":
		if active.Project == "" {
			_ = reply.Send(chatID, "활성 프로젝트가 없습니다. 먼저 메시지를 보내거나 !project list 후 작업하세요.")
			return
		}
		_ = reply.Send(chatID, b.formatChatList(active.Project))
	case "use":
		if len(fields) < 3 {
			_ = reply.Send(chatID, "사용법: !chat use <id> | !chat use <프로젝트> <id>")
			return
		}
		project := active.Project
		convID := fields[2]
		if len(fields) >= 4 {
			project = fields[2]
			convID = fields[3]
		}
		if project == "" {
			_ = reply.Send(chatID, "활성 프로젝트가 없습니다. 먼저 메시지를 보내거나 !project list 후 작업하세요.")
			return
		}
		c, ok := b.store.GetConversation(project, convID)
		if !ok {
			_ = reply.Send(chatID, "해당 대화를 찾을 수 없습니다: "+project+"/"+convID)
			return
		}
		_ = b.store.SetActive(project, c.ID)
		_ = reply.Send(chatID, fmt.Sprintf("✅ 대화 전환 [%s] %s", c.ID, c.Title))
	case "rename":
		if active.Project == "" || active.ConversationID == "" {
			_ = reply.Send(chatID, "이름을 바꿀 활성 웹 토픽이 없습니다. 먼저 토픽을 선택하세요.")
			return
		}
		newTitle := ""
		if parts := strings.SplitN(text, " ", 3); len(parts) == 3 {
			newTitle = strings.TrimSpace(parts[2])
		}
		if newTitle == "" {
			_ = reply.Send(chatID, "사용법: !chat rename <새 제목>")
			return
		}
		c, ok := b.store.GetConversation(active.Project, active.ConversationID)
		if !ok {
			_ = reply.Send(chatID, "활성 토픽을 찾을 수 없습니다.")
			return
		}
		c.Title = newTitle
		if err := b.store.UpdateConversation(active.Project, c); err != nil {
			_ = reply.Send(chatID, "⚠️ 이름 변경 실패: "+err.Error())
			return
		}
		_ = reply.Send(chatID, "✏️ 대화 이름을 변경했습니다: "+newTitle)
	default:
		_ = reply.Send(chatID, "사용법: !chat new [제목] | !chat list | !chat use <id> | !chat use <프로젝트> <id> | !chat rename <새 제목>")
	}
}

func (b *Bot) formatChatList(project string) string {
	p, ok := b.store.GetProject(project)
	if !ok {
		return "프로젝트를 찾을 수 없습니다: " + project
	}
	if len(p.Conversations) == 0 {
		return fmt.Sprintf("📂 %s: 대화가 없습니다. !chat new [제목]", project)
	}
	active := b.store.GetActive()
	var sb strings.Builder
	fmt.Fprintf(&sb, "💬 %s 대화 목록\n", project)
	shown := 0
	for _, id := range sortedConvIDs(p.Conversations) {
		c := p.Conversations[id]
		// Conversations explicitly created from the web UI are managed there only;
		// keep them out of the Telegram chat list (continuations inherit the origin).
		if c.Origin == OriginWeb {
			continue
		}
		cm := ""
		if id == active.ConversationID {
			cm = " ⭐"
		}
		line := fmt.Sprintf("[%s] %s%s", id, c.Title, cm)
		if c.Summary != "" {
			line += " — " + c.Summary
		}
		sb.WriteString(line + "\n")
		shown++
	}
	if shown == 0 {
		return fmt.Sprintf("📂 %s: 대화가 없습니다. !chat new [제목]", project)
	}
	return sb.String()
}

// handleUpdate builds aglink_new.exe, starts it, waits for it to connect to
// Telegram, then hands over: old process exits cleanly, new process renames itself.
// Works without launcher.ps1 — zero downtime.
func (b *Bot) handleUpdate(reply replySender, chatID int64) {
	_ = reply.Send(chatID, "🔨 빌드 시작...")

	exe, err := os.Executable()
	if err != nil {
		_ = reply.Send(chatID, "⚠️ 실행 파일 경로 확인 실패: "+err.Error())
		return
	}
	srcDir := filepath.Dir(exe)
	newExe := filepath.Join(srcDir, "aglink_new"+exeSuffix)
	readyFile := filepath.Join(os.TempDir(), fmt.Sprintf(".aglink_ready_%d", os.Getpid()))

	// Verify source code exists in srcDir (fix: exe copied to different dir would silently fail)
	if _, serr := os.Stat(filepath.Join(srcDir, "main.go")); serr != nil {
		_ = reply.Send(chatID, "⚠️ 소스 코드를 찾을 수 없습니다 ("+srcDir+")\nexe와 소스 코드가 같은 디렉터리에 있어야 !update가 작동합니다.")
		return
	}

	// If we're already running as aglink_new.exe, the self-rename from the previous
	// handoff hasn't completed yet (or failed). aglink_new.exe is our own exe file,
	// so go build cannot overwrite it. Abort and instruct the user.
	if filepath.Base(exe) == "aglink_new"+exeSuffix {
		if _, serr := os.Stat(newExe); serr == nil {
			_ = reply.Send(chatID, "⚠️ 이전 핸드오프의 이름 변경이 아직 완료되지 않았습니다.\n잠시 후 다시 시도하거나 aglink_new를 aglink로 수동 교체 후 재시작하세요.")
			return
		}
	}

	// Plugins first (fail fast — don't touch aglink if a sibling plugin's
	// source is broken). Skips silently on deployments that don't have
	// aglink-screen/aglink-web checked out next to aglink.
	pluginReport, perr := updatePlugins(srcDir)
	if perr != nil {
		_ = reply.Send(chatID, "⚠️ "+perr.Error())
		return
	}
	if len(pluginReport) > 0 {
		_ = reply.Send(chatID, "🔌 플러그인 갱신됨: "+strings.Join(pluginReport, ", "))
	}

	// Build
	buildCtx, buildCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer buildCancel()
	// Stamp the new binary's version (v<major>.<minor>.<commit-count> + hash +
	// time). Best-effort: git absent / not a repo → omit ldflags and the binary
	// renders v<major>.<minor>.dev.
	buildArgs := []string{"build", "-o", newExe}
	if count := gitCommitCount(srcDir); count != "" {
		buildArgs = append(buildArgs, "-ldflags",
			"-X main.buildCommitCount="+count+
				" -X main.buildCommit="+gitShortCommit(srcDir)+
				" -X main.buildTime="+time.Now().UTC().Format(time.RFC3339))
	}
	buildArgs = append(buildArgs, ".")
	buildCmd := exec.CommandContext(buildCtx, "go", buildArgs...)
	buildCmd.Dir = srcDir
	if out, berr := buildCmd.CombinedOutput(); berr != nil {
		_ = reply.Send(chatID, "⚠️ 빌드 실패:\n"+strings.TrimSpace(string(out)))
		return
	}

	_ = reply.Send(chatID, "✅ 빌드 성공! 새 버전 연결 중...")
	_ = os.Remove(readyFile)

	// Start new process — passes readyFile + chatID so it can signal and notify via Telegram
	newProc := exec.Command(newExe, "run",
		"--handoff-ready", readyFile,
		"--notify-chat", fmt.Sprintf("%d", chatID),
	)
	if err := newProc.Start(); err != nil {
		_ = reply.Send(chatID, "⚠️ 새 버전 시작 실패: "+err.Error())
		return
	}

	// Wait up to 60s for new process to signal Telegram connection.
	// 60s: claude health check is up to 20s, bot init adds more time.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		if _, serr := os.Stat(readyFile); serr == nil {
			_ = os.Remove(readyFile)
			_ = reply.Send(chatID, "🔄 새 버전 연결됨! 전환합니다...")
			log.Println("[bot] handoff: new instance ready, exiting")
			b.manager.CloseInteractive()
			os.Exit(0)
		}
	}

	// Timeout — kill new process, keep current running
	_ = newProc.Process.Kill()
	_ = reply.Send(chatID, "⚠️ 새 버전 연결 대기 시간 초과 (60초). 이전 버전 계속 사용합니다.")
}

// handleRemind processes !remind commands.
// Usage:
//
//	!remind 30m 배포 확인           — 30분 후 알림
//	!remind 2h task 서버 확인해줘   — 2시간 후 Claude 작업
//	!remind list                    — 대기 중 목록
//	!remind cancel <id>             — 취소
func (b *Bot) handleRemind(reply replySender, chatID int64, _ string, fields []string) {
	if len(fields) < 2 {
		_ = reply.Send(chatID, "사용법: !remind <시간> [task] <메시지>  |  !remind list  |  !remind cancel <id>\n시간 예) 30m, 2h, 1d — 단위: m(분) h(시간) d(일)\n예) !remind 30m 배포 확인, !remind 2h task 서버 상태 확인해줘")
		return
	}
	switch fields[1] {
	case "list":
		reminders := b.scheduler.ListReminders()
		if len(reminders) == 0 {
			_ = reply.Send(chatID, "대기 중인 알림이 없습니다.")
			return
		}
		var sb strings.Builder
		sb.WriteString("⏰ 대기 중인 알림:\n")
		for _, r := range reminders {
			remaining := time.Until(r.FireAt)
			var timeStr string
			if remaining < 0 {
				timeStr = "즉시 실행 예정"
			} else {
				timeStr = remaining.Round(time.Second).String() + " 후"
			}
			fmt.Fprintf(&sb, "[%s] %s — %s\n", r.ID, timeStr, r.Prompt)
		}
		_ = reply.Send(chatID, sb.String())
	case "cancel":
		if len(fields) < 3 {
			_ = reply.Send(chatID, "사용법: !remind cancel <id>")
			return
		}
		if b.scheduler.Remove(fields[2]) {
			_ = reply.Send(chatID, "✅ 알림 취소됨: "+fields[2])
		} else {
			_ = reply.Send(chatID, "⚠️ 알림을 찾을 수 없습니다: "+fields[2])
		}
	default:
		// !remind <duration> [task] <message>
		dur, _, err := ParseSchedule(fields[1])
		if err != nil {
			_ = reply.Send(chatID, "⚠️ 시간 형식 오류: "+err.Error())
			return
		}
		isTask := len(fields) > 2 && fields[2] == "task"
		msgStart := 2
		if isTask {
			msgStart = 3
		}
		if msgStart >= len(fields) {
			_ = reply.Send(chatID, "⚠️ 메시지를 입력해주세요.")
			return
		}
		msg := strings.Join(fields[msgStart:], " ")
		fireAt := time.Now().Add(dur)
		t := &Task{
			ID:        newTaskID(),
			ChatID:    chatID,
			Prompt:    msg,
			FireAt:    fireAt,
			Status:    "pending",
			IsTask:    isTask,
			Label:     "알림: " + msg,
			CreatedAt: time.Now(),
		}
		if err := b.scheduler.AddTask(t); err != nil {
			_ = reply.Send(chatID, "⚠️ 알림 등록 실패: "+err.Error())
			return
		}
		kind := "알림"
		if isTask {
			kind = "Claude 작업"
		}
		_ = reply.Send(chatID, fmt.Sprintf("✅ 알림 등록 [%s] — %s 후 (%s): %s", t.ID, humanDelay(dur), kind, msg))
	}
}

// handleCron processes !cron commands.
// Usage:
//
//	!cron add <schedule> <메시지>          — 반복 알림
//	!cron add <schedule> task <프롬프트>   — 반복 Claude 작업
//	!cron list                             — 목록
//	!cron remove <id>                      — 제거
func (b *Bot) handleCron(reply replySender, chatID int64, _ string, fields []string) {
	if len(fields) < 2 {
		_ = reply.Send(chatID, "사용법: !cron add <주기> [task] <내용>  |  !cron list  |  !cron remove <id>\n주기 예) 30m, 2h, 1d, 1w, 매시간, 매일, 매주\n예) !cron add 1h 서버 상태 확인, !cron add 매일 task 오늘의 작업 요약해줘")
		return
	}
	switch fields[1] {
	case "list":
		crons := b.scheduler.ListCrons()
		if len(crons) == 0 {
			_ = reply.Send(chatID, "등록된 크론 작업이 없습니다.")
			return
		}
		var sb strings.Builder
		sb.WriteString("🔔 크론 작업 목록:\n")
		for _, c := range crons {
			kind := "알림"
			if c.IsTask {
				kind = "작업"
			}
			nextAt := b.scheduler.NextFire(c.ID)
			var nextStr string
			if nextAt.IsZero() {
				nextStr = "계산 중..."
			} else {
				remaining := time.Until(nextAt)
				if remaining < 0 {
					nextStr = "즉시 실행 예정"
				} else {
					nextStr = remaining.Round(time.Second).String() + " 후"
				}
			}
			fmt.Fprintf(&sb, "[%s] %s (%s) — 다음: %s\n  %s\n", c.ID, c.Label, kind, nextStr, c.Prompt)
		}
		_ = reply.Send(chatID, sb.String())
	case "remove":
		if len(fields) < 3 {
			_ = reply.Send(chatID, "사용법: !cron remove <id>")
			return
		}
		if b.scheduler.Remove(fields[2]) {
			_ = reply.Send(chatID, "✅ 크론 제거됨: "+fields[2])
		} else {
			_ = reply.Send(chatID, "⚠️ 크론을 찾을 수 없습니다: "+fields[2])
		}
	case "add":
		if len(fields) < 4 {
			_ = reply.Send(chatID, "사용법: !cron add <주기> [task] <내용>")
			return
		}
		dur, label, err := ParseSchedule(fields[2])
		if err != nil {
			_ = reply.Send(chatID, "⚠️ 주기 형식 오류: "+err.Error())
			return
		}
		isTask := fields[3] == "task"
		msgStart := 3
		if isTask {
			msgStart = 4
		}
		if msgStart >= len(fields) {
			_ = reply.Send(chatID, "⚠️ 내용을 입력해주세요.")
			return
		}
		task := strings.Join(fields[msgStart:], " ")
		c, err := b.scheduler.AddCron(chatID, b.store.TelegramActiveProject(), label, dur, task, isTask)
		if err != nil {
			_ = reply.Send(chatID, "⚠️ 크론 등록 실패: "+err.Error())
			return
		}
		kind := "알림"
		if isTask {
			kind = "Claude 작업"
		}
		_ = reply.Send(chatID, fmt.Sprintf("✅ 크론 등록 [%s] %s (%s)\n  내용: %s", c.ID, label, kind, task))
	default:
		_ = reply.Send(chatID, "사용법: !cron add | list | remove")
	}
}

// handleTask processes !task commands — the unified scheduling interface.
//
// Subcommands:
//
//	!task add <cron|duration> [task] <prompt>
//	!task add <cron|duration> --script <script> [task] <prompt>
//	!task once <HH:MM|YYYY-MM-DD HH:MM> <message>
//	!task list [pending|paused|all]
//	!task pause|resume|cancel <id>
//	!task update <id> [--cron <expr>] [--prompt <text>] [--script <script>]
func (b *Bot) handleTask(reply replySender, chatID int64, _ string, fields []string) {
	if len(fields) < 2 {
		_ = reply.Send(chatID, taskHelpText())
		return
	}
	switch fields[1] {
	case "help":
		_ = reply.Send(chatID, taskHelpText())

	case "list":
		filter := "pending"
		if len(fields) >= 3 {
			filter = fields[2]
		}
		tasks := b.scheduler.ListTasks(filter)
		if len(tasks) == 0 {
			_ = reply.Send(chatID, "등록된 작업이 없습니다. (필터: "+filter+")")
			return
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "📋 작업 목록 (%s):\n", filter)
		for _, t := range tasks {
			kind := "알림"
			if t.IsTask {
				kind = "작업"
			}
			schedule := t.CronExpr
			if schedule == "" {
				schedule = "일회성 " + t.FireAt.Format("2006-01-02 15:04")
			}
			next := b.scheduler.NextFire(t.ID)
			nextStr := ""
			if !next.IsZero() {
				remaining := time.Until(next)
				if remaining < 0 {
					nextStr = " → 즉시 실행 예정"
				} else {
					nextStr = fmt.Sprintf(" → %s 후", remaining.Round(time.Second))
				}
			} else if t.Status == "pending" && !t.FireAt.IsZero() {
				remaining := time.Until(t.FireAt)
				if remaining < 0 {
					nextStr = " → 즉시 실행 예정"
				} else {
					nextStr = fmt.Sprintf(" → %s 후", remaining.Round(time.Second))
				}
			}
			scriptMark := ""
			if t.Script != "" {
				scriptMark = " [스크립트]"
			}
			// Show full prompt line only when it's truncated in the label (>30 runes).
			promptSuffix := ""
			if len([]rune(t.Prompt)) > 30 {
				promptSuffix = "\n  ▶ " + truncate(t.Prompt, 80)
			}
			fmt.Fprintf(&sb, "[%s] %s (%s/%s)%s\n  %s%s%s\n",
				t.ID, t.Label, t.Status, kind, scriptMark, schedule, nextStr, promptSuffix)
		}
		_ = reply.Send(chatID, sb.String())

	case "pause":
		if len(fields) < 3 {
			_ = reply.Send(chatID, "사용법: !task pause <id>")
			return
		}
		if err := b.scheduler.PauseTask(fields[2]); err != nil {
			_ = reply.Send(chatID, "⚠️ "+err.Error())
		} else {
			_ = reply.Send(chatID, "⏸ 작업 일시정지됨: "+fields[2])
		}

	case "resume":
		if len(fields) < 3 {
			_ = reply.Send(chatID, "사용법: !task resume <id>")
			return
		}
		if err := b.scheduler.ResumeTask(fields[2]); err != nil {
			_ = reply.Send(chatID, "⚠️ "+err.Error())
		} else {
			_ = reply.Send(chatID, "▶️ 작업 재개됨: "+fields[2])
		}

	case "cancel":
		if len(fields) < 3 {
			_ = reply.Send(chatID, "사용법: !task cancel <id>")
			return
		}
		if err := b.scheduler.CancelTask(fields[2]); err != nil {
			_ = reply.Send(chatID, "⚠️ "+err.Error())
		} else {
			_ = reply.Send(chatID, "✅ 작업 취소됨: "+fields[2])
		}

	case "update":
		// !task update <id> [--cron <expr>] [--prompt <text>] [--script <script>] [--depends-on <id,...>]
		if len(fields) < 3 {
			_ = reply.Send(chatID, "사용법: !task update <id> [--cron <식>] [--prompt <텍스트>] [--script <스크립트|clear>] [--depends-on <id,...|none>]")
			return
		}
		id := fields[2]
		cronExpr, prompt, script, depsRaw := parseFlags4(fields[3:], "--cron", "--prompt", "--script", "--depends-on")
		if cronExpr == "" && prompt == "" && script == "" && depsRaw == "" {
			_ = reply.Send(chatID, "⚠️ 변경할 항목을 지정하세요.\n사용법: !task update <id> [--cron <식>] [--prompt <텍스트>] [--script <스크립트|clear>] [--depends-on <id,...|none>]")
			return
		}
		// Sentinels: "--script clear" removes the script; "--depends-on none" clears deps.
		if script == "clear" {
			script = "\x00" // marker passed to UpdateTask to clear
		} else if script != "" {
			if verr := validateScript(b.cfg(), script); verr != nil {
				_ = reply.Send(chatID, "⚠️ 스크립트 거부: "+verr.Error())
				return
			}
		}
		var deps []string
		if depsRaw == "none" {
			deps = []string{} // explicit clear
		} else if depsRaw != "" {
			deps = parseDependsOn(depsRaw)
		}
		if err := b.scheduler.UpdateTask(id, cronExpr, prompt, script, deps); err != nil {
			_ = reply.Send(chatID, "⚠️ "+err.Error())
		} else {
			_ = reply.Send(chatID, "✅ 작업 업데이트됨: "+id)
		}

	case "once":
		// !task once <HH:MM|YYYY-MM-DD HH:MM> <message>
		if len(fields) < 4 {
			_ = reply.Send(chatID, "사용법: !task once <HH:MM|YYYY-MM-DD HH:MM> <메시지>")
			return
		}
		fireAt, msgStart, err := parseOnceDatetime(fields[2:])
		if err != nil {
			_ = reply.Send(chatID, "⚠️ 시각 형식 오류: "+err.Error())
			return
		}
		msg := strings.Join(fields[2+msgStart:], " ")
		if msg == "" {
			_ = reply.Send(chatID, "⚠️ 메시지를 입력해주세요.")
			return
		}
		t, err := b.scheduler.AddReminder(chatID, b.store.TelegramActiveProject(), msg, fireAt)
		if err != nil {
			_ = reply.Send(chatID, "⚠️ 등록 실패: "+err.Error())
			return
		}
		dayLabel := ""
		now := time.Now()
		if fireAt.Year() == now.Year() && fireAt.YearDay() == now.YearDay() {
			dayLabel = " (오늘)"
		} else if fireAt.Sub(now) < 48*time.Hour {
			dayLabel = " (내일)"
		}
		_ = reply.Send(chatID, fmt.Sprintf("✅ 일회성 등록 [%s] — %s%s에 실행\n  %s",
			t.ID, fireAt.Format("2006-01-02 15:04"), dayLabel, msg))

	case "add":
		// !task add <cron|duration> [--script <script>] [task] <prompt>
		if len(fields) < 4 {
			_ = reply.Send(chatID, "사용법: !task add <주기> [task] <프롬프트>\n주기: 30m, 2h, 1d, 1w, 매시간, 매일, 매주, 또는 5-field cron\n예) !task add 매일 task 오늘 요약해줘\n    !task add 0 9 * * 1-5 task 주식 확인")
			return
		}
		cronExpr, script, dependsOn, isTask, prompt, err := parseTaskAddArgs(fields[2:])
		if err != nil {
			_ = reply.Send(chatID, "⚠️ "+err.Error())
			return
		}
		if script != "" {
			if verr := validateScript(b.cfg(), script); verr != nil {
				_ = reply.Send(chatID, "⚠️ 스크립트 거부: "+verr.Error())
				return
			}
		}
		kind := "알림"
		if isTask {
			kind = "Claude 작업"
		}
		t := &Task{
			ID:        newTaskID(),
			ChatID:    chatID,
			Prompt:    prompt,
			Script:    script,
			CronExpr:  cronExpr,
			DependsOn: dependsOn,
			Status:    "pending",
			IsTask:    isTask,
			Label:     truncate(prompt, 30),
			CreatedAt: time.Now(),
		}
		if err := b.scheduler.AddTask(t); err != nil {
			_ = reply.Send(chatID, "⚠️ 등록 실패: "+err.Error())
			return
		}
		scriptNote := ""
		if script != "" {
			scriptNote = " [스크립트 사전확인 있음]"
		}
		_ = reply.Send(chatID, fmt.Sprintf("✅ 작업 등록 [%s] %s (%s)%s\n  %s",
			t.ID, cronExpr, kind, scriptNote, prompt))

	default:
		_ = reply.Send(chatID, "알 수 없는 !task 하위 명령. !task help 참조")
	}
}

// parseTaskAddArgs parses fields after "!task add".
// Returns (cronExpr, script, dependsOn, isTask, prompt, error).
// Supports: 5-field cron tokens, duration shorthand, --script, --depends-on flags, task keyword.
func parseTaskAddArgs(args []string) (cronExpr, script string, dependsOn []string, isTask bool, prompt string, err error) {
	if len(args) == 0 {
		return "", "", nil, false, "", fmt.Errorf("인수가 부족합니다")
	}

	// Extract --script and --depends-on flags from args before parsing cron/prompt
	var rest []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--script":
			if i+1 >= len(args) {
				return "", "", nil, false, "", fmt.Errorf("--script 플래그에 값이 필요합니다")
			}
			script = args[i+1]
			i++
		case "--depends-on":
			if i+1 >= len(args) {
				return "", "", nil, false, "", fmt.Errorf("--depends-on 플래그에 값이 필요합니다")
			}
			dependsOn = parseDependsOn(args[i+1])
			i++
		default:
			rest = append(rest, args[i])
		}
	}
	args = rest

	if len(args) == 0 {
		return "", "", nil, false, "", fmt.Errorf("cron 식 또는 주기를 입력하세요")
	}

	// Determine cron expression
	var cronEnd int
	if len(args) >= 5 && allCronFields(args[0:5]) {
		// 5-field cron: "0 9 * * 1-5"
		cronExpr = strings.Join(args[0:5], " ")
		cronEnd = 5
	} else if args[0] == "@every" && len(args) >= 2 {
		cronExpr = "@every " + args[1]
		cronEnd = 2
	} else {
		// Duration shorthand: 30m, 2h, daily, etc.
		dur, _, pErr := ParseSchedule(args[0])
		if pErr != nil {
			return "", "", nil, false, "", fmt.Errorf("주기 형식 오류 (%q): %v\n예) 30m, 2h, daily, 또는 5-field cron (0 9 * * 1-5)", args[0], pErr)
		}
		cronExpr = durationToCron(dur)
		cronEnd = 1
	}

	remaining := args[cronEnd:]
	if len(remaining) == 0 {
		return "", "", nil, false, "", fmt.Errorf("프롬프트가 없습니다")
	}

	// Optional "task" keyword
	if remaining[0] == "task" {
		isTask = true
		remaining = remaining[1:]
	}

	if len(remaining) == 0 {
		return "", "", nil, false, "", fmt.Errorf("프롬프트가 없습니다")
	}
	prompt = strings.Join(remaining, " ")
	return cronExpr, script, dependsOn, isTask, prompt, nil
}

// allCronFields returns true if all 5 tokens look like valid cron expression fields.
func allCronFields(tokens []string) bool {
	if len(tokens) < 5 {
		return false
	}
	for _, t := range tokens[:5] {
		for _, c := range t {
			if (c < '0' || c > '9') && c != '*' && c != '/' && c != '-' && c != ',' && c != '?' {
				return false
			}
		}
		if t == "" {
			return false
		}
	}
	return true
}

// parseOnceDatetime parses "HH:MM" or "YYYY-MM-DD HH:MM" from the start of tokens.
// Returns (fireAt, tokensConsumed, error).
func parseOnceDatetime(tokens []string) (time.Time, int, error) {
	if len(tokens) == 0 {
		return time.Time{}, 0, fmt.Errorf("시각 없음")
	}
	now := time.Now()
	// Try "HH:MM"
	if t, err := time.ParseInLocation("15:04", tokens[0], time.Local); err == nil {
		fireAt := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, time.Local)
		if fireAt.Before(now) {
			fireAt = fireAt.Add(24 * time.Hour)
		}
		return fireAt, 1, nil
	}
	// Try "YYYY-MM-DD HH:MM" (2 tokens)
	if len(tokens) >= 2 {
		combined := tokens[0] + " " + tokens[1]
		if t, err := time.ParseInLocation("2006-01-02 15:04", combined, time.Local); err == nil {
			if t.Before(time.Now()) {
				return time.Time{}, 0, fmt.Errorf("과거 날짜입니다: %s", combined)
			}
			return t, 2, nil
		}
	}
	return time.Time{}, 0, fmt.Errorf("%q — HH:MM 또는 YYYY-MM-DD HH:MM 형식으로 입력하세요", tokens[0])
}

// parseFlags4 extracts up to 4 named flag values from tokens.
func parseFlags4(tokens []string, flag1, flag2, flag3, flag4 string) (v1, v2, v3, v4 string) {
	known := map[string]*string{flag1: &v1, flag2: &v2, flag3: &v3, flag4: &v4}
	i := 0
	for i < len(tokens) {
		dest, ok := known[tokens[i]]
		if !ok {
			i++
			continue
		}
		i++
		var parts []string
		for i < len(tokens) {
			if _, isFlag := known[tokens[i]]; isFlag {
				break
			}
			parts = append(parts, tokens[i])
			i++
		}
		if len(parts) > 0 {
			*dest = strings.Join(parts, " ")
		}
	}
	return
}

// parseDependsOn splits a comma-separated list of task IDs.
func parseDependsOn(raw string) []string {
	var out []string
	for _, id := range strings.Split(raw, ",") {
		if id := strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// parseFlags extracts up to 3 named flag values from tokens.
// A flag's value spans from the flag to the next recognized flag (or end of tokens),
// so "--cron 0 9 * * 1-5 --prompt foo" correctly captures "0 9 * * 1-5" as the cron value.
func parseFlags(tokens []string, flag1, flag2, flag3 string) (v1, v2, v3 string) {
	known := map[string]*string{flag1: &v1, flag2: &v2, flag3: &v3}
	i := 0
	for i < len(tokens) {
		dest, ok := known[tokens[i]]
		if !ok {
			i++
			continue
		}
		i++
		var parts []string
		for i < len(tokens) {
			if _, isFlag := known[tokens[i]]; isFlag {
				break
			}
			parts = append(parts, tokens[i])
			i++
		}
		if len(parts) > 0 {
			*dest = strings.Join(parts, " ")
		}
	}
	return
}

func taskHelpText() string {
	return strings.TrimSpace(`
📋 !task — 통합 스케줄 관리

등록:
!task add <주기> [task] <프롬프트>
  주기: 30m, 2h, 1d, 1w, 매시간, 매일, 매주, 또는 5-field cron (0 9 * * 1-5)
  task 키워드 있으면 Claude 작업, 없으면 알림
  예) !task add 매일 task 오늘 요약해줘
  예) !task add daily task 오늘 요약해줘
  예) !task add 0 9 * * 1-5 task 주식 확인

스크립트 사전확인:
!task add <주기> --script <bash_expr> [task] <프롬프트>
  스크립트가 {"wakeAgent":true} 반환할 때만 실행

일회성:
!task once <HH:MM|YYYY-MM-DD HH:MM> <메시지>
  예) !task once 09:00 아침 회의 준비해줘
  예) !task once 2026-06-12 14:30 배포 확인해줘

관리:
!task list [pending|paused|all]
!task pause <id>      — 일시정지
!task resume <id>     — 재개
!task cancel <id>     — 취소
!task update <id> [--cron <식>] [--prompt <텍스트>] [--script <스크립트|clear>] [--depends-on <id,...|none>]
  --script clear        스크립트 제거
  --depends-on none     의존성 초기화
`)
}

// hasAttachment returns true if the message contains a downloadable file.
func (b *Bot) hasAttachment(msg *tgbotapi.Message) bool {
	return len(msg.Photo) > 0 || msg.Document != nil || msg.Video != nil ||
		msg.Audio != nil || msg.Voice != nil
}

// handleAttachment downloads the attached file, saves it to <data dir>/attachments/,
// and dispatches a combined prompt (caption + file path) to Claude.
func (b *Bot) handleAttachment(chatID int64, msg *tgbotapi.Message) {
	caption := strings.TrimSpace(msg.Caption)

	fileID, ext := attachFileInfo(msg)
	if fileID == "" {
		if caption != "" {
			b.dispatchText(chatID, caption, OriginTelegram)
		}
		return
	}

	savePath, err := b.downloadAttachment(fileID, ext)
	if err != nil {
		log.Printf("[bot] attachment download failed: %v", err)
		_ = b.Send(chatID, "⚠️ 첨부파일 다운로드 실패: "+err.Error())
		return
	}

	b.ingestAttachment(chatID, savePath, caption, OriginTelegram)
}

// attachmentsDir is the one directory aglink saves attachments into. Both
// producers — the Telegram download and aglink-chat's upload relay — write here.
func attachmentsDir() (string, error) {
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "attachments"), nil
}

// insideDir reports whether path resolves to something under dir. Purely
// lexical after cleaning, which is what we want: the caller must not be able to
// escape with ".." or by naming an unrelated absolute path.
func insideDir(dir, path string) bool {
	ad, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	ap, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(ad, ap)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ingestAttachment builds a prompt from a saved file path + caption and dispatches
// it. Shared by the Telegram attachment path and the web upload endpoint so both
// behave identically.
//
// savePath is not trusted. The control API's upload_attachment carries it from
// the client, and it used to be pruned by directory: filepath.Dir(savePath) was
// handed to pruneAttachments, which deletes everything but the newest
// maxAttachments files. A path naming any other directory therefore deleted the
// user's files there. It was also passed straight to a worker to read.
func (b *Bot) ingestAttachment(chatID int64, savePath, caption, origin string) {
	b.ingestAttachmentTargeted(chatID, savePath, caption, origin, nil)
}

func (b *Bot) ingestAttachmentTargeted(chatID int64, savePath, caption, origin string, tgt *Target) {
	dir, err := attachmentsDir()
	if err != nil {
		log.Printf("[bot] attachment rejected: cannot resolve attachments dir: %v", err)
		return
	}
	if !insideDir(dir, savePath) {
		log.Printf("[bot] attachment rejected: %q is outside %q", savePath, dir)
		return
	}

	// Cap the attachments directory to the most recent maxAttachments files. The
	// just-saved file is the newest, so it always survives. Best-effort.
	pruneAttachments(dir, maxAttachments)

	prompt := caption
	if prompt == "" {
		prompt = "첨부파일을 분석해줘"
	}
	prompt = prompt + "\n\n[첨부파일: " + savePath + "]"
	if tgt != nil {
		b.dispatchTargeted(chatID, prompt, tgt)
		return
	}
	b.dispatchText(chatID, prompt, origin)
}

// attachFileInfo extracts the Telegram file ID and extension for the first attachment found.
func attachFileInfo(msg *tgbotapi.Message) (fileID, ext string) {
	if len(msg.Photo) > 0 {
		// Use the last (highest-resolution) photo size.
		return msg.Photo[len(msg.Photo)-1].FileID, ".jpg"
	}
	if msg.Document != nil {
		ext := filepath.Ext(msg.Document.FileName)
		if ext == "" {
			ext = extFromMIME(msg.Document.MimeType)
		}
		return msg.Document.FileID, ext
	}
	if msg.Video != nil {
		return msg.Video.FileID, ".mp4"
	}
	if msg.Audio != nil {
		return msg.Audio.FileID, ".mp3"
	}
	if msg.Voice != nil {
		return msg.Voice.FileID, ".ogg"
	}
	return "", ""
}

// downloadAttachment fetches a Telegram file by ID and saves it to the attachments directory.
func (b *Bot) downloadAttachment(fileID, ext string) (string, error) {
	dir, err := attachmentsDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("첨부파일 디렉터리 생성 실패: %w", err)
	}

	tgFile, err := b.api.GetFile(tgbotapi.FileConfig{FileID: fileID})
	if err != nil {
		return "", fmt.Errorf("Telegram 파일 정보 조회 실패: %w", err)
	}
	url := tgFile.Link(b.api.Token)

	dlCtx, dlCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer dlCancel()
	req, err := http.NewRequestWithContext(dlCtx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("요청 생성 실패: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("파일 다운로드 실패: %w", err)
	}
	defer resp.Body.Close()

	saveName := fmt.Sprintf("%d%s", time.Now().UnixMilli(), ext)
	savePath := filepath.Join(dir, saveName)
	f, err := os.Create(savePath)
	if err != nil {
		return "", fmt.Errorf("파일 저장 실패: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return "", fmt.Errorf("파일 쓰기 실패: %w", err)
	}
	log.Printf("[bot] attachment saved: %s", savePath)
	return savePath, nil
}

// extFromMIME returns a file extension guess from a MIME type.
func extFromMIME(mime string) string {
	switch strings.ToLower(mime) {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	case "application/zip":
		return ".zip"
	default:
		return ".bin"
	}
}

// handleHistory processes !history commands — date-based conversation log viewer.
//
//	!history                          — today's log for active project
//	!history list [project|all]       — list available dates (all = all projects)
//	!history <YYYY-MM-DD>             — specific date, active project
//	!history <project>                — today's log for named project
//	!history <project> <YYYY-MM-DD>   — specific project + date
func (b *Bot) handleHistory(reply replySender, chatID int64, fields []string) {
	active := b.store.GetActive()
	defaultProject := active.Project

	if len(fields) >= 2 && fields[1] == "list" {
		// "!history list all" → list all projects that have history
		if len(fields) >= 3 && fields[2] == "all" {
			projects, err := ListHistoryProjects()
			if err != nil {
				_ = reply.Send(chatID, "⚠️ 히스토리 프로젝트 목록 조회 실패: "+err.Error())
				return
			}
			if len(projects) == 0 {
				_ = reply.Send(chatID, "📅 기록된 히스토리가 없습니다.")
				return
			}
			_ = reply.Send(chatID, "📅 히스토리가 있는 프로젝트:\n"+strings.Join(projects, "\n"))
			return
		}

		project := defaultProject
		if len(fields) >= 3 {
			project = fields[2]
		}
		if project == "" {
			_ = reply.Send(chatID, "활성 프로젝트가 없습니다. !history list <프로젝트명> 또는 !history list all")
			return
		}
		dates, err := ListHistoryDates(project)
		if err != nil {
			_ = reply.Send(chatID, "⚠️ 히스토리 목록 조회 실패: "+err.Error())
			return
		}
		if len(dates) == 0 {
			_ = reply.Send(chatID, "📅 "+project+": 기록된 날짜 없음")
			return
		}
		_ = reply.Send(chatID, "📅 "+project+" 히스토리 날짜:\n"+strings.Join(dates, "\n"))
		return
	}

	// Parse: !history [project] [YYYY-MM-DD]
	project := defaultProject
	date := time.Now().Format("2006-01-02")
	for _, arg := range fields[1:] {
		if len(arg) == 10 && arg[4] == '-' && arg[7] == '-' {
			if _, err := time.Parse("2006-01-02", arg); err != nil {
				_ = reply.Send(chatID, "⚠️ 날짜 형식 오류: "+arg+" (YYYY-MM-DD 사용)")
				return
			}
			date = arg
		} else {
			project = arg
		}
	}

	if project == "" {
		_ = reply.Send(chatID, "활성 프로젝트가 없습니다.\n!history list all 로 기록이 있는 프로젝트를 확인하거나 !history <프로젝트명> 형식으로 사용하세요.")
		return
	}

	content, err := ReadHistory(project, date)
	if err != nil {
		_ = reply.Send(chatID, "⚠️ 히스토리 조회 실패: "+err.Error())
		return
	}
	if content == "" {
		_ = reply.Send(chatID, fmt.Sprintf("📅 %s / %s: 기록 없음", project, date))
		return
	}
	// Telegram's sendMessage limit is 4096 characters.
	// Truncate by UTF-8 byte length (conservative for CJK: each Korean rune = 3 bytes).
	// Reserve ~200 bytes for the "📅 <project> / <date>:\n" header.
	const maxContentBytes = 3800
	if len(content) > maxContentBytes {
		end := maxContentBytes
		b2 := []byte(content)
		// Step back to the nearest valid UTF-8 rune boundary.
		for end > 0 && (b2[end]&0xC0) == 0x80 {
			end--
		}
		content = string(b2[:end]) + "\n...(잘림)"
	}
	_ = reply.Send(chatID, fmt.Sprintf("📅 %s / %s:\n%s", project, date, content))
}

// handleBackend handles !backend — displays or switches the active AI backend.
func (b *Bot) handleBackend(reply replySender, chatID int64, fields []string) {
	if len(fields) < 2 {
		_ = reply.Send(chatID, "현재 백엔드: "+strings.ToUpper(b.manager.Backend()))
		return
	}
	target := strings.ToLower(fields[1])
	switch target {
	case "claude", "codex", "opencode":
	default:
		_ = reply.Send(chatID, "사용법: !backend [claude|codex|opencode]")
		return
	}

	if active, _ := b.dispatchLoad(); active > 0 {
		_ = reply.Send(chatID, "⏳ 작업 중에는 백엔드를 전환할 수 없습니다. !cancel 후 다시 시도하세요.")
		return
	}

	current := b.manager.Backend()
	if current == target {
		_ = reply.Send(chatID, "이미 "+strings.ToUpper(target)+" 백엔드입니다.")
		return
	}

	if err := b.manager.SetBackend(target); err != nil {
		_ = reply.Send(chatID, "⚠️ "+err.Error())
		return
	}
	_ = reply.Send(chatID, fmt.Sprintf("✅ 백엔드 전환됨: %s → %s", strings.ToUpper(current), strings.ToUpper(target)))
}

// handleInteractive toggles this conversation between the normal per-turn
// headless client and a persistent ConPTY-backed session
// (interactiveClaudeRunner), so a message sent while an earlier one is still
// running steers into the live session instead of queueing behind it. Web
// conversations only — see Manager.SetInteractive for why the telegram stream
// doesn't support this.
func (b *Bot) handleInteractive(reply replySender, chatID int64, fields []string, tgt Target) {
	if len(fields) < 2 {
		state := "꺼짐"
		if b.manager.IsInteractive(tgt) {
			state = "켜짐"
		}
		_ = reply.Send(chatID, "현재 interactive 모드: "+state+"\n사용법: !interactive on|off")
		return
	}
	switch strings.ToLower(fields[1]) {
	case "on":
		if err := b.manager.SetInteractive(tgt, true); err != nil {
			_ = reply.Send(chatID, "⚠️ "+err.Error())
			return
		}
		_ = reply.Send(chatID, "✅ interactive 모드 켜짐 — 이 대화는 이제 상주 세션을 사용하며, 처리 중에 보낸 메시지는 끼워넣기(steering)됩니다.")
	case "off":
		if err := b.manager.SetInteractive(tgt, false); err != nil {
			_ = reply.Send(chatID, "⚠️ "+err.Error())
			return
		}
		_ = reply.Send(chatID, "✅ interactive 모드 꺼짐 — 다음 메시지부터 일반 방식으로 처리됩니다.")
	default:
		_ = reply.Send(chatID, "사용법: !interactive on|off")
	}
}

// handleParallel dispatches multiple independent prompts concurrently.
// Syntax: !parallel <prompt1> | <prompt2> | ...
// Each |-separated prompt becomes its own worker; responses arrive independently.
func (b *Bot) handleParallel(reply replySender, chatID int64, text, origin string) {
	rest := strings.TrimSpace(strings.TrimPrefix(text, "!parallel"))
	if rest == "" {
		_ = reply.Send(chatID, "사용법: !parallel <프롬프트1> | <프롬프트2> | ...\n예) !parallel 테스트 작성해줘 | 문서 업데이트해줘")
		return
	}
	parts := strings.Split(rest, "|")
	var prompts []string
	for _, p := range parts {
		if p := strings.TrimSpace(p); p != "" {
			prompts = append(prompts, p)
		}
	}
	if len(prompts) == 0 {
		_ = reply.Send(chatID, "⚠️ 유효한 프롬프트가 없습니다.")
		return
	}
	// Cap to MaxWorkers to prevent unbounded resource / rate-limit bypass.
	maxP := b.cfg().MaxWorkers
	if maxP < 1 {
		maxP = 1
	}
	if len(prompts) > maxP {
		_ = reply.Send(chatID, fmt.Sprintf("⚠️ !parallel 최대 %d개까지 허용됩니다 (%d개 입력됨). 앞의 %d개만 실행합니다.", maxP, len(prompts), maxP))
		prompts = prompts[:maxP]
	}
	if len(prompts) == 1 {
		b.dispatchText(chatID, prompts[0], origin)
		return
	}
	_ = reply.Send(chatID, fmt.Sprintf("🔀 %d개 병렬 작업 시작합니다...", len(prompts)))
	for _, p := range prompts {
		b.dispatchText(chatID, p, origin)
	}
}

// removeInt64 returns a copy of ids without the given value.
func removeInt64(ids []int64, v int64) []int64 {
	out := ids[:0:0]
	for _, id := range ids {
		if id != v {
			out = append(out, id)
		}
	}
	return out
}

// handleUser manages the runtime allow-list: !user add <id> | remove <id> | list
func (b *Bot) handleUser(reply replySender, chatID int64, fields []string) {
	if b.userStore == nil {
		_ = reply.Send(chatID, "⚠️ UserStore를 사용할 수 없습니다.")
		return
	}
	if len(fields) < 2 {
		_ = reply.Send(chatID, "사용법: !user add <id> | !user remove <id> | !user list")
		return
	}
	switch fields[1] {
	case "add":
		if len(fields) < 3 {
			_ = reply.Send(chatID, "사용법: !user add <telegram_user_id>")
			return
		}
		var id int64
		if _, err := fmt.Sscanf(fields[2], "%d", &id); err != nil {
			_ = reply.Send(chatID, "⚠️ 잘못된 사용자 ID (숫자 입력 필요): "+fields[2])
			return
		}
		if err := b.userStore.Add(id); err != nil {
			_ = reply.Send(chatID, "⚠️ 저장 실패: "+err.Error())
			return
		}
		_ = reply.Send(chatID, fmt.Sprintf("✅ 사용자 추가됨: %d", id))
	case "remove":
		if len(fields) < 3 {
			_ = reply.Send(chatID, "사용법: !user remove <telegram_user_id>")
			return
		}
		var id int64
		if _, err := fmt.Sscanf(fields[2], "%d", &id); err != nil {
			_ = reply.Send(chatID, "⚠️ 잘못된 사용자 ID: "+fields[2])
			return
		}
		if b.cfg().IsAllowed(id) {
			_ = reply.Send(chatID, "⚠️ config.txt AllowedUserIDs에 있는 사용자는 !user remove로 제거할 수 없습니다.")
			return
		}
		// Lockout guard: refuse if removing this ID would leave no allowed users.
		runtimeAfter := b.userStore.List()
		runtimeAfter = removeInt64(runtimeAfter, id)
		if len(b.cfg().AllowedUserIDs) == 0 && len(b.cfg().AllowedUsernames) == 0 && len(runtimeAfter) == 0 {
			_ = reply.Send(chatID, "⚠️ 이 사용자를 제거하면 허용된 사용자가 없어져 봇이 잠깁니다. 먼저 다른 사용자를 추가하세요.")
			return
		}
		if err := b.userStore.Remove(id); err != nil {
			_ = reply.Send(chatID, "⚠️ 저장 실패: "+err.Error())
			return
		}
		_ = reply.Send(chatID, fmt.Sprintf("🗑 사용자 제거됨: %d", id))
	case "list":
		var sb strings.Builder
		sb.WriteString("👥 허용된 사용자:\n")
		sb.WriteString("  [config] IDs: ")
		for i, id := range b.cfg().AllowedUserIDs {
			if i > 0 {
				sb.WriteString(", ")
			}
			fmt.Fprintf(&sb, "%d", id)
		}
		if len(b.cfg().AllowedUsernames) > 0 {
			sb.WriteString("\n  [config] 유저네임: @" + strings.Join(b.cfg().AllowedUsernames, ", @"))
		}
		runtimeIDs := b.userStore.List()
		if len(runtimeIDs) > 0 {
			sb.WriteString("\n  [runtime] IDs: ")
			for i, id := range runtimeIDs {
				if i > 0 {
					sb.WriteString(", ")
				}
				fmt.Fprintf(&sb, "%d", id)
			}
		} else {
			sb.WriteString("\n  [runtime] 없음")
		}
		_ = reply.Send(chatID, sb.String())
	default:
		_ = reply.Send(chatID, "사용법: !user add <id> | !user remove <id> | !user list")
	}
}

func helpText() string {
	return strings.TrimSpace(`
🤖 aglink — 폰에서 PC의 Claude를 자연어로 쓰세요.

그냥 말하세요. 예) "myapp 로그인 버그 이어서 보자", "voice 서버에 헬스체크 추가해줘"
→ 어느 프로젝트의 어느 대화인지 알아서 찾아 작업합니다.
사진·파일 첨부: 그냥 보내면 Claude가 분석합니다.

명령어:
!project add <이름> <경로>   프로젝트 등록
!project remove <이름>       프로젝트 제거
!project list                프로젝트·대화 목록
!chat new [제목]             현재 프로젝트에 새 대화
!chat list                   현재 프로젝트의 대화 목록
!chat use <id|프로젝트 id>    대화 수동 전환
!status                      실행 중 작업 + 활성 대화 + 백엔드
!cancel                      진행 중 작업 취소
!timeout +<분>|-<분>|reset    진행 중 작업의 제한 시간 조절 (이 작업만, 기본값 미만 불가)

병렬 작업:
!parallel <p1> | <p2> | ...  여러 프롬프트를 동시에 Claude에 전달

스케줄 (통합):
!task add <주기|cron> [task] <내용>         반복 작업/알림 등록
  주기: 30m 2h 1d 1w 매시간 매일 매주
  --depends-on <id,...>   다른 작업 완료 후 실행 (DAG)
!task once <HH:MM|YYYY-MM-DD HH:MM> <내용>  일회성 알림
!task list [pending|paused|all]             목록
!task pause|resume|cancel <id>              관리
!task update <id> --cron|--prompt|--script|--depends-on <값>
!task help                                  상세 도움말

히스토리:
!history [프로젝트] [YYYY-MM-DD]      대화 기록 조회
!history list [프로젝트|all]          날짜 목록 (all = 전체 프로젝트)

!compact   지금까지의 텔레그램 대화 핵심을 이 대화 전용 메모리 파일(.aglink/memory/telegram.md)에 저장하고
           세션을 새로 시작 (컨텍스트가 길어져 느려졌을 때 사용)

사용자 관리:
!user add <id>               런타임 허용 사용자 추가 (재시작 후에도 유지)
!user remove <id>            런타임 허용 사용자 제거
!user list                   허용 사용자 목록 (config + runtime)

화면 제어 (Windows, LLM 우회 즉시 실행):
!screen list                 보이는 창 목록
!screen shot [창이름]         스크린샷 (창 지정 시 해당 창만, 없으면 전체)
!screen region <x> <y> <w> <h> [창이름]  영역 캡처 (창 지정 시 창 상대좌표)
!screen preset save <이름>    현재 커서 위치를 프리셋으로 저장
!screen click <프리셋이름>     저장한 프리셋 좌표 클릭 (즉시)

원격 제어 (SSH):
!ssh list                    등록된 원격 호스트 목록
!ssh <호스트> <명령...>        원격 호스트에서 명령 실행 (예: !ssh gpu1 nvidia-smi)
!ssh test <호스트>            원격 접속 확인

기타:
!remind <시간> <메시지>      일회성 알림 (구버전 호환)
!cron add|list|remove        반복 작업 (구버전 호환)
!backend [claude|codex|opencode]  AI 백엔드 전환
!interactive [on|off]        상주 세션 모드 전환 (웹 대화 전용, 실험적)
                              처리 중에도 메시지를 보내면 끼워넣기(steering)됩니다
!update                      새 버전 빌드 & 자동 재시작
!help                        이 도움말
`)
}

// validateDir confirms path exists and is a directory. Used by webSetDir so a
// per-conversation WorkDir is never set to a missing/non-directory path.
func validateDir(path string) error {
	if path == "" {
		return fmt.Errorf("경로가 비어 있습니다")
	}
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("경로를 찾을 수 없습니다: %s", path)
	}
	if !fi.IsDir() {
		return fmt.Errorf("디렉토리가 아닙니다: %s", path)
	}
	return nil
}

// webNew creates a new top-level web conversation and makes it the active one
// so the web UI can select it immediately.
func (b *Bot) webNew(reply replySender, chatID int64, title string) {
	c, err := b.store.NewWebConv(title)
	if err != nil {
		_ = reply.Send(chatID, "⚠️ 새 웹 대화 생성 실패: "+err.Error())
		return
	}
	_ = b.store.SetActive("", c.ID)
	_ = reply.Send(chatID, "🆕 새 웹 대화: "+c.Title)
}

// webSetDir sets the working directory for a web conversation, validating the
// path exists and is a directory first (never persist a non-existent WorkDir).
func (b *Bot) webSetDir(reply replySender, chatID int64, id, path string) {
	c, ok := b.store.GetWebConv(id)
	if !ok {
		_ = reply.Send(chatID, "웹 대화를 찾을 수 없습니다.")
		return
	}
	if err := validateDir(path); err != nil {
		_ = reply.Send(chatID, "⚠️ "+err.Error())
		return
	}
	c.WorkDir = path
	if err := b.store.UpdateWebConv(c); err != nil {
		_ = reply.Send(chatID, "⚠️ 설정 실패: "+err.Error())
		return
	}
	_ = reply.Send(chatID, "📁 작업 폴더 설정: "+path)
}

// webRename renames a web conversation.
// webRename renames a web conversation. It returns an error (nil on success) so
// a caller can synchronously observe whether the rename actually landed — the
// control API relays that to the browser so a fast reload can't race ahead of
// the store write. The reply.Send confirmations stay: other channels still show
// them in the chat log.
func (b *Bot) webRename(reply replySender, chatID int64, id, title string) error {
	c, ok := b.store.GetWebConv(id)
	if !ok || title == "" {
		_ = reply.Send(chatID, "이름 변경 실패: 대화가 없거나 제목이 비었습니다.")
		return fmt.Errorf("대화가 없거나 제목이 비었습니다")
	}
	c.Title = title
	if err := b.store.UpdateWebConv(c); err != nil {
		_ = reply.Send(chatID, "⚠️ 이름 변경 실패: "+err.Error())
		return err
	}
	_ = reply.Send(chatID, "✏️ 이름 변경: "+title)
	return nil
}

// webDelete removes a top-level web conversation. store.DeleteWebConv already
// clears the active pointer when it referenced the deleted conversation.
func (b *Bot) webDelete(reply replySender, chatID int64, id string) {
	if _, ok := b.store.GetWebConv(id); !ok {
		_ = reply.Send(chatID, "웹 대화를 찾을 수 없습니다.")
		return
	}
	if err := b.store.DeleteWebConv(id); err != nil {
		_ = reply.Send(chatID, "⚠️ 삭제 실패: "+err.Error())
		return
	}
	_ = reply.Send(chatID, "🗑️ 대화가 삭제되었습니다.")
}

func (b *Bot) setChannelBackend(tgt Target, backend string) error {
	backend, ok := normalizeChannelBackendOverride(backend)
	if !ok {
		return fmt.Errorf("backend must be default, claude, or codex")
	}
	if b.manager != nil {
		if err := b.manager.requireBackendAvailable(backend); err != nil {
			return err
		}
	}
	if tgt.IsWeb() {
		if tgt.ID == "" {
			return fmt.Errorf("web conversation id is required")
		}
		c, ok := b.store.GetWebConv(tgt.ID)
		if !ok {
			return fmt.Errorf("web conversation not found: %s", tgt.ID)
		}
		c.Backend = backend
		return b.store.UpdateWebConv(c)
	}
	c := b.store.TelegramConversation()
	c.Backend = backend
	return b.store.UpdateTelegramConversation(c)
}
