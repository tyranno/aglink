package main

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestTape_WritesPlayableWavAndNotes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	v := &VoiceService{}
	path, err := v.TapeStart()
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]byte, tapeRate*2) // one second of silence
	for i := 0; i < 3; i++ {
		if err := v.TapeAppend(base64.StdEncoding.EncodeToString(pcm)); err != nil {
			t.Fatal(err)
		}
		// The header is right after every piece, before the recording is stopped.
		b, _ := os.ReadFile(path)
		if got := binary.LittleEndian.Uint32(b[40:]); got != uint32((i+1)*len(pcm)) || len(b) != wavHeaderLen+(i+1)*len(pcm) {
			t.Fatalf("after piece %d: data size %d, file %d", i, got, len(b))
		}
	}
	if err := v.TapeNote(65.4, " 안녕하세요 "); err != nil {
		t.Fatal(err)
	}
	out, err := v.TapeStop()
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Path    string
		Seconds float64
	}
	_ = json.Unmarshal([]byte(out), &res)
	if res.Path != path || res.Seconds != 3 {
		t.Fatalf("stop = %s", out)
	}
	txt, _ := os.ReadFile(strings.TrimSuffix(path, ".wav") + ".txt")
	if !strings.Contains(string(txt), "[01:05] 안녕하세요\n") {
		t.Fatalf("notes = %q", txt)
	}
	if err := v.TapeAppend(base64.StdEncoding.EncodeToString(pcm)); err == nil {
		t.Fatal("append after stop should fail")
	}
}
