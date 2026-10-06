package main

import (
	"io"
	"log"
	"os"
	"regexp"
)

// Secrets must never reach a log line. The one that did: a failed Telegram
// send logs net/http's error, which quotes the whole request URL —
// https://api.telegram.org/bot<id>:<secret>/sendMessage — so the bot token sat
// in plain text in aglink.log and the systemd journal. Anyone holding either
// could drive the bot. Rather than chase every call site that might print an
// error, everything written through the logger and the children's output
// passes through redactWriter.

// telegramTokenRE matches a Telegram bot token: the numeric bot id, a colon,
// and the 35-character secret. No leading \b: in the API URL the id follows
// "bot" directly ("/bot123…:AAH…"), with no word boundary in between.
var telegramTokenRE = regexp.MustCompile(`\d{6,}:[A-Za-z0-9_-]{30,}`)

const redactedToken = "<telegram-token>"

// redactSecrets replaces anything that looks like a bot token.
func redactSecrets(s string) string {
	return telegramTokenRE.ReplaceAllString(s, redactedToken)
}

// redactWriter rewrites each Write before passing it on. The standard logger
// hands over one whole line per Write, so a token is never split across two
// calls there; a child's raw output could split one, which is the price of
// not buffering it.
type redactWriter struct{ w io.Writer }

func (r redactWriter) Write(p []byte) (int, error) {
	if !telegramTokenRE.Match(p) {
		return r.w.Write(p)
	}
	if _, err := r.w.Write([]byte(redactSecrets(string(p)))); err != nil {
		return 0, err
	}
	// Report the caller's length: the logger treats a short count as an error.
	return len(p), nil
}

func init() {
	// Before setupFileLogging runs — or if it fails — the logger writes to
	// stderr alone, which on a Linux server is the journal. Cover that too.
	log.SetOutput(redactWriter{os.Stderr})
	childLogWriter = redactWriter{os.Stderr}
}
