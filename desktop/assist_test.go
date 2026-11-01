package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLaunchAllowed(t *testing.T) {
	for name, want := range map[string]bool{
		"notepad.exe": true, "chrome": true, "ms-settings:": true, "Spotify": true,
		"cmd": false, "PowerShell.exe": false, "shutdown": false, `C:\x.exe`: false,
		"http://x": false, "a & b": false, "..": false, "메모장": false, "": false,
	} {
		if got := launchAllowed(name); got != want {
			t.Errorf("launchAllowed(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestUsagePeriod(t *testing.T) {
	now := time.Date(2026, 11, 4, 15, 0, 0, 0, time.Local) // a Wednesday
	for kind, wantFrom := range map[string]time.Time{
		"today":     time.Date(2026, 11, 4, 0, 0, 0, 0, time.Local),
		"yesterday": time.Date(2026, 11, 3, 0, 0, 0, 0, time.Local),
		"week":      time.Date(2026, 11, 2, 0, 0, 0, 0, time.Local),
		"month":     time.Date(2026, 11, 1, 0, 0, 0, 0, time.Local),
	} {
		from, _, err := usagePeriod(kind, now)
		if err != nil || !from.Equal(wantFrom) {
			t.Errorf("%s: from %v (%v), want %v", kind, from, err, wantFrom)
		}
	}
	// Sunday belongs to the week that began on Monday.
	if from, _, _ := usagePeriod("week", time.Date(2026, 11, 8, 9, 0, 0, 0, time.Local)); from.Day() != 2 {
		t.Errorf("sunday week from %v", from)
	}
	if _, _, err := usagePeriod("year", now); err == nil {
		t.Error("unknown period accepted")
	}
}

func TestSumUsage_AllConversationsOnce(t *testing.T) {
	at := func(d, h int) string { return time.Date(2026, 11, d, h, 0, 0, 0, time.Local).Format(time.RFC3339) }
	store := `{
	  "telegramConv": {"id":"tg","title":"텔레그램","turnStats":[{"at":"` + at(4, 9) + `","costUsd":0.5}]},
	  "webConvs": {
	    "a": {"id":"a","title":"이것저것","turnStats":[{"at":"` + at(4, 10) + `","costUsd":1.25},{"at":"` + at(4, 11) + `","costUsd":1},{"at":"` + at(3, 23) + `","costUsd":9}]},
	    "b": {"id":"b","title":"빈 대화"},
	    "bad": "not a conversation"
	  },
	  "projects": [{"conversations": {"a": {"id":"a","title":"이것저것","turnStats":[{"at":"` + at(4, 10) + `","costUsd":1.25}]}, "p": {"id":"p","title":"프로젝트","turnStats":[{"at":"` + at(4, 12) + `","costUsd":0.25}]}}}]
	}`
	from := time.Date(2026, 11, 4, 0, 0, 0, 0, time.Local)
	s := sumUsage(storeConvs([]byte(store)), from, from.AddDate(0, 0, 1))
	if s.Turns != 4 || s.Top != "이것저것" || s.CostUSD < 2.999 || s.CostUSD > 3.001 {
		t.Fatalf("sum = %+v", s)
	}
	if got := storeConvs([]byte(`[1,2]`)); got != nil {
		t.Fatalf("odd store gave %v", got)
	}
}

func TestMemos_AddListDelete(t *testing.T) {
	t.Setenv("AGLINK_HOME", t.TempDir())
	v := &VoiceService{}
	if raw, _ := v.MemoList(); raw != "[]" {
		t.Fatalf("empty list = %s", raw)
	}
	v.MemoAdd("견적서 확인", false)
	v.MemoAdd("보고서 쓰기", true)
	raw, err := v.MemoAdd("  우유 사기 ", true)
	if err != nil {
		t.Fatal(err)
	}
	var list []memo
	json.Unmarshal([]byte(raw), &list)
	if len(list) != 3 || list[2].Text != "우유 사기" || !list[2].Todo || list[2].ID != 3 {
		t.Fatalf("list = %+v", list)
	}
	raw, _ = v.MemoDelete([]int{2})
	json.Unmarshal([]byte(raw), &list)
	if len(list) != 2 || list[1].ID != 3 {
		t.Fatalf("after delete = %+v", list)
	}
	if raw, _ = v.MemoDelete(nil); raw != "[]" {
		t.Fatalf("after clear = %s", raw)
	}
	if _, err := v.MemoAdd("  ", false); err == nil {
		t.Fatal("empty memo kept")
	}
}

func TestTrimMiddleAndSummaryPath(t *testing.T) {
	s := strings.Repeat("가", 100) + strings.Repeat("나", 100)
	got := trimMiddle(s, 30)
	if !strings.HasPrefix(got, strings.Repeat("가", 20)) || !strings.HasSuffix(got, strings.Repeat("나", 10)) || !strings.Contains(got, "생략") {
		t.Fatalf("trimMiddle = %q", got)
	}
	if trimMiddle("짧다", 30) != "짧다" {
		t.Fatal("short text changed")
	}
	dir := filepath.Join("C:", "Docs", "aglink 녹음")
	if p := tapeSummaryPath(dir, filepath.Join(dir, "녹음 2026-11-01 10-00-00.txt")); p != filepath.Join(dir, "녹음 2026-11-01 10-00-00 요약.md") {
		t.Fatalf("summary path = %q", p)
	}
	if p := tapeSummaryPath(dir, filepath.Join("C:", "Other", "x.txt")); p != "" {
		t.Fatalf("outside path accepted: %q", p)
	}
}
