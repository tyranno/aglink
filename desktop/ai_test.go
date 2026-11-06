package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseClaudeConfig(t *testing.T) {
	yml := "manager:\n    path: nope\nclaude:\n    path: \"C:\\x\\claude.exe\"\n    # comment\n    oauth_token: sk-abc\nbackend:\n    oauth_token: other\n"
	path, token := parseClaudeConfig(bufio.NewScanner(strings.NewReader(yml)))
	if path != `C:\x\claude.exe` || token != "sk-abc" {
		t.Fatalf("got %q %q", path, token)
	}
}

func TestParseAskAI(t *testing.T) {
	if s, err := parseAskAI([]byte(`{"result":"  # 제목\n분류: 기타 ","is_error":false}`)); err != nil || s != "# 제목\n분류: 기타" {
		t.Fatalf("got %q %v", s, err)
	}
	if _, err := parseAskAI([]byte(`{"result":"Invalid API key","is_error":true}`)); err == nil {
		t.Fatal("is_error should fail")
	}
	if _, err := parseAskAI([]byte("not json")); err == nil {
		t.Fatal("bad output should fail")
	}
}

func TestNativeClaude(t *testing.T) {
	dir := t.TempDir()
	shim := filepath.Join(dir, "claude.cmd")
	if got := nativeClaude(shim); got != shim {
		t.Fatalf("no exe beside it: %s", got)
	}
	exe := filepath.Join(dir, "node_modules", "@anthropic-ai", "claude-code", "bin", "claude.exe")
	_ = os.MkdirAll(filepath.Dir(exe), 0o755)
	_ = os.WriteFile(exe, nil, 0o755)
	if got := nativeClaude(shim); got != exe {
		t.Fatalf("got %s", got)
	}
	if got := nativeClaude(exe); got != exe {
		t.Fatalf("exe kept: %s", got)
	}
}
