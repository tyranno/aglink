# Web Chat Transport Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a localhost web-chat UI as a second transport so teleclaude can be driven from a browser with full feature parity, sharing conversation/project state with the existing Telegram bot.

**Architecture:** Introduce a broadcast `Hub` that fans every outgoing message/photo/typing to all registered channels. Telegram is a "global" channel (receives every chatID); each browser WebSocket connection is a per-owner channel. `Bot.Send/SendPhoto/Typing` delegate to the Hub, so existing handlers fan out unchanged. Web input is injected into the same `handleCommand`/`dispatchText` pipeline under the owner's chatID. Manager/worker/store logic is reused as-is.

**Tech Stack:** Go 1.25.5, `net/http`, `github.com/coder/websocket` (pure-Go WS, CGO-free), `go:embed` for the vanilla-JS UI, existing `github.com/go-telegram-bot-api/telegram-bot-api/v5`.

**Design ref:** `docs/superpowers/specs/2026-07-02-web-chat-transport-design.md`

## Global Constraints

- CGO-free; single static .exe. Only new dependency allowed: `github.com/coder/websocket`.
- Localhost only: web server binds `127.0.0.1` (never `0.0.0.0`).
- Config section name is `web_chat` — do NOT confuse with existing `web_control` (aglink-web browser control).
- Cross-compile clean: `GOOS=windows go build ./...` and `GOOS=linux go build ./...`.
- `go vet ./...` and `go test ./...` green after every task.
- Module path is `teleclaude`; package is `main` (flat package — new files are `package main`).
- Commit after every task. Do NOT push (repo policy: local commits only).
- Commit trailer: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Windows note: `gofmt -l` flags CRLF but git stores LF; verify real formatting with `tr -d '\r' | gofmt -d`.

## v1 Simplifications (documented deviations from spec)

- Long assistant replies: chunking stays Telegram-oriented. Web receives the same message(s) the Telegram path produces (a long reply may arrive as multiple bubbles). A single-bubble web view is a later nicety.
- Web input is injected via WebSocket (`{type:"send"}`); there is no separate `/api/send` HTTP route. File upload uses `POST /api/upload`.

---

## File Structure

| File | Responsibility |
|---|---|
| `hub.go` (new) | `ChannelSender` interface + `Hub` (global + per-chat registries, fan-out). Transport-agnostic (no tgbotapi import). |
| `bot.go` (modify) | `telegramChannel` (tgbotapi send bodies moved here); `Bot.out *Hub`; `Send/SendPhoto/Typing` delegate to Hub; `Hub()` accessor; `ingestAttachment` extracted from `handleAttachment`. |
| `webchat.go` (new) | `webServer` (http on 127.0.0.1), token load/create, auth + origin checks, `/ws` handler, `webChannel`, `/api/upload`, embedded UI serving, input injection. |
| `web/index.html`, `web/app.js`, `web/style.css` (new) | Embedded vanilla-JS chat UI. |
| `types.go` (modify) | `Config` fields: `WebChat`, `WebChatAddr`, `WebChatToken`, `WebChatOwnerChatID`. |
| `config_yaml.go` (modify) | `web_chat` YAML struct + both-way mapping + default addr. |
| `main.go` (modify) | Start `webServer` goroutine when `web_chat.enabled`. |
| `hub_test.go`, `webchat_test.go` (new); `config_yaml_test.go`, `bot_test.go`/existing (modify) | Tests. |

---

## Task 1: Hub + ChannelSender + telegramChannel

**Files:**
- Create: `hub.go`
- Modify: `bot.go` (add `telegramChannel` type)
- Test: `hub_test.go`

**Interfaces:**
- Produces:
  - `type ChannelSender interface { Send(chatID int64, text string) error; SendPhoto(chatID int64, png []byte, caption string) error; Typing(chatID int64) }`
  - `type Hub struct{...}`; `func NewHub() *Hub`; `func (h *Hub) RegisterGlobal(ch ChannelSender)`; `func (h *Hub) Register(chatID int64, ch ChannelSender)`; `func (h *Hub) Unregister(chatID int64, ch ChannelSender)`; `func (h *Hub) Send(chatID int64, text string) error`; `func (h *Hub) SendPhoto(chatID int64, png []byte, caption string) error`; `func (h *Hub) Typing(chatID int64)`
  - `type telegramChannel struct{ api *tgbotapi.BotAPI }`; `func newTelegramChannel(api *tgbotapi.BotAPI) *telegramChannel`
- Consumes: nothing (leaf).

- [ ] **Step 1: Write the failing test** — `hub_test.go`

```go
package main

import (
	"errors"
	"sync"
	"testing"
)

// recCh is a ChannelSender test double recording calls; optional forced error.
type recCh struct {
	mu       sync.Mutex
	texts    []string
	photos   int
	typings  int
	sendErr  error
}

func (r *recCh) Send(_ int64, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.texts = append(r.texts, text)
	return r.sendErr
}
func (r *recCh) SendPhoto(_ int64, _ []byte, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.photos++
	return nil
}
func (r *recCh) Typing(_ int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.typings++
}

func TestHubFanOut_GlobalPlusPerChat(t *testing.T) {
	h := NewHub()
	g := &recCh{}     // telegram-like global
	w := &recCh{}     // web, bound to chat 7
	other := &recCh{} // web bound to a different chat
	h.RegisterGlobal(g)
	h.Register(7, w)
	h.Register(99, other)

	_ = h.Send(7, "hi")
	_ = h.SendPhoto(7, []byte("png"), "cap")
	h.Typing(7)

	if len(g.texts) != 1 || g.texts[0] != "hi" {
		t.Errorf("global should get the text, got %v", g.texts)
	}
	if len(w.texts) != 1 || g.photos != 1 || w.photos != 1 || g.typings != 1 || w.typings != 1 {
		t.Errorf("chat-7 channels should receive; g=%+v w=%+v", g, w)
	}
	if len(other.texts) != 0 || other.photos != 0 || other.typings != 0 {
		t.Errorf("chat-99 channel must NOT receive chat-7 traffic, got %+v", other)
	}
}

func TestHubUnregister(t *testing.T) {
	h := NewHub()
	w := &recCh{}
	h.Register(7, w)
	h.Unregister(7, w)
	_ = h.Send(7, "hi")
	if len(w.texts) != 0 {
		t.Errorf("unregistered channel must not receive, got %v", w.texts)
	}
}

func TestHubErrorIsolation(t *testing.T) {
	h := NewHub()
	bad := &recCh{sendErr: errors.New("boom")}
	good := &recCh{}
	h.RegisterGlobal(bad)
	h.Register(7, good)
	if err := h.Send(7, "hi"); err != nil {
		t.Errorf("Hub.Send should swallow per-channel errors, got %v", err)
	}
	if len(good.texts) != 1 {
		t.Errorf("a failing channel must not block others, got %v", good.texts)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestHub ./...`
Expected: FAIL — `undefined: NewHub` / `ChannelSender`.

- [ ] **Step 3: Implement `hub.go`**

```go
package main

import (
	"log"
	"sync"
)

// ChannelSender is the full output surface a transport channel must provide.
// Extends the relay MessageSender (Send/Typing) with photo delivery so images
// fan out too. Telegram and each web connection implement this.
type ChannelSender interface {
	Send(chatID int64, text string) error
	SendPhoto(chatID int64, png []byte, caption string) error
	Typing(chatID int64)
}

// Hub fans outgoing messages to every registered channel. Global channels
// (Telegram) receive traffic for every chatID; per-chat channels (web sessions)
// receive only their bound chatID. A per-channel error is logged and isolated so
// one dead channel never blocks the others. Hub itself satisfies ChannelSender.
type Hub struct {
	mu      sync.RWMutex
	global  []ChannelSender
	perChat map[int64][]ChannelSender
}

func NewHub() *Hub {
	return &Hub{perChat: make(map[int64][]ChannelSender)}
}

func (h *Hub) RegisterGlobal(ch ChannelSender) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.global = append(h.global, ch)
}

func (h *Hub) Register(chatID int64, ch ChannelSender) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.perChat[chatID] = append(h.perChat[chatID], ch)
}

func (h *Hub) Unregister(chatID int64, ch ChannelSender) {
	h.mu.Lock()
	defer h.mu.Unlock()
	list := h.perChat[chatID]
	for i, c := range list {
		if c == ch {
			h.perChat[chatID] = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(h.perChat[chatID]) == 0 {
		delete(h.perChat, chatID)
	}
}

// targets returns the fan-out set for chatID: all global channels plus any
// per-chat channels bound to that chatID (copied so we don't hold the lock
// while calling into channels).
func (h *Hub) targets(chatID int64) []ChannelSender {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]ChannelSender, 0, len(h.global)+len(h.perChat[chatID]))
	out = append(out, h.global...)
	out = append(out, h.perChat[chatID]...)
	return out
}

func (h *Hub) Send(chatID int64, text string) error {
	for _, ch := range h.targets(chatID) {
		if err := ch.Send(chatID, text); err != nil {
			log.Printf("[hub] channel send error: %v", err)
		}
	}
	return nil
}

func (h *Hub) SendPhoto(chatID int64, png []byte, caption string) error {
	for _, ch := range h.targets(chatID) {
		if err := ch.SendPhoto(chatID, png, caption); err != nil {
			log.Printf("[hub] channel photo error: %v", err)
		}
	}
	return nil
}

func (h *Hub) Typing(chatID int64) {
	for _, ch := range h.targets(chatID) {
		ch.Typing(chatID)
	}
}
```

- [ ] **Step 4: Add `telegramChannel` to `bot.go`** (place directly above the current `Send` method, ~line 71)

```go
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

func (t *telegramChannel) Send(chatID int64, text string) error {
	msg := tgbotapi.NewMessage(chatID, text)
	_, err := t.api.Send(msg)
	if err != nil {
		log.Printf("[tg] send error: %v", err)
	}
	return err
}

func (t *telegramChannel) SendPhoto(chatID int64, png []byte, caption string) error {
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

func (t *telegramChannel) Typing(chatID int64) {
	if _, err := t.api.Request(tgbotapi.NewChatAction(chatID, tgbotapi.ChatTyping)); err != nil {
		log.Printf("[tg] typing error: %v", err)
	}
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test -run TestHub ./...`
Expected: PASS.

- [ ] **Step 6: Build + vet**

Run: `GOOS=windows go build ./... && go vet ./...`
Expected: no output (success). `telegramChannel` is unused for now — Go allows unused types (only unused imports/locals fail), so this builds.

- [ ] **Step 7: Commit**

```bash
git add hub.go bot.go hub_test.go
git commit -m "feat(web-chat): broadcast Hub + ChannelSender + telegramChannel"
```

---

## Task 2: Bot delegates output to the Hub

**Files:**
- Modify: `bot.go` (struct field, `NewBot`, `Send`/`SendPhoto`/`Typing` bodies, add `Hub()` accessor)
- Test: `hub_test.go` (add delegation test)

**Interfaces:**
- Consumes: `NewHub`, `RegisterGlobal`, `newTelegramChannel` (Task 1).
- Produces: `func (b *Bot) Hub() *Hub`. `Bot.Send/SendPhoto/Typing` now fan out via the Hub. Telegram is registered as the sole global channel at construction.

- [ ] **Step 1: Write the failing test** — append to `hub_test.go`

```go
func TestBotHubAccessorRegistersTelegram(t *testing.T) {
	// A freshly constructed Bot must expose a Hub that already has the Telegram
	// global channel registered, so registering a web channel + sending reaches both.
	// We only assert the web side here (Telegram needs a live API), proving Bot.Send
	// delegates to the Hub rather than calling tgbotapi directly.
	b := &Bot{}
	b.out = NewHub() // mimic NewBot's hub init without a real tgbotapi client
	w := &recCh{}
	b.out.Register(55, w)
	if err := b.Send(55, "via hub"); err != nil {
		t.Fatal(err)
	}
	if b.Hub() != b.out {
		t.Error("Hub() must return the bot's hub")
	}
	if len(w.texts) != 1 || w.texts[0] != "via hub" {
		t.Errorf("Bot.Send must delegate to Hub, got %v", w.texts)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestBotHubAccessor ./...`
Expected: FAIL — `b.out undefined` / `b.Hub undefined`.

- [ ] **Step 3: Add the `out` field to the `Bot` struct** (`bot.go` ~line 40, inside the struct)

```go
	onReady     func() // called once after GetUpdatesChan starts (handoff signal)
	out         *Hub   // output fan-out: telegram (global) + web channels (per-chat)
```

- [ ] **Step 4: Initialize the Hub in `NewBot`** (replace the `return &Bot{...}` block)

```go
func NewBot(api *tgbotapi.BotAPI, cfgh *ConfigHolder, store StoreRepo, manager *Manager, scheduler *Scheduler, userStore *UserStore) *Bot {
	hub := NewHub()
	hub.RegisterGlobal(newTelegramChannel(api))
	return &Bot{
		api:         api,
		cfgh:        cfgh,
		store:       store,
		manager:     manager,
		scheduler:   scheduler,
		rateLimiter: NewRateLimiter(cfgh.Get().RateLimitPerMin),
		userStore:   userStore,
		cancels:     make(map[int]context.CancelFunc),
		out:         hub,
	}
}
```

- [ ] **Step 5: Replace `Bot.Send`/`SendPhoto`/`Typing` bodies to delegate**

```go
// Send delivers a plain-text message, fanning out to all channels (MessageSender).
func (b *Bot) Send(chatID int64, text string) error {
	return b.out.Send(chatID, text)
}

// SendPhoto delivers a PNG image with optional caption to all channels.
func (b *Bot) SendPhoto(chatID int64, png []byte, caption string) error {
	return b.out.SendPhoto(chatID, png, caption)
}

// Typing shows the "typing…" indicator on all channels (MessageSender).
func (b *Bot) Typing(chatID int64) {
	b.out.Typing(chatID)
}

// Hub returns the output fan-out hub so other transports (web chat) can register
// their own channels.
func (b *Bot) Hub() *Hub { return b.out }
```

- [ ] **Step 6: Run tests**

Run: `go test ./...`
Expected: PASS (all existing tests still green — manager tests use `fakeSender`, unaffected; `TestBotHubAccessorRegistersTelegram` passes).

- [ ] **Step 7: Build + vet**

Run: `GOOS=windows go build ./... && GOOS=linux go build ./... && go vet ./...`
Expected: success.

- [ ] **Step 8: Commit**

```bash
git add bot.go hub_test.go
git commit -m "feat(web-chat): route Bot output through the Hub; expose Bot.Hub()"
```

---

## Task 3: `web_chat` config

**Files:**
- Modify: `types.go` (Config fields), `config_yaml.go` (yaml struct + mapping + default)
- Test: `config_yaml_test.go` (extend round-trip)

**Interfaces:**
- Produces: `Config.WebChat bool`, `Config.WebChatAddr string`, `Config.WebChatToken string`, `Config.WebChatOwnerChatID int64`. YAML key `web_chat` with `enabled`, `addr`, `token`, `owner_chat_id`.

- [ ] **Step 1: Write the failing test** — edit `config_yaml_test.go` `TestYAMLRoundTrip`, add fields to the constructed `Config` (after `WebBinaryPath` line) and to the assertion.

Add to the struct literal:
```go
		WebChat:            true,
		WebChatAddr:        "127.0.0.1:1717",
		WebChatToken:       "tok-abc",
		WebChatOwnerChatID: 6723802240,
```
Add to the mismatch `if` condition (append with `||`):
```go
		got.WebChat != true || got.WebChatAddr != "127.0.0.1:1717" ||
		got.WebChatToken != "tok-abc" || got.WebChatOwnerChatID != 6723802240 ||
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestYAMLRoundTrip ./...`
Expected: FAIL — `WebChat` etc. undefined on `Config`.

- [ ] **Step 3: Add fields to `Config` in `types.go`** (next to `WebControl`/`WebBinaryPath`, ~line 35)

```go
	WebChat            bool   // local web chat transport enabled
	WebChatAddr        string // web chat bind address (localhost only), default 127.0.0.1:1717
	WebChatToken       string // web chat auth token; empty → auto-generated + persisted
	WebChatOwnerChatID int64  // chatID web actions run as; 0 → first AllowedUserIDs
```

- [ ] **Step 4: Add YAML struct + mapping in `config_yaml.go`**

In the yaml document struct (near the `WebControl` block, ~lines 50-53) add:
```go
	WebChat struct {
		Enabled     bool   `yaml:"enabled"`
		Addr        string `yaml:"addr"`
		Token       string `yaml:"token"`
		OwnerChatID int64  `yaml:"owner_chat_id"`
	} `yaml:"web_chat"`
```

In `yamlToConfig` (near `c.WebControl` mapping, ~line 110):
```go
	c.WebChat = y.WebChat.Enabled
	c.WebChatAddr = y.WebChat.Addr
	if c.WebChatAddr == "" {
		c.WebChatAddr = "127.0.0.1:1717"
	}
	c.WebChatToken = y.WebChat.Token
	c.WebChatOwnerChatID = y.WebChat.OwnerChatID
```

In `configToYAML` (reverse, near `y.WebControl`, ~line 142):
```go
	y.WebChat.Enabled = c.WebChat
	y.WebChat.Addr = c.WebChatAddr
	y.WebChat.Token = c.WebChatToken
	y.WebChat.OwnerChatID = c.WebChatOwnerChatID
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test -run TestYAMLRoundTrip ./...`
Expected: PASS. (The round-trip sets `WebChatAddr` to `127.0.0.1:1717`, which the default branch preserves.)

- [ ] **Step 6: Build + vet + full test**

Run: `GOOS=windows go build ./... && go vet ./... && go test ./...`
Expected: success.

- [ ] **Step 7: Commit**

```bash
git add types.go config_yaml.go config_yaml_test.go
git commit -m "feat(web-chat): web_chat config section (enabled/addr/token/owner)"
```

---

## Task 4: Extract `ingestAttachment`

**Files:**
- Modify: `bot.go` (`handleAttachment` tail → `ingestAttachment`)
- Test: `bot_test.go` (new file; uses existing `recordSender`? — no, use a fresh minimal fixture)

**Interfaces:**
- Produces: `func (b *Bot) ingestAttachment(chatID int64, savePath, caption string)` — builds the prompt (`caption` or default) + `\n\n[첨부파일: <path>]` and dispatches it. Reused by Telegram and web upload paths.
- Consumes: `b.dispatchText` (existing).

- [ ] **Step 1: Write the failing test** — create `bot_test.go`

```go
package main

import (
	"strings"
	"testing"
)

func TestIngestAttachment_BuildsPrompt(t *testing.T) {
	var got string
	b := &Bot{dispatchHook: func(_ int64, text string) { got = text }}
	b.ingestAttachment(7, "C:\\a\\file.png", "설명해줘")
	if !strings.Contains(got, "설명해줘") || !strings.Contains(got, "[첨부파일: C:\\a\\file.png]") {
		t.Errorf("prompt = %q", got)
	}
}

func TestIngestAttachment_DefaultCaption(t *testing.T) {
	var got string
	b := &Bot{dispatchHook: func(_ int64, text string) { got = text }}
	b.ingestAttachment(7, "/tmp/x.pdf", "")
	if !strings.Contains(got, "첨부파일을 분석해줘") || !strings.Contains(got, "[첨부파일: /tmp/x.pdf]") {
		t.Errorf("prompt = %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestIngestAttachment ./...`
Expected: FAIL — `dispatchHook` undefined / `ingestAttachment` undefined.

- [ ] **Step 3: Add a test seam + `ingestAttachment` in `bot.go`**

Add field to `Bot` struct (after `out *Hub`):
```go
	dispatchHook func(chatID int64, text string) // test seam; nil in production
```

Change `dispatchText` to honor the hook (replace its body, ~line 171):
```go
func (b *Bot) dispatchText(chatID int64, text string) {
	if b.dispatchHook != nil {
		b.dispatchHook(chatID, text)
		return
	}
	b.dispatch(queuedMsg{chatID: chatID, text: text})
}
```

Add `ingestAttachment` (place near `handleAttachment`, ~line 1260):
```go
// ingestAttachment builds a prompt from a saved file path + caption and dispatches
// it. Shared by the Telegram attachment path and the web upload endpoint so both
// behave identically.
func (b *Bot) ingestAttachment(chatID int64, savePath, caption string) {
	prompt := caption
	if prompt == "" {
		prompt = "첨부파일을 분석해줘"
	}
	prompt = prompt + "\n\n[첨부파일: " + savePath + "]"
	b.dispatchText(chatID, prompt)
}
```

Replace the prompt-building tail of `handleAttachment` (lines 1254-1259) with:
```go
	b.ingestAttachment(chatID, savePath, caption)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestIngestAttachment ./...`
Expected: PASS.

- [ ] **Step 5: Build + vet + full test**

Run: `GOOS=windows go build ./... && go vet ./... && go test ./...`
Expected: success.

- [ ] **Step 6: Commit**

```bash
git add bot.go bot_test.go
git commit -m "refactor(web-chat): extract ingestAttachment for shared upload path"
```

---

## Task 5: Web auth helpers + token persistence

**Files:**
- Create: `webchat.go` (auth helpers + token loader only, this task)
- Test: `webchat_test.go`

**Interfaces:**
- Produces:
  - `func loadOrCreateWebToken(cfgToken string) (string, error)` — cfgToken wins; else read/create `~/.teleclaude/web_chat.token` (0600).
  - `func tokenOK(r *http.Request, want string) bool` — token from `?token=` or `Authorization: Bearer` header; constant-time compare.
  - `func originOK(r *http.Request) bool` — true if no Origin header, or Origin host is `127.0.0.1`/`localhost`.

- [ ] **Step 1: Write the failing test** — create `webchat_test.go`

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTokenOK(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/ws?token=secret", nil)
	if !tokenOK(r, "secret") {
		t.Error("query token should match")
	}
	if tokenOK(r, "other") {
		t.Error("wrong token must fail")
	}
	r2 := httptest.NewRequest(http.MethodGet, "/api/upload", nil)
	r2.Header.Set("Authorization", "Bearer secret")
	if !tokenOK(r2, "secret") {
		t.Error("bearer token should match")
	}
	r3 := httptest.NewRequest(http.MethodGet, "/ws", nil)
	if tokenOK(r3, "secret") {
		t.Error("missing token must fail")
	}
}

func TestOriginOK(t *testing.T) {
	cases := []struct {
		origin string
		want   bool
	}{
		{"", true},
		{"http://127.0.0.1:1717", true},
		{"http://localhost:1717", true},
		{"http://localhost", true},
		{"http://evil.com", false},
		{"https://example.org:1717", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/ws", nil)
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if got := originOK(r); got != c.want {
			t.Errorf("originOK(%q)=%v, want %v", c.origin, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run 'TestTokenOK|TestOriginOK' ./...`
Expected: FAIL — undefined `tokenOK`/`originOK`.

- [ ] **Step 3: Implement the helpers in `webchat.go`**

```go
package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// loadOrCreateWebToken returns cfgToken if set, otherwise reads (or creates and
// persists) ~/.teleclaude/web_chat.token with 0600 perms.
func loadOrCreateWebToken(cfgToken string) (string, error) {
	if cfgToken != "" {
		return cfgToken, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(home, ".teleclaude", "web_chat.token")
	if b, rerr := os.ReadFile(p); rerr == nil {
		if tok := strings.TrimSpace(string(b)); tok != "" {
			return tok, nil
		}
	}
	buf := make([]byte, 24)
	if _, rerr := rand.Read(buf); rerr != nil {
		return "", rerr
	}
	tok := hex.EncodeToString(buf)
	_ = os.WriteFile(p, []byte(tok), 0o600)
	return tok, nil
}

// tokenOK checks the request token (query ?token= or "Authorization: Bearer <t>")
// against want using a constant-time comparison.
func tokenOK(r *http.Request, want string) bool {
	got := r.URL.Query().Get("token")
	if got == "" {
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			got = strings.TrimPrefix(h, "Bearer ")
		}
	}
	if got == "" || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// originOK guards against cross-site requests: allow when there is no Origin
// header (non-browser / same-origin) or when the Origin host is loopback.
func originOK(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "127.0.0.1" || host == "localhost"
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run 'TestTokenOK|TestOriginOK' ./...`
Expected: PASS.

- [ ] **Step 5: Build + vet**

Run: `GOOS=windows go build ./... && go vet ./...`
Expected: success.

- [ ] **Step 6: Commit**

```bash
git add webchat.go webchat_test.go
git commit -m "feat(web-chat): token persistence + token/origin auth helpers"
```

---

## Task 6: Web server, WebSocket channel, embedded UI

**Files:**
- Modify: `webchat.go` (server, `/ws`, `webChannel`, index/static, inject)
- Create: `web/index.html`, `web/app.js`, `web/style.css`
- Test: `webchat_test.go` (inject routing test)

**Interfaces:**
- Consumes: `Hub` (`Register`/`Unregister`), `Bot.handleCommand`, `Bot.dispatchText`, `tokenOK`, `originOK`.
- Produces:
  - `type webServer struct { addr, token string; ownerChatID int64; hub *Hub; bot *Bot }`
  - `func (s *webServer) Start()` — binds `s.addr` (loopback), serves UI + `/ws` + later `/api/upload`.
  - `func (s *webServer) inject(text string)` — `!`→`handleCommand`, else `dispatchText`, under `ownerChatID`.
  - `type webChannel struct{...}` implementing `ChannelSender` over a WebSocket.

- [ ] **Step 1: Add the dependency**

Run: `go get github.com/coder/websocket`
Expected: `go.mod`/`go.sum` updated with `github.com/coder/websocket`.

- [ ] **Step 2: Write the failing test** — append to `webchat_test.go`

```go
func TestWebInjectRouting(t *testing.T) {
	var gotCmd, gotText string
	b := &Bot{}
	b.out = NewHub()
	b.commandHook = func(_ int64, text string) { gotCmd = text }
	b.dispatchHook = func(_ int64, text string) { gotText = text }
	s := &webServer{ownerChatID: 7, bot: b, hub: b.out}

	s.inject("!help")
	s.inject("hello world")

	if gotCmd != "!help" {
		t.Errorf("command not routed to handleCommand, got %q", gotCmd)
	}
	if gotText != "hello world" {
		t.Errorf("text not routed to dispatchText, got %q", gotText)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test -run TestWebInjectRouting ./...`
Expected: FAIL — `commandHook`/`webServer`/`inject` undefined.

- [ ] **Step 4: Add `commandHook` seam to `Bot`** (`bot.go`, next to `dispatchHook`)

```go
	commandHook  func(chatID int64, text string) // test seam; nil in production
```

Wrap `handleCommand`'s entry (`bot.go` ~line 251) to honor the hook:
```go
func (b *Bot) handleCommand(chatID int64, text string) {
	if b.commandHook != nil {
		b.commandHook(chatID, text)
		return
	}
	// ... existing body unchanged ...
```

- [ ] **Step 5: Implement server + inject + webChannel + UI serving in `webchat.go`**

Add imports to the existing `webchat.go` import block: `"context"`, `"embed"`, `"encoding/base64"`, `"io/fs"`, `"log"`, `"net"`, `"sync"`, `"time"`, `"github.com/coder/websocket"`, `"github.com/coder/websocket/wsjson"`.

```go
//go:embed web
var webFS embed.FS

type webServer struct {
	addr        string
	token       string
	ownerChatID int64
	hub         *Hub
	bot         *Bot
}

// wsFrame is the JSON envelope sent to browsers.
type wsFrame struct {
	Type    string `json:"type"`              // "text" | "image" | "typing"
	Text    string `json:"text,omitempty"`
	Caption string `json:"caption,omitempty"`
	Data    string `json:"data,omitempty"` // base64 PNG for images
}

// inMsg is a message from the browser.
type inMsg struct {
	Type string `json:"type"` // "send"
	Text string `json:"text"`
}

// webChannel is one browser connection as a ChannelSender. Frames go through a
// buffered channel drained by a writer goroutine; if the buffer fills (slow
// client) the connection is closed rather than blocking the Hub.
type webChannel struct {
	send      chan wsFrame
	closeOnce sync.Once
	cancel    context.CancelFunc
}

func (w *webChannel) push(f wsFrame) {
	select {
	case w.send <- f:
	default:
		w.close()
	}
}
func (w *webChannel) close() { w.closeOnce.Do(func() { w.cancel() }) }

func (w *webChannel) Send(_ int64, text string) error { w.push(wsFrame{Type: "text", Text: text}); return nil }
func (w *webChannel) SendPhoto(_ int64, png []byte, caption string) error {
	w.push(wsFrame{Type: "image", Caption: caption, Data: base64.StdEncoding.EncodeToString(png)})
	return nil
}
func (w *webChannel) Typing(_ int64) { w.push(wsFrame{Type: "typing"}) }

// inject feeds a browser message into the same pipeline Telegram uses.
func (s *webServer) inject(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if strings.HasPrefix(text, "!") {
		s.bot.handleCommand(s.ownerChatID, text)
	} else {
		s.bot.dispatchText(s.ownerChatID, text)
	}
}

func (s *webServer) authOK(r *http.Request) bool { return originOK(r) && tokenOK(r, s.token) }

func (s *webServer) handleWS(w http.ResponseWriter, r *http.Request) {
	if !s.authOK(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"127.0.0.1:*", "localhost:*"},
	})
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch := &webChannel{send: make(chan wsFrame, 64), cancel: cancel}
	s.hub.Register(s.ownerChatID, ch)
	defer s.hub.Unregister(s.ownerChatID, ch)
	defer c.Close(websocket.StatusNormalClosure, "")

	// Writer goroutine.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case f := <-ch.send:
				wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
				werr := wsjson.Write(wctx, c, f)
				wcancel()
				if werr != nil {
					cancel()
					return
				}
			}
		}
	}()

	// Reader loop (blocks until disconnect).
	for {
		var m inMsg
		if rerr := wsjson.Read(ctx, c, &m); rerr != nil {
			break
		}
		if m.Type == "send" {
			go s.inject(m.Text)
		}
	}
	cancel()
}

func (s *webServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	b, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "ui missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

func (s *webServer) Start() {
	staticSub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Printf("[webchat] embed error: %v — web chat disabled", err)
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/api/upload", s.handleUpload) // implemented in Task 7
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))
	mux.HandleFunc("/", s.handleIndex)

	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		log.Printf("[webchat] listen %s failed: %v — web chat disabled", s.addr, err)
		return
	}
	log.Printf("[webchat] http://%s/?token=%s", s.addr, s.token)
	srv := &http.Server{Handler: mux}
	if serr := srv.Serve(ln); serr != nil {
		log.Printf("[webchat] server stopped: %v", serr)
	}
}
```

Note: `handleUpload` is referenced here but implemented in Task 7. To keep this task building on its own, add a temporary stub at the end of `webchat.go` now and replace it in Task 7:
```go
// handleUpload is implemented in Task 7.
func (s *webServer) handleUpload(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}
```

- [ ] **Step 6: Create `web/index.html`**

```html
<!doctype html>
<html lang="ko">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>teleclaude</title>
  <link rel="stylesheet" href="/static/style.css" />
</head>
<body>
  <header><span id="status" class="off">연결 끊김</span> teleclaude web chat</header>
  <main id="log"></main>
  <form id="composer">
    <input id="input" type="text" autocomplete="off" placeholder="메시지 또는 !명령…" />
    <input id="file" type="file" />
    <button type="submit">전송</button>
  </form>
  <script src="/static/app.js"></script>
</body>
</html>
```

- [ ] **Step 7: Create `web/style.css`**

```css
* { box-sizing: border-box; }
body { margin: 0; font-family: system-ui, sans-serif; display: flex; flex-direction: column; height: 100vh; }
header { padding: 8px 12px; background: #1e293b; color: #fff; font-weight: 600; }
#status { font-size: 12px; padding: 2px 6px; border-radius: 4px; margin-right: 8px; }
#status.on { background: #16a34a; }
#status.off { background: #dc2626; }
#log { flex: 1; overflow-y: auto; padding: 12px; display: flex; flex-direction: column; gap: 8px; background: #f8fafc; }
.msg { max-width: 80%; padding: 8px 10px; border-radius: 10px; white-space: pre-wrap; word-break: break-word; }
.msg.user { align-self: flex-end; background: #2563eb; color: #fff; }
.msg.assistant { align-self: flex-start; background: #e2e8f0; color: #0f172a; }
.msg.system { align-self: center; background: #fef3c7; color: #92400e; font-size: 13px; }
.msg img { max-width: 100%; border-radius: 6px; display: block; }
#composer { display: flex; gap: 6px; padding: 8px; border-top: 1px solid #cbd5e1; }
#input { flex: 1; padding: 8px; border: 1px solid #cbd5e1; border-radius: 6px; }
#file { max-width: 160px; }
button { padding: 8px 14px; border: 0; border-radius: 6px; background: #2563eb; color: #fff; cursor: pointer; }
```

- [ ] **Step 8: Create `web/app.js`**

```javascript
(function () {
  // Token: from ?token= (persist to localStorage) or previously stored.
  const params = new URLSearchParams(location.search);
  let token = params.get("token");
  if (token) localStorage.setItem("tc_token", token);
  else token = localStorage.getItem("tc_token") || "";

  const log = document.getElementById("log");
  const statusEl = document.getElementById("status");
  const form = document.getElementById("composer");
  const input = document.getElementById("input");
  const fileEl = document.getElementById("file");
  let ws, backoff = 500;

  function add(role, text) {
    const d = document.createElement("div");
    d.className = "msg " + role;
    d.textContent = text;
    log.appendChild(d);
    log.scrollTop = log.scrollHeight;
    return d;
  }
  function addImage(caption, b64) {
    const d = document.createElement("div");
    d.className = "msg assistant";
    if (caption) { const c = document.createElement("div"); c.textContent = caption; d.appendChild(c); }
    const img = document.createElement("img");
    img.src = "data:image/png;base64," + b64;
    d.appendChild(img);
    log.appendChild(d);
    log.scrollTop = log.scrollHeight;
  }

  function connect() {
    const scheme = location.protocol === "https:" ? "wss" : "ws";
    ws = new WebSocket(`${scheme}://${location.host}/ws?token=${encodeURIComponent(token)}`);
    ws.onopen = () => { statusEl.textContent = "연결됨"; statusEl.className = "on"; backoff = 500; };
    ws.onclose = () => {
      statusEl.textContent = "연결 끊김"; statusEl.className = "off";
      setTimeout(connect, backoff);
      backoff = Math.min(backoff * 2, 10000);
    };
    ws.onmessage = (ev) => {
      let f; try { f = JSON.parse(ev.data); } catch { return; }
      if (f.type === "text") add("assistant", f.text);
      else if (f.type === "image") addImage(f.caption || "", f.data);
      // "typing" frames are ignored for v1 (no indicator element).
    };
  }

  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    if (fileEl.files.length > 0) {
      const fd = new FormData();
      fd.append("file", fileEl.files[0]);
      fd.append("caption", input.value.trim());
      add("user", "📎 " + fileEl.files[0].name + (input.value.trim() ? " — " + input.value.trim() : ""));
      await fetch("/api/upload", { method: "POST", headers: { Authorization: "Bearer " + token }, body: fd });
      fileEl.value = ""; input.value = "";
      return;
    }
    const text = input.value.trim();
    if (!text || !ws || ws.readyState !== WebSocket.OPEN) return;
    add("user", text);
    ws.send(JSON.stringify({ type: "send", text }));
    input.value = "";
  });

  connect();
})();
```

- [ ] **Step 9: Run test to verify it passes**

Run: `go test -run TestWebInjectRouting ./...`
Expected: PASS.

- [ ] **Step 10: Build + vet + full test**

Run: `GOOS=windows go build ./... && GOOS=linux go build ./... && go vet ./... && go test ./...`
Expected: success (embedded `web/` files are picked up by `//go:embed web`).

- [ ] **Step 11: Commit**

```bash
git add webchat.go webchat_test.go bot.go web/ go.mod go.sum
git commit -m "feat(web-chat): http server + WebSocket channel + embedded chat UI"
```

---

## Task 7: File upload endpoint

**Files:**
- Modify: `webchat.go` (replace `handleUpload` stub)
- Test: `webchat_test.go` (multipart upload → ingest)

**Interfaces:**
- Consumes: `Bot.ingestAttachment` (Task 4), `authOK`/`originOK`.
- Produces: real `POST /api/upload` — saves the uploaded file to `~/.teleclaude/attachments/<unixmilli><ext>` and calls `ingestAttachment(ownerChatID, path, caption)`.

- [ ] **Step 1: Write the failing test** — append to `webchat_test.go`

```go
import (
	"bytes"
	"mime/multipart"
	// (add these to the existing import block)
)

func TestHandleUpload_Ingests(t *testing.T) {
	var gotText string
	b := &Bot{}
	b.out = NewHub()
	b.dispatchHook = func(_ int64, text string) { gotText = text }
	s := &webServer{ownerChatID: 7, token: "secret", bot: b, hub: b.out}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("caption", "이거 봐줘")
	fw, _ := mw.CreateFormFile("file", "note.txt")
	_, _ = fw.Write([]byte("hello"))
	_ = mw.Close()

	r := httptest.NewRequest(http.MethodPost, "/api/upload?token=secret", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	s.handleUpload(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	if !strings.Contains(gotText, "이거 봐줘") || !strings.Contains(gotText, "note.txt") == false {
		// prompt must contain caption and the saved path (which ends in .txt)
	}
	if !strings.Contains(gotText, "이거 봐줘") || !strings.Contains(gotText, ".txt]") {
		t.Errorf("ingest prompt = %q", gotText)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestHandleUpload ./...`
Expected: FAIL — stub returns 501, `w.Code` mismatch.

- [ ] **Step 3: Replace the `handleUpload` stub in `webchat.go`**

Add imports: `"fmt"`, `"io"`, `"os"` (os/path/filepath already imported from Task 5). Then:

```go
func (s *webServer) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.authOK(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "no file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	home, err := os.UserHomeDir()
	if err != nil {
		http.Error(w, "no home", http.StatusInternalServerError)
		return
	}
	dir := filepath.Join(home, ".teleclaude", "attachments")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		http.Error(w, "mkdir failed", http.StatusInternalServerError)
		return
	}
	ext := filepath.Ext(hdr.Filename)
	savePath := filepath.Join(dir, fmt.Sprintf("%d%s", time.Now().UnixMilli(), ext))
	out, err := os.Create(savePath)
	if err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	defer out.Close()
	if _, err := io.Copy(out, file); err != nil {
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	s.bot.ingestAttachment(s.ownerChatID, savePath, r.FormValue("caption"))
	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestHandleUpload ./...`
Expected: PASS.

- [ ] **Step 5: Build + vet + full test**

Run: `GOOS=windows go build ./... && go vet ./... && go test ./...`
Expected: success.

- [ ] **Step 6: Commit**

```bash
git add webchat.go webchat_test.go
git commit -m "feat(web-chat): /api/upload → ingestAttachment"
```

---

## Task 8: Wire the web server into `main.go`

**Files:**
- Modify: `main.go` (start `webServer` when `web_chat.enabled`)

**Interfaces:**
- Consumes: `loadOrCreateWebToken`, `webServer`, `Bot.Hub()`.

- [ ] **Step 1: Add the startup block in `main.go`** — insert immediately after `bot := NewBot(...)` (line 236) and before the scheduler wiring:

```go
	// Web chat transport (localhost only). Starts alongside Telegram; both share
	// state via the same Hub + owner chatID. Failure here never blocks the bot.
	if cfg.WebChat {
		owner := cfg.WebChatOwnerChatID
		if owner == 0 && len(cfg.AllowedUserIDs) > 0 {
			owner = cfg.AllowedUserIDs[0]
		}
		addr := cfg.WebChatAddr
		if addr == "" {
			addr = "127.0.0.1:1717"
		}
		if tok, terr := loadOrCreateWebToken(cfg.WebChatToken); terr != nil {
			log.Printf("[webchat] token init failed: %v — web chat disabled", terr)
		} else if owner == 0 {
			log.Printf("[webchat] no owner chatID (set web_chat.owner_chat_id or allowed_user_ids) — web chat disabled")
		} else {
			ws := &webServer{addr: addr, token: tok, ownerChatID: owner, hub: bot.Hub(), bot: bot}
			go ws.Start()
		}
	}
```

- [ ] **Step 2: Build + vet + full test**

Run: `GOOS=windows go build ./... && GOOS=linux go build ./... && go vet ./... && go test ./...`
Expected: success.

- [ ] **Step 3: Manual verification** (Windows)

```bash
# In ~/.teleclaude/config.yaml add:
#   web_chat:
#     enabled: true
#     addr: "127.0.0.1:1717"
# Then build + run:
GOOS=windows go build -o teleclaude_new.exe .
```
Start it, watch the log for `[webchat] http://127.0.0.1:1717/?token=...`, open that URL in a browser. Verify: (1) status turns "연결됨", (2) sending a plain message shows a user bubble + assistant reply, (3) the same reply also arrives in Telegram, (4) a `!help` command returns the help text, (5) an uploaded image/file triggers a worker turn, (6) a screen-tool image (if screen control on) renders inline.

Expected: all six behaviors confirmed. If the reply appears only in one channel, check that Telegram was registered as a global channel (Task 2, `NewBot`) and the web channel under the same `ownerChatID`.

- [ ] **Step 4: Commit**

```bash
git add main.go
git commit -m "feat(web-chat): start localhost web server alongside Telegram"
```

---

## Self-Review

**Spec coverage:**
- §2 architecture (Hub + telegram global + web per-chat) → Tasks 1, 2. ✓
- §3.1 Hub/ChannelSender/telegramChannel → Task 1. ✓
- §3.2 web server + routes + coder/websocket + fail-soft → Task 6, 8. ✓
- §3.3 input injection + ingestAttachment → Tasks 4, 6, 7. ✓
- §3.4 embedded UI → Task 6. ✓
- §3.5 web_chat config → Task 3. ✓
- §4 data flow (startup, connect, send, fan-out, upload) → Tasks 6, 7, 8. ✓
- §5 auth (127.0.0.1 bind, token file, Origin, owner) → Tasks 5, 6, 8. ✓
- §6 error handling (WS disconnect unregister, channel isolation, backpressure, port-in-use, 401) → Tasks 1, 6, 8. ✓
- §7 testing → Tasks 1,3,4,5,6,7. ✓
- §8 scope boundaries respected (no TLS/multiuser/framework). ✓

**Type consistency:** `ChannelSender` (Send/SendPhoto/Typing) is implemented by `telegramChannel` (Task 1), `Hub` (Task 1), `webChannel` (Task 6), and the `recCh` test double (Task 1) — all four match the exact signatures. `webServer` fields (`addr, token, ownerChatID, hub, bot`) are consistent across Tasks 6, 7, 8. `wsFrame`/`inMsg` defined once (Task 6). `ingestAttachment(chatID, savePath, caption)` signature matches its caller in Task 7.

**Placeholder scan:** No TBD/TODO; the only stub (`handleUpload`, Task 6) is explicitly replaced in Task 7 with full code and the reason stated. All code steps include complete code.
