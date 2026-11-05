package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestConnectRemoteWhisper(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/inference" {
			http.NotFound(w, r)
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
	t.Setenv("AGLINK_WHISPER_URL", " http://host:8178/ ")
	if got := remoteWhisperURL(); got != "http://host:8178" {
		t.Fatalf("got %q", got)
	}
}
