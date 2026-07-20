package main

import "testing"

func TestYAMLRoundTrip(t *testing.T) {
	c := &Config{
		HomeDir:             "C:\\tools\\aglink-home",
		TelegramBotToken:    "123:ABC",
		AllowedUserIDs:      []int64{111, 222},
		AllowedUsernames:    []string{"alice"},
		ManagerModel:        "haiku",
		WorkerModel:         "sonnet",
		ManagerAlways:       false,
		ClaudePath:          "",
		ClaudeOauthToken:    "sk-ant-oat01-X",
		DefaultBackend:      "claude",
		CodexModel:          "o4-mini",
		TimeoutMinutes:      10,
		MaxWorkers:          3,
		RateLimitPerMin:     20,
		AllowScripts:        false,
		ScreenControl:       true,
		ScreenPresetsFile:   "",
		ScreenElevated:      true,
		ScreenKeepAwake:     true,
		ScreenBinaryPath:    "C:\\tools\\aglink-screen.exe",
		WebControl:          true,
		WebBinaryPath:       "C:\\tools\\aglink-web.exe",
		ConversationTTLDays: 45,
		WebChat:             true,
		WebChatAddr:         "127.0.0.1:27271",
		WebChatToken:        "tok-abc",
		WebChatOwnerChatID:  6723802240,
	}
	b, err := marshalConfigYAML(c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := unmarshalConfigYAML(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.HomeDir != c.HomeDir ||
		got.TelegramBotToken != c.TelegramBotToken ||
		len(got.AllowedUserIDs) != 2 || got.AllowedUserIDs[1] != 222 ||
		got.WorkerModel != "sonnet" || got.ManagerAlways != false ||
		got.ClaudeOauthToken != "sk-ant-oat01-X" || got.DefaultBackend != "claude" ||
		got.MaxWorkers != 3 || got.RateLimitPerMin != 20 || got.ScreenControl != true ||
		got.ScreenElevated != true || got.ScreenKeepAwake != true ||
		got.ScreenBinaryPath != "C:\\tools\\aglink-screen.exe" ||
		got.WebControl != true || got.WebBinaryPath != "C:\\tools\\aglink-web.exe" ||
		got.ConversationTTLDays != 45 ||
		got.WebChat != true || got.WebChatAddr != "127.0.0.1:27271" ||
		got.WebChatToken != "tok-abc" || got.WebChatOwnerChatID != 6723802240 {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

func TestYAMLDefaults(t *testing.T) {
	// Minimal YAML → defaults applied, validate passes.
	y := []byte("telegram:\n  bot_token: t\n  allowed_user_ids: [1]\n")
	got, err := unmarshalConfigYAML(y)
	if err != nil {
		t.Fatal(err)
	}
	if got.ManagerModel != "haiku" || got.TimeoutMinutes != 10 || got.MaxWorkers != 3 ||
		got.RateLimitPerMin != 20 || got.ManagerAlways != true || got.ConversationTTLDays != 30 {
		t.Errorf("defaults wrong: %+v", got)
	}
}

// TestConfigDefaultWebChatAddr verifies that omitting web_chat.addr falls back to the
// documented default 127.0.0.1:27271, while an explicitly-set addr is preserved as-is.
func TestConfigDefaultWebChatAddr(t *testing.T) {
	y := []byte("telegram:\n  bot_token: t\n  allowed_user_ids: [1]\nweb_chat:\n  enabled: true\n")
	got, err := unmarshalConfigYAML(y)
	if err != nil {
		t.Fatal(err)
	}
	if !got.WebChat {
		t.Errorf("expected WebChat enabled, got %+v", got)
	}
	if got.WebChatAddr != "127.0.0.1:27271" {
		t.Errorf("expected default addr 127.0.0.1:27271, got %q", got.WebChatAddr)
	}

	y2 := []byte("telegram:\n  bot_token: t\n  allowed_user_ids: [1]\nweb_chat:\n  enabled: true\n  addr: 0.0.0.0:9999\n")
	got2, err := unmarshalConfigYAML(y2)
	if err != nil {
		t.Fatal(err)
	}
	if got2.WebChatAddr != "0.0.0.0:9999" {
		t.Errorf("expected explicit addr to survive, got %q", got2.WebChatAddr)
	}
}

// TestAglinkChatImpliesChatControl verifies that enabling only aglink_chat also
// turns on the control API it depends on (previously both had to be set).
func TestAglinkChatImpliesChatControl(t *testing.T) {
	y := []byte("telegram:\n  bot_token: t\n  allowed_user_ids: [1]\naglink_chat:\n  enabled: true\n")
	got, err := unmarshalConfigYAML(y)
	if err != nil {
		t.Fatal(err)
	}
	if !got.AglinkChat {
		t.Fatalf("expected AglinkChat enabled, got %+v", got)
	}
	if !got.ChatControl {
		t.Errorf("aglink_chat.enabled should imply ChatControl, got ChatControl=false")
	}
	// Sanity: not enabling aglink_chat leaves ChatControl untouched (off here).
	y2 := []byte("telegram:\n  bot_token: t\n  allowed_user_ids: [1]\n")
	got2, err := unmarshalConfigYAML(y2)
	if err != nil {
		t.Fatal(err)
	}
	if got2.ChatControl {
		t.Errorf("ChatControl should stay off when neither chat_control nor aglink_chat is set")
	}
}
