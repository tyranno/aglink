package main

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

// A made-up token in the real format: numeric id, colon, 35-char secret.
const fakeBotToken = "1234567890:AAH0abcdefghijklmnopqrstuvwxyz12345"

func TestRedactSecretsHidesBotTokenInURL(t *testing.T) {
	in := `[tg] send error: Post "https://api.telegram.org/bot` + fakeBotToken + `/sendMessage": read: connection reset by peer`
	out := redactSecrets(in)
	if strings.Contains(out, "AAH0abcdefghij") || strings.Contains(out, "1234567890:") {
		t.Fatalf("token survived: %s", out)
	}
	if !strings.Contains(out, "api.telegram.org/bot<telegram-token>/sendMessage") || !strings.Contains(out, "connection reset by peer") {
		t.Fatalf("the rest of the line must stay readable: %s", out)
	}
}

func TestRedactSecretsLeavesOrdinaryLinesAlone(t *testing.T) {
	for _, s := range []string{
		"2026/10/06 23:53:40 [main] allowlist: [6723802240], backend=claude",
		"[worker] ✅ backend=claude elapsed=1m19.142113487s output=284 bytes",
		"time 12:30:45 and ratio 16:9",
	} {
		if got := redactSecrets(s); got != s {
			t.Errorf("changed %q to %q", s, got)
		}
	}
}

func TestLoggerOutputIsRedacted(t *testing.T) {
	var buf bytes.Buffer
	l := log.New(redactWriter{&buf}, "", 0)
	l.Printf("[hub] channel send error: Post %q: EOF", "https://api.telegram.org/bot"+fakeBotToken+"/sendMessage")
	if strings.Contains(buf.String(), fakeBotToken) {
		t.Fatalf("logged the token: %s", buf.String())
	}
}
