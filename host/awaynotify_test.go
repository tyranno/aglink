package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type awaySent struct {
	chatID int64
	text   string
}

// newTestAway builds a notifier with an injected idle reading and an inline,
// recording Telegram sender.
func newTestAway(cfg *Config, idle time.Duration, idleOK bool) (*awayNotifier, *[]awaySent) {
	var sent []awaySent
	n := &awayNotifier{
		cfg:   func() *Config { return cfg },
		idle:  func() (time.Duration, bool) { return idle, idleOK },
		send:  func(id int64, text string) error { sent = append(sent, awaySent{id, text}); return nil },
		spawn: func(f func()) { f() },
	}
	return n, &sent
}

func awayCfg() *Config {
	return &Config{TelegramBotToken: "123:test", AllowedUserIDs: []int64{11, 22}, AwayNotify: true}
}

func TestAwayNotifier_SendsOnlyWhenIdleLongEnough(t *testing.T) {
	cases := []struct {
		name    string
		idle    time.Duration
		ok      bool
		minutes int
		want    bool
	}{
		{"active user", 30 * time.Second, true, 0, false},
		{"just under default 5m", 4*time.Minute + 59*time.Second, true, 0, false},
		{"default 5m reached", 5 * time.Minute, true, 0, true},
		{"custom 10m not reached", 7 * time.Minute, true, 10, false},
		{"custom 10m reached", 11 * time.Minute, true, 10, true},
		{"custom 1m", 70 * time.Second, true, 1, true},
		{"idle unknown (non-Windows)", time.Hour, false, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := awayCfg()
			cfg.AwayIdleMinutes = tc.minutes
			n, sent := newTestAway(cfg, tc.idle, tc.ok)
			got := n.turnDone("빌드 정리", 12*time.Minute, "끝났습니다.\n자세히는…", false)
			if got != tc.want {
				t.Fatalf("turnDone = %v, want %v", got, tc.want)
			}
			if tc.want && len(*sent) != 2 {
				t.Fatalf("every allowed user gets it: %+v", *sent)
			}
			if !tc.want && len(*sent) != 0 {
				t.Fatalf("nothing may be sent: %+v", *sent)
			}
		})
	}
}

func TestAwayNotifier_SilentWhenOffOrNoTelegram(t *testing.T) {
	off := awayCfg()
	off.AwayNotify = false
	n, sent := newTestAway(off, time.Hour, true)
	if n.turnDone("t", time.Minute, "a", false) || len(*sent) != 0 {
		t.Fatal("disabled: must not send")
	}

	noUsers := awayCfg()
	noUsers.AllowedUserIDs = nil
	n, sent = newTestAway(noUsers, time.Hour, true)
	if n.turnDone("t", time.Minute, "a", false) || len(*sent) != 0 {
		t.Fatal("no Telegram users: must not send")
	}

	// No bot (api nil) → no sender.
	n = newTelegramAwayNotifier(func() *Config { return awayCfg() }, nil)
	n.idle = func() (time.Duration, bool) { return time.Hour, true }
	if n.turnDone("t", time.Minute, "a", false) {
		t.Fatal("no Telegram bot: must not send")
	}

	// A nil notifier (never wired) is a no-op, not a panic.
	var nilN *awayNotifier
	if nilN.turnDone("t", time.Minute, "a", false) {
		t.Fatal("nil notifier must not send")
	}
}

func TestAwayNotifier_SendErrorIsSwallowed(t *testing.T) {
	n, _ := newTestAway(awayCfg(), time.Hour, true)
	calls := 0
	n.send = func(int64, string) error { calls++; return errors.New("network down") }
	if !n.turnDone("t", time.Minute, "a", false) {
		t.Fatal("decision is to send even if delivery then fails")
	}
	if calls != 2 {
		t.Fatalf("one failed user must not stop the next: calls=%d", calls)
	}
}

func TestFormatAwayMessages(t *testing.T) {
	got := formatAwayDone("배포 스크립트", 12*time.Minute+30*time.Second, "\n```go\n## **배포가 끝났습니다**\n두 번째 줄")
	want := "🔔 [배포 스크립트] 작업 끝남 · 12분 걸림 · 배포가 끝났습니다"
	if got != want {
		t.Errorf("done = %q, want %q", got, want)
	}
	if got := formatAwayDone("", 40*time.Second, ""); got != "🔔 [대화] 작업 끝남 · 40초 걸림" {
		t.Errorf("empty title/answer = %q", got)
	}
	if got := formatAwayFailed("x", 65*time.Minute, "exit status 1\nmore"); got != "⚠️ [x] 작업 실패 · 1시간 5분 걸림 · exit status 1" {
		t.Errorf("failed = %q", got)
	}
	long := strings.Repeat("가", 200)
	if got := formatAwayDone("t", time.Minute, long); !strings.HasSuffix(got, "…") || len([]rune(got)) > 120 {
		t.Errorf("long answer is not cut: %q", got)
	}
	if awayDuration(2*time.Hour) != "2시간" {
		t.Errorf("2h = %q", awayDuration(2*time.Hour))
	}
}

// Only web conversations notify: the Telegram stream already answers in
// Telegram, so a notice there would only duplicate it.
func TestManagerNotifyAwayTurn_WebOnly(t *testing.T) {
	n, sent := newTestAway(awayCfg(), time.Hour, true)
	m := &Manager{}
	m.SetAwayNotifier(n)
	c := &Conversation{ID: "c1", Title: "웹 대화"}

	m.notifyAwayTurn(m.telegramSink(""), c, time.Minute, "답", false)
	if len(*sent) != 0 {
		t.Fatalf("telegram-stream turn must not notify: %+v", *sent)
	}
	m.notifyAwayTurn(m.newWebConvSink(), c, time.Minute, "답", false)
	if len(*sent) != 2 || !strings.Contains((*sent)[0].text, "[웹 대화] 작업 끝남") {
		t.Fatalf("web turn must notify: %+v", *sent)
	}

	// Not wired → nothing, no panic.
	(&Manager{}).notifyAwayTurn(m.newWebConvSink(), c, time.Minute, "답", false)
}

func TestAwayConfigYAMLRoundTrip(t *testing.T) {
	// Default: on, 0 (→5 minutes).
	c, err := unmarshalConfigYAML([]byte("telegram:\n  bot_token: \"123:x\"\n  allowed_user_ids: [1]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.AwayNotify || c.AwayIdleMinutes != 0 || awayThreshold(c) != 5*time.Minute {
		t.Fatalf("defaults: notify=%v minutes=%d", c.AwayNotify, c.AwayIdleMinutes)
	}
	c.AwayNotify = false
	c.AwayIdleMinutes = 15
	raw, err := marshalConfigYAML(c)
	if err != nil {
		t.Fatal(err)
	}
	back, err := unmarshalConfigYAML(raw)
	if err != nil {
		t.Fatal(err)
	}
	if back.AwayNotify || back.AwayIdleMinutes != 15 {
		t.Fatalf("round trip lost the setting: notify=%v minutes=%d\n%s", back.AwayNotify, back.AwayIdleMinutes, raw)
	}
}

func TestApplySettings_AwayFields(t *testing.T) {
	cfg := awayCfg()
	if err := applySettings(cfg, map[string]any{"notify.away_telegram": false, "notify.away_idle_minutes": float64(9)}); err != nil {
		t.Fatal(err)
	}
	if cfg.AwayNotify || cfg.AwayIdleMinutes != 9 {
		t.Fatalf("applySettings: notify=%v minutes=%d", cfg.AwayNotify, cfg.AwayIdleMinutes)
	}
	found := 0
	for _, s := range buildSettings(cfg, nil) {
		for _, f := range s.Fields {
			if f.Key == "notify.away_telegram" || f.Key == "notify.away_idle_minutes" {
				found++
			}
		}
	}
	if found != 2 {
		t.Fatalf("settings form must expose both fields, found %d", found)
	}
}

func TestApprovalConvTitle(t *testing.T) {
	if (&Bot{}).approvalConvTitle("chat:1/conv:abc") != "" {
		t.Fatal("no store → empty")
	}
}

func TestApprovalConvTitle_FromWebConv(t *testing.T) {
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	c, err := st.NewWebConv("리포트 정리")
	if err != nil {
		t.Fatal(err)
	}
	b := &Bot{store: st}
	if got := b.approvalConvTitle("chat:7/conv:" + c.ID); got != "리포트 정리" {
		t.Fatalf("title = %q", got)
	}
	if got := b.approvalConvTitle("chat:7/conv:nope"); got != "" {
		t.Fatalf("unknown conversation = %q", got)
	}
	if got := b.approvalConvTitle(""); got != "" {
		t.Fatalf("no session = %q", got)
	}
}
