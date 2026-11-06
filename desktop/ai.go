package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// AskAI runs one tool-less claude call on this computer, apart from any chat,
// so a meeting write-up or advice neither waits for a busy conversation nor
// lands in one. It signs in the way the aglink host does (claude.oauth_token
// in ~/.aglink/config.yaml, else claude's own login).
func (v *VoiceService) AskAI(prompt string) (string, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", errors.New("빈 요청")
	}
	path, token := hostClaudeConfig()
	if path != "" && !fileExists(path) {
		path = ""
	}
	if path == "" {
		p, err := exec.LookPath("claude")
		if err != nil {
			return "", errors.New("이 컴퓨터에서 claude 를 찾지 못했어요")
		}
		path = p
	}
	path = nativeClaude(path)
	ctx, cancel := context.WithTimeout(context.Background(), askAITimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, askAIArgs()...)
	home, _ := os.UserHomeDir()
	cmd.Dir = home
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Env = os.Environ()
	if token != "" {
		cmd.Env = append(cmd.Env, "CLAUDE_CODE_OAUTH_TOKEN="+token)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	hideConsole(cmd)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", errors.New("AI 답이 너무 오래 걸려서 멈췄어요")
		}
		return "", fmt.Errorf("AI 호출 실패: %v %s", err, firstLine(errb.String()))
	}
	return parseAskAI(out.Bytes())
}

const askAITimeout = 5 * time.Minute

// nativeClaude swaps npm's claude.cmd/.ps1 shim for the claude.exe beside it:
// a .cmd runs through cmd.exe, which breaks on a path like C:\Program Files.
func nativeClaude(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".cmd" && ext != ".ps1" {
		return path
	}
	exe := filepath.Join(filepath.Dir(path), "node_modules", "@anthropic-ai", "claude-code", "bin", "claude.exe")
	if fileExists(exe) {
		return exe
	}
	return path
}

func askAIArgs() []string {
	return []string{
		"-p", "--output-format", "json", "--model", "sonnet",
		"--strict-mcp-config", "--setting-sources", "project,local",
		"--tools", "",
		"--system-prompt", "You are a concise helper inside aglink. Follow the instructions in the user message exactly.",
		"--settings", `{"alwaysThinkingEnabled":false}`,
	}
}

func parseAskAI(b []byte) (string, error) {
	var res struct {
		Result  string `json:"result"`
		IsError bool   `json:"is_error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(b), &res); err != nil {
		return "", fmt.Errorf("AI 답을 읽지 못했어요: %s", firstLine(string(b)))
	}
	if res.IsError {
		return "", fmt.Errorf("AI 오류: %s", firstLine(res.Result))
	}
	text := strings.TrimSpace(res.Result)
	if text == "" {
		return "", errors.New("AI 답이 비어 있어요")
	}
	return text, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200])
	}
	return s
}

// hostClaudeConfig reads claude.path and claude.oauth_token from the host's
// ~/.aglink/config.yaml (two plain keys, so no YAML library is needed).
func hostClaudeConfig() (path, token string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ""
	}
	f, err := os.Open(filepath.Join(home, ".aglink", "config.yaml"))
	if err != nil {
		return "", ""
	}
	defer f.Close()
	return parseClaudeConfig(bufio.NewScanner(f))
}

func parseClaudeConfig(sc *bufio.Scanner) (path, token string) {
	in := false
	for sc.Scan() {
		ln := sc.Text()
		trim := strings.TrimSpace(ln)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		if ln[0] != ' ' && ln[0] != '\t' {
			in = trim == "claude:"
			continue
		}
		if !in {
			continue
		}
		key, val, ok := strings.Cut(trim, ":")
		if !ok {
			continue
		}
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		switch key {
		case "path":
			path = val
		case "oauth_token":
			token = val
		}
	}
	return path, token
}
