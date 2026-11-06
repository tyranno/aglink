package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWithUserProfile(t *testing.T) {
	if got := withUserProfile("g", ""); got != "g" {
		t.Errorf("no profile = %q", got)
	}
	if got := withUserProfile("", "존댓말 선호"); !strings.Contains(got, "사용자 프로필") || !strings.Contains(got, "존댓말 선호") {
		t.Errorf("profile only = %q", got)
	}
	if got := withUserProfile("g", "p"); !strings.HasPrefix(got, "g\n\n") || !strings.HasSuffix(got, "p") {
		t.Errorf("both = %q", got)
	}
}

func TestSharedMemoryReminder(t *testing.T) {
	home := t.TempDir()
	t.Setenv(dataDirEnv, home)
	got := sharedMemoryReminder()
	if !strings.Contains(got, filepath.ToSlash(filepath.Join(home, "memory", "USER.md"))) {
		t.Errorf("reminder lacks the profile path: %q", got)
	}
	if !strings.Contains(got, " recall <검색어>") || !strings.Contains(got, "<system-reminder>") {
		t.Errorf("reminder lacks recall hint or wrapper: %q", got)
	}
	if st, err := os.Stat(filepath.Join(home, "memory")); err != nil || !st.IsDir() {
		t.Error("memory dir should exist so the worker can write USER.md")
	}
}

// A fresh session gets the profile and the reminder; a resumed one does not
// pay for them again.
func TestRunWorker_UserProfileOnFreshSessionOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv(dataDirEnv, home)
	_ = os.MkdirAll(filepath.Join(home, "memory"), 0o700)
	_ = os.WriteFile(filepath.Join(home, "memory", "USER.md"), []byte("- 이름: 도원\n- 존댓말 선호"), 0o600)

	fc := &fakeClaude{runRes: RunResult{Text: "done", SessionID: "sess-x"}}
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	_ = st.Load()
	dir := t.TempDir()
	_ = st.AddProject("myapp", dir)
	m := NewManager(fc, nil, st, NewConfigHolder(&Config{ManagerAlways: true, HomeDir: dir}))
	sink := m.telegramSink("myapp")
	f := &fakeSender{}

	m.runWorker(context.Background(), 1, "안녕", sink, dir, st.TelegramConversation(), f, fc, "claude")
	if p := fc.lastRun.Prompt; fc.lastRun.Resume || !strings.Contains(p, "존댓말 선호") || !strings.Contains(p, "USER.md") {
		t.Fatalf("fresh prompt lacks the profile: resume=%v %q", fc.lastRun.Resume, p)
	}

	m.runWorker(context.Background(), 1, "다음", sink, dir, st.TelegramConversation(), f, fc, "claude")
	if p := fc.lastRun.Prompt; !fc.lastRun.Resume || strings.Contains(p, "존댓말 선호") || strings.Contains(p, "USER.md") {
		t.Errorf("resumed prompt should not repeat the profile: resume=%v %q", fc.lastRun.Resume, p)
	}
}

func recallFixture(t *testing.T) recallSources {
	t.Helper()
	old := historyDirOverride
	historyDirOverride = t.TempDir()
	t.Cleanup(func() { historyDirOverride = old })

	proj := t.TempDir()
	mem := filepath.Join(proj, ".aglink", "memory")
	_ = os.MkdirAll(mem, 0o700)
	_ = os.WriteFile(filepath.Join(mem, "c1.md"), []byte("# 배포\n\nNanoPi 에는 ~/bin/aglink 로 배포한다.\n\n## 기타\n\n로그는 journalctl 로 본다."), 0o600)
	_ = os.WriteFile(filepath.Join(mem, "orphan.md"), []byte("배포 이야기 하나 더"), 0o600)
	if err := WriteHistory("myapp", "로그인 버그", "로그인이 안 돼", "세션 쿠키 만료 문제였음 — nanopi 와 무관"); err != nil {
		t.Fatal(err)
	}

	projects := map[string]*Project{"myapp": {Path: proj, Conversations: map[string]*Conversation{"c1": {ID: "c1", Title: "배포 정리"}}}}
	return buildRecallSources(projects, nil, &Conversation{ID: "telegram"}, "")
}

func TestSearchRecall(t *testing.T) {
	docs := recallDocs(recallFixture(t))

	hits := searchRecall(docs, "nanopi 배포", 5)
	if len(hits) == 0 {
		t.Fatal("no hits")
	}
	top := hits[0]
	if top.doc.kind != "메모" || top.doc.name != "myapp · 배포 정리" || top.matched != 2 || !strings.Contains(top.snippet, "NanoPi") {
		t.Errorf("top hit = %+v", top)
	}
	found := map[string]bool{}
	for _, h := range hits {
		found[h.doc.kind+"|"+h.doc.name] = true
	}
	if !found["기록|myapp · 로그인 버그"] {
		t.Errorf("history entry missing: %v", found)
	}

	if got := searchRecall(docs, "쿠버네티스", 5); len(got) != 0 {
		t.Errorf("unrelated query hit %v", got)
	}
	if got := searchRecall(docs, "   ", 5); got != nil {
		t.Errorf("empty query = %v", got)
	}

	out := formatRecall("nanopi 배포", hits)
	if !strings.Contains(out, "배포 정리") || !strings.Contains(out, "1.") {
		t.Errorf("format = %q", out)
	}
	if out := formatRecall("x", nil); !strings.Contains(out, "찾지 못했어요") {
		t.Errorf("empty format = %q", out)
	}
}

func TestRecallSnippet(t *testing.T) {
	text := strings.Repeat("가", 100) + "목표" + strings.Repeat("나", 300)
	s := recallSnippet(text, 100)
	if !strings.Contains(s, "목표") || !strings.HasPrefix(s, "…") || !strings.HasSuffix(s, "…") {
		t.Errorf("snippet = %q", s)
	}
}
