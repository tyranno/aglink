package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// setHistoryDir points the history system at a temp directory for the test.
func setHistoryDir(t *testing.T) {
	t.Helper()
	old := historyDirOverride
	historyDirOverride = t.TempDir()
	t.Cleanup(func() { historyDirOverride = old })
}

func TestWriteHistory_ReadHistory_RoundTrip(t *testing.T) {
	setHistoryDir(t)
	today := time.Now().Format("2006-01-02")

	if err := WriteHistory("myapp", "로그인 버그", "어떻게 됐어?", "다 고쳤어요"); err != nil {
		t.Fatalf("WriteHistory: %v", err)
	}

	content, err := ReadHistory("myapp", today)
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	if content == "" {
		t.Fatal("ReadHistory returned empty string")
	}
	for _, want := range []string{"로그인 버그", "어떻게 됐어?", "다 고쳤어요"} {
		if !strings.Contains(content, want) {
			t.Errorf("history missing %q", want)
		}
	}
}

func TestReadHistory_NotFound_ReturnsEmpty(t *testing.T) {
	setHistoryDir(t)
	content, err := ReadHistory("nope", "2000-01-01")
	if err != nil {
		t.Fatalf("expected nil error for missing history, got: %v", err)
	}
	if content != "" {
		t.Errorf("expected empty content, got %q", content)
	}
}

func TestListHistoryDates_DescendingOrder(t *testing.T) {
	setHistoryDir(t)

	// Create project dir and write placeholder .md files with different dates.
	projDir := historyDirOverride + "/testproject"
	if err := os.MkdirAll(projDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, date := range []string{"2026-01-01", "2026-03-15", "2026-02-10"} {
		if err := os.WriteFile(projDir+"/"+date+".md", []byte("x"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	dates, err := ListHistoryDates("testproject")
	if err != nil {
		t.Fatalf("ListHistoryDates: %v", err)
	}
	if len(dates) != 3 {
		t.Fatalf("expected 3 dates, got %d: %v", len(dates), dates)
	}
	// Must be descending (newest first).
	if dates[0] != "2026-03-15" || dates[1] != "2026-02-10" || dates[2] != "2026-01-01" {
		t.Errorf("dates not sorted descending: %v", dates)
	}
}

func TestListHistoryDates_MissingProject_ReturnsEmpty(t *testing.T) {
	setHistoryDir(t)
	dates, err := ListHistoryDates("no-such-project")
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if len(dates) != 0 {
		t.Errorf("expected empty, got %v", dates)
	}
}

func TestListHistoryProjects(t *testing.T) {
	setHistoryDir(t)

	for _, proj := range []string{"alpha", "beta"} {
		if err := WriteHistory(proj, "title", "q", "a"); err != nil {
			t.Fatalf("WriteHistory(%s): %v", proj, err)
		}
	}

	projects, err := ListHistoryProjects()
	if err != nil {
		t.Fatalf("ListHistoryProjects: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d: %v", len(projects), projects)
	}
}

func TestWriteHistory_ResponseTruncation(t *testing.T) {
	setHistoryDir(t)
	// 501 Korean runes → should be truncated with "(생략)" marker.
	longResp := strings.Repeat("가", 501)
	if err := WriteHistory("myapp", "t", "q", longResp); err != nil {
		t.Fatalf("WriteHistory: %v", err)
	}
	content, err := ReadHistory("myapp", time.Now().Format("2006-01-02"))
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	if !strings.Contains(content, "...(생략)") {
		t.Errorf("expected truncation marker in content; got:\n%s", content)
	}
}

func TestWriteHistory_ShortResponse_NoTruncation(t *testing.T) {
	setHistoryDir(t)
	if err := WriteHistory("myapp", "t", "q", "짧은 응답"); err != nil {
		t.Fatalf("WriteHistory: %v", err)
	}
	content, _ := ReadHistory("myapp", time.Now().Format("2006-01-02"))
	if strings.Contains(content, "생략") {
		t.Errorf("short response should not be truncated")
	}
}
