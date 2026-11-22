package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportFileName(t *testing.T) {
	cases := map[string]string{
		"회의 정리":               "회의 정리.md",
		`a/b\c:d*e?f"g<h>i|j`: "a_b_c_d_e_f_g_h_i_j.md",
		"":                    "대화.md",
		"   ":                 "대화.md",
		"///":                 "대화.md",
		"끝에 점...":             "끝에 점.md",
		"notes.md":            "notes.md",
		"탭\t줄\n바꿈":            "탭_줄_바꿈.md",
	}
	for in, want := range cases {
		if got := exportFileName(in); got != want {
			t.Errorf("exportFileName(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("가", 200)
	if got := exportFileName(long); len([]rune(got)) != 83 {
		t.Errorf("long title not capped: %d runes", len([]rune(got)))
	}
}

func TestStageTextAttachment(t *testing.T) {
	t.Setenv("AGLINK_HOME", t.TempDir())
	if _, err := stageTextAttachment("  \n "); err == nil {
		t.Fatal("blank text must be refused")
	}
	if _, err := stageTextAttachment(strings.Repeat("x", maxTextAttachmentBytes+1)); err == nil {
		t.Fatal("oversized text must be refused")
	}
	path, err := stageTextAttachment("안녕\n세상")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(path) != ".txt" {
		t.Fatalf("want .txt, got %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "안녕\n세상" {
		t.Fatalf("content = %q, %v", data, err)
	}
}

func TestWriteExportFileAddsExtension(t *testing.T) {
	base := filepath.Join(t.TempDir(), "out")
	path, err := writeExportFile(base, "# hi")
	if err != nil {
		t.Fatal(err)
	}
	if path != base+".md" {
		t.Fatalf("path = %s", path)
	}
	if data, _ := os.ReadFile(path); string(data) != "# hi" {
		t.Fatalf("content = %q", data)
	}
}
