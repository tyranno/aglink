package main

import (
	"archive/zip"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanTranscript(t *testing.T) {
	cases := map[string]string{
		"  오늘 빌드 로그\n 확인해 줘. ": "오늘 빌드 로그 확인해 줘.",
		"시청해주셔서 감사합니다.":        "",
		" 구독과 좋아요 부탁드립니다!":     "",
		"감사합니다":                "",
		"집에 갈래 [감사합니다]":        "집에 갈래",
		"[음악]":                 "",
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
	if err := extractRelease(zipPath, dst, "whisper-cli.exe"); err != nil {
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

func TestAudioContext(t *testing.T) {
	for _, c := range []struct{ secs, want int }{{0, 512}, {4, 512}, {10, 756}, {25, 1500}, {60, 1500}} {
		if got := audioContext(44 + c.secs*32000); got != c.want {
			t.Errorf("audioContext(%ds) = %d, want %d", c.secs, got, c.want)
		}
	}
}

func TestSilentWavHeader(t *testing.T) {
	wav := silentWav(time.Second)
	if len(wav) != 44+32000 {
		t.Fatalf("len = %d, want %d", len(wav), 44+32000)
	}
	if string(wav[0:4]) != "RIFF" || string(wav[8:16]) != "WAVEfmt " || string(wav[36:40]) != "data" {
		t.Fatalf("bad header % x", wav[:44])
	}
	if rate := binary.LittleEndian.Uint32(wav[24:28]); rate != 16000 {
		t.Fatalf("rate = %d", rate)
	}
}

func TestCleanTranscript_DropsNewsSignoff(t *testing.T) {
	if got := cleanTranscript("MBC 뉴스 김상현입니다."); got != "" {
		t.Fatalf("got %q", got)
	}
	if got := cleanTranscript("진행해 MBC 뉴스 이덕영입니다"); got != "진행해" {
		t.Fatalf("got %q", got)
	}
}

func TestCleanTranscriptDropsParenNotes(t *testing.T) {
	if got := cleanTranscript("(끝) (끝) 아들 이리 와봐 (끝)"); got != "아들 이리 와봐" {
		t.Fatalf("got %q", got)
	}
	if got := cleanTranscript("(끝) (끝)"); got != "" {
		t.Fatalf("got %q", got)
	}
}
