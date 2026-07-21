package main

import (
	"log"
	"path/filepath"
	"strings"
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

// WatchConfig watches the config file's directory and hot-reloads on change.
// Returns a stop func. Editor atomic-saves (temp+rename) are handled by watching
// the directory and filtering for the config file name; events are debounced.
func WatchConfig(path string, holder *ConfigHolder, hooks ReloadHooks) (func(), error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
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
			cfg, err := LoadConfig(path)
			if err != nil {
				log.Printf("[config] reload 실패: %v (이전 설정 유지)", err)
				if hooks.Notify != nil {
					hooks.Notify("⚠️ 설정 reload 실패: " + err.Error() + " — 이전 설정 유지")
				}
				return
			}
			old := holder.Get()
			holder.Set(cfg)
			applyReload(old, cfg, hooks)
			log.Printf("[config] reload 적용됨")
			if hooks.Notify != nil {
				hooks.Notify("⚙️ 설정이 reload되었습니다")
			}
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
