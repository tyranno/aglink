package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestCleanTranscript(t *testing.T) {
	cases := map[string]string{
		"  오늘 빌드 로그\n 확인해 줘. ": "오늘 빌드 로그 확인해 줘.",
		"시청해주셔서 감사합니다.":        "",
		" 구독과 좋아요 부탁드립니다!":     "",
		"감사합니다":                "감사합니다",
		"":                     "",
	}
	for in, want := range cases {
		if got := cleanTranscript(in); got != want {
			t.Errorf("cleanTranscript(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtractReleaseTakesOnlyCLIAndDLLs(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "w.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, name := range []string{
		"Release/whisper-cli.exe",
		"Release/ggml.dll",
		"Release/whisper-server.exe",
		"other/whisper-cli.exe",
	} {
		w, _ := zw.Create(name)
		w.Write([]byte(name))
	}
	zw.Close()
	f.Close()

	dst := filepath.Join(dir, "bin")
	if err := extractRelease(zipPath, dst); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dst)
	got := map[string]bool{}
	for _, e := range entries {
		got[e.Name()] = true
	}
	if len(got) != 2 || !got["whisper-cli.exe"] || !got["ggml.dll"] {
		t.Fatalf("extracted %v, want whisper-cli.exe and ggml.dll only", got)
	}
	b, _ := os.ReadFile(filepath.Join(dst, "whisper-cli.exe"))
	if string(b) != "Release/whisper-cli.exe" {
		t.Fatalf("whisper-cli.exe came from %q, want the Release/ copy", b)
	}
}
