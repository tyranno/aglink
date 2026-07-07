package main

import "strings"

// configSecretSentinel replaces every non-empty secret value when config.yaml is
// sent to the browser, and is swapped back to the stored secret on save. Exact
// string replacement (not YAML-structural) so comments/formatting survive.
const configSecretSentinel = "●●●●●●●● (unchanged)"

// configSecrets returns the current non-empty secret values, longest first so a
// secret that is a substring of another is masked correctly.
func configSecrets(c *Config) []string {
	if c == nil {
		return nil
	}
	cand := []string{c.TelegramBotToken, c.ClaudeOauthToken, c.WebChatToken, c.ChatControlToken}
	out := make([]string, 0, len(cand))
	seen := map[string]bool{}
	for _, s := range cand {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if len(out[j]) > len(out[i]) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// maskConfigSecrets replaces every stored secret value in raw with the sentinel.
func maskConfigSecrets(raw []byte, c *Config) []byte {
	s := string(raw)
	for _, secret := range configSecrets(c) {
		s = strings.ReplaceAll(s, secret, configSecretSentinel)
	}
	return []byte(s)
}

// restoreConfigSecrets swaps each sentinel back to the stored secret it stands
// for. All sentinels are the same literal, so we resolve per-line by the YAML key
// (and section for the ambiguous "token" key used by both web_chat and
// chat_control). A line the user actually edited (no sentinel) is left as typed.
func restoreConfigSecrets(edited []byte, c *Config) []byte {
	s := string(edited)
	if !strings.Contains(s, configSecretSentinel) {
		return []byte(s)
	}
	byKey := map[string]string{
		"bot_token":   c.TelegramBotToken,
		"oauth_token": c.ClaudeOauthToken,
	}
	lines := strings.Split(s, "\n")
	section := ""
	for i, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		switch {
		case trimmed == "web_chat:":
			section = "web_chat"
		case trimmed == "chat_control:":
			section = "chat_control"
		case trimmed != "" && !strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(ln, " ") && !strings.HasPrefix(ln, "\t"):
			section = "" // left the indented block (a new top-level key)
		}
		if !strings.Contains(ln, configSecretSentinel) {
			continue
		}
		key := ""
		if idx := strings.Index(trimmed, ":"); idx > 0 {
			key = strings.TrimSpace(trimmed[:idx])
		}
		repl := byKey[key]
		if key == "token" {
			if section == "chat_control" {
				repl = c.ChatControlToken
			} else {
				repl = c.WebChatToken
			}
		}
		if repl != "" {
			lines[i] = strings.ReplaceAll(ln, configSecretSentinel, repl)
		}
	}
	return []byte(strings.Join(lines, "\n"))
}
