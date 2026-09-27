package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ReloadHooks are invoked by applyReload when specific fields change.
type ReloadHooks struct {
	OnRateLimit      func(int)    // new rate limit
	OnTokenChanged   func()       // bot token changed (needs restart)
	OnScreenControl  func(bool)   // screen_control.enabled toggled
	OnKeepAwake      func(bool)   // screen_control.keep_awake toggled
	OnDefaultBackend func(string) // backend.default changed — switch the live active backend now
	OnNeedRestart    func(string) // a startup-only field changed (see configNeedsRestart) — restart to apply
	Notify           func(string)
}

// configNeedsRestart reports whether the change between old and nw touches a
// field that is only wired at process startup — long-lived objects that a live
// config swap cannot rebuild: the ConPTY interactive-claude client, the Telegram
// poller, and the spawned helper binaries / bound addresses. The value fields
// themselves already hot-apply (code reads holder.Get() live); only these
// boot-constructed objects need a restart. reason lists the changed fields.
func configNeedsRestart(old, nw *Config) (bool, string) {
	checks := []struct {
		changed bool
		name    string
	}{
		{old.InteractiveClaude != nw.InteractiveClaude, "interactive_claude"},
		{old.TelegramBotToken != nw.TelegramBotToken, "telegram 토큰"},
		{old.ScreenBinaryPath != nw.ScreenBinaryPath, "screen 바이너리 경로"},
		{old.WebBinaryPath != nw.WebBinaryPath, "web 바이너리 경로"},
		{old.AglinkChat != nw.AglinkChat, "aglink_chat 사용여부"},
		{old.AglinkChatBinaryPath != nw.AglinkChatBinaryPath, "aglink_chat 바이너리 경로"},
		{old.AglinkChatAddr != nw.AglinkChatAddr, "aglink_chat 주소"},
		{old.ChatControlAddr != nw.ChatControlAddr, "control 주소"},
		{old.WebChatAddr != nw.WebChatAddr, "web chat 주소"},
	}
	var changed []string
	for _, c := range checks {
		if c.changed {
			changed = append(changed, c.name)
		}
	}
	return len(changed) > 0, strings.Join(changed, ", ")
}

// applyReload compares old vs new config and fires the relevant hooks.
func applyReload(old, nw *Config, h ReloadHooks) {
	if old.RateLimitPerMin != nw.RateLimitPerMin && h.OnRateLimit != nil {
		h.OnRateLimit(nw.RateLimitPerMin)
	}
	if old.TelegramBotToken != nw.TelegramBotToken && h.OnTokenChanged != nil {
		h.OnTokenChanged()
	}
	if old.ScreenControl != nw.ScreenControl && h.OnScreenControl != nil {
		h.OnScreenControl(nw.ScreenControl)
	}
	if old.ScreenKeepAwake != nw.ScreenKeepAwake && h.OnKeepAwake != nil {
		h.OnKeepAwake(nw.ScreenKeepAwake)
	}
	if old.DefaultBackend != nw.DefaultBackend && h.OnDefaultBackend != nil {
		h.OnDefaultBackend(nw.DefaultBackend)
	}
	if h.OnNeedRestart != nil {
		if need, reason := configNeedsRestart(old, nw); need {
			h.OnNeedRestart(reason)
		}
	}
}

// ConfigApplier re-reads the config file and swaps it into the live holder,
// firing the reload hooks for whatever changed. Everything that reloads config
// goes through one applier: the fsnotify watcher below, and — synchronously —
// every writer that persists config.yaml (settings form, raw editor, MCP list).
// The writers matter because the UI refetches the settings right after a save;
// waiting for the watcher's 300ms debounce meant that refetch returned the
// pre-save values and the form re-rendered showing the old model.
type ConfigApplier struct {
	path   string
	holder *ConfigHolder
	hooks  ReloadHooks

	mu      sync.Mutex
	lastRaw []byte // bytes of the last successfully applied file
}

func NewConfigApplier(path string, holder *ConfigHolder, hooks ReloadHooks) *ConfigApplier {
	a := &ConfigApplier{path: path, holder: holder, hooks: hooks}
	// Seed with the bytes the running config was loaded from so an event for a
	// touch that didn't actually change anything is recognized as a no-op.
	a.lastRaw, _ = os.ReadFile(path)
	return a
}

// Apply re-reads the config file and, if its content changed since the last
// applied version, swaps it into the holder and fires the hooks. Identical
// content is a no-op — a save applies synchronously and the debounced watcher
// event for that same write lands here afterwards, which would otherwise
// re-notify every user a second time. On a parse error the previous config is
// kept and the error returned.
func (a *ConfigApplier) Apply() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	raw, rerr := os.ReadFile(a.path)
	if rerr == nil && a.lastRaw != nil && bytes.Equal(raw, a.lastRaw) {
		return nil
	}
	cfg, err := LoadConfig(a.path)
	if err != nil {
		log.Printf("[config] reload 실패: %v (이전 설정 유지)", err)
		a.notify("⚠️ 설정 reload 실패: " + err.Error() + " — 이전 설정 유지")
		return err
	}
	old := a.holder.Get()
	a.holder.Set(cfg)
	a.lastRaw = raw
	applyReload(old, cfg, a.hooks)
	log.Printf("[config] reload 적용됨")
	a.notify("⚙️ 설정이 reload되었습니다")
	return nil
}

// notify fans the message out off the caller's goroutine: Notify sends chat
// messages, and a save's control reply shouldn't wait on Telegram round-trips
// now that writers apply synchronously.
func (a *ConfigApplier) notify(msg string) {
	if a.hooks.Notify == nil {
		return
	}
	go a.hooks.Notify(msg)
}

// liveConfigApplier is the process-wide applier registered by main. Config
// writers call applyLiveConfig after a successful write so the change is live
// *before* they reply. Nil (tests, wizard) makes applyLiveConfig a no-op.
var liveConfigApplier atomic.Pointer[ConfigApplier]

// SetLiveConfigApplier registers the applier used by applyLiveConfig.
func SetLiveConfigApplier(a *ConfigApplier) { liveConfigApplier.Store(a) }

// applyLiveConfig hot-applies the config file that was just written, so the
// running process (and the very next get_settings read) sees it immediately
// instead of up to a debounce later.
func applyLiveConfig() {
	if a := liveConfigApplier.Load(); a != nil {
		_ = a.Apply()
	}
}

// WatchConfig watches the config file's directory and hot-reloads on change via
// the applier. Returns a stop func. Editor atomic-saves (temp+rename) are
// handled by watching the directory and filtering for the config file name;
// events are debounced.
func WatchConfig(a *ConfigApplier) (func(), error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	path := a.path
	dir := filepath.Dir(path)
	name := filepath.Base(path)
	if err := w.Add(dir); err != nil {
		_ = w.Close()
		return nil, err
	}

	done := make(chan struct{})
	go func() {
		var timer *time.Timer
		reload := func() {
			select {
			case <-done:
				return
			default:
			}
			_ = a.Apply()
		}
		for {
			select {
			case <-done:
				return
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				if filepath.Base(ev.Name) != name {
					continue
				}
				if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
					continue
				}
				if timer != nil {
					timer.Stop()
				}
				timer = time.AfterFunc(300*time.Millisecond, reload)
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				log.Printf("[config] watcher 오류: %v", err)
			}
		}
	}()

	return func() { close(done); _ = w.Close() }, nil
}
