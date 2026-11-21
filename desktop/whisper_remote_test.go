package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestConnectRemoteWhisper(t *testing.T) {
	t.Setenv("AGLINK_HOME", t.TempDir()) // no voice-words.json: the prompt is whisperPrompt itself
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/inference" {
			http.NotFound(w, r)
			return
		}
		if r.FormValue("prompt") != whisperPrompt || r.FormValue("language") != "ko" {
			http.Error(w, "missing prompt/language", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte("안녕하세요"))
	}))
	defer srv.Close()

	s, err := connectRemoteWhisper(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	text, err := s.transcribe(silentWav(time.Second), 5*time.Second)
	if err != nil || text != "안녕하세요" {
		t.Fatalf("got %q, %v", text, err)
	}
	s.stop() // remote: must not panic

	srv.Close()
	if _, err := connectRemoteWhisper(srv.URL); err == nil {
		t.Fatal("a dead server must be reported")
	}
}

func TestRemoteWhisperURL(t *testing.T) {
	t.Setenv("AGLINK_HOME", t.TempDir()) // no whisper-server.json pref here → env is used
	t.Setenv("AGLINK_WHISPER_URL", " http://host:8178/ ")
	if got := remoteWhisperURL(); got != "http://host:8178" {
		t.Fatalf("got %q", got)
	}
}

func TestWhisperServerPrefWins(t *testing.T) {
	t.Setenv("AGLINK_HOME", t.TempDir())
	t.Setenv("AGLINK_WHISPER_URL", "http://env:8178")
	// Nothing saved yet: env is used.
	if got := remoteWhisperURL(); got != "http://env:8178" {
		t.Fatalf("before save: got %q", got)
	}
	// A saved URL wins over the env.
	if err := saveWhisperServerPref(" http://pref:8178/ "); err != nil {
		t.Fatal(err)
	}
	if got := remoteWhisperURL(); got != "http://pref:8178" {
		t.Fatalf("after save url: got %q", got)
	}
	// A saved empty string means "this PC" and still wins over the env.
	if err := saveWhisperServerPref(""); err != nil {
		t.Fatal(err)
	}
	if got := remoteWhisperURL(); got != "" {
		t.Fatalf("after save empty: got %q", got)
	}
}
