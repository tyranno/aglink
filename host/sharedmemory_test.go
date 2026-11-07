package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestSearchRecall_SpokenForms(t *testing.T) {
	docs := recallDocs(recallFixture(t))

	// Particles and endings on the query words: "배포를" → 배포, "로그인했어" → 로그인.
	hits := searchRecall(docs, "nanopi에 배포를", 5)
	if len(hits) == 0 || hits[0].doc.name != "myapp · 배포 정리" {
		t.Fatalf("particle query top = %+v", hits)
	}
	if hits := searchRecall(docs, "로그인했어", 5); len(hits) == 0 || hits[0].doc.name != "myapp · 로그인 버그" {
		t.Errorf("ending query = %+v", hits)
	}
	// Spacing differs from the note: "쿠키만료" finds "쿠키 만료".
	if hits := searchRecall(docs, "세션쿠키만료", 5); len(hits) == 0 || hits[0].doc.name != "myapp · 로그인 버그" {
		t.Errorf("spacing query = %+v", hits)
	}
	// A short word whose stem would be one rune stays whole.
	if got := recallStem("속도"); got != "속도" {
		t.Errorf("recallStem(속도) = %q", got)
	}
}

func TestSearchRecall_RareWordWeighs(t *testing.T) {
	now := time.Now()
	docs := []recallDoc{
		{kind: "메모", name: "a", when: now, text: "음성 설정 음성 설정"},
		{kind: "메모", name: "b", when: now, text: "음성 화자인증 기준 0.5"},
		{kind: "메모", name: "c", when: now, text: "음성 이야기"},
		{kind: "메모", name: "d", when: now, text: "음성 메모"},
	}
	hits := searchRecall(docs, "음성 화자인증", 5)
	if len(hits) == 0 || hits[0].doc.name != "b" {
		t.Errorf("rare word should lead: %+v", hits)
	}
}

func TestRecallSnippet(t *testing.T) {
	text := strings.Repeat("가", 100) + "목표" + strings.Repeat("나", 300)
	s := recallSnippet(text, 100)
	if !strings.Contains(s, "목표") || !strings.HasPrefix(s, "…") || !strings.HasSuffix(s, "…") {
		t.Errorf("snippet = %q", s)
	}
}
