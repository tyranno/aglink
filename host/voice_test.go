package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestWhisperURL(t *testing.T) {
	t.Setenv("AGLINK_WHISPER_URL", " http://env:8178/ ")
	if got := whisperURL(&Config{}); got != "http://env:8178" {
		t.Errorf("env = %q", got)
	}
	if got := whisperURL(&Config{VoiceWhisperURL: "http://cfg:8178/"}); got != "http://cfg:8178" {
		t.Errorf("config should win: %q", got)
	}
	t.Setenv("AGLINK_WHISPER_URL", "")
	if got := whisperURL(&Config{}); got != "" {
		t.Errorf("none = %q", got)
	}
}

func TestCleanTranscript(t *testing.T) {
	cases := map[string]string{
		" 내일 회의\n 몇 시야? ":    "내일 회의 몇 시야?",
		"감사합니다.":             "",
		"[음악] 시청해주셔서 감사합니다.": "",
		"(웃음) 배포해 줘":         "배포해 줘",
	}
	for in, want := range cases {
		if got := cleanTranscript(in); got != want {
			t.Errorf("cleanTranscript(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVoiceSeconds(t *testing.T) {
	if s, ok := voiceSeconds(&tgbotapi.Message{Voice: &tgbotapi.Voice{Duration: 7}}); !ok || s != 7 {
		t.Errorf("voice = %d, %v", s, ok)
	}
	if _, ok := voiceSeconds(&tgbotapi.Message{Document: &tgbotapi.Document{}}); ok {
		t.Error("a document is not speech")
	}
}

// fakeWhisper is a whisper.cpp /inference stand-in that records the form.
type fakeWhisper struct {
	mu     sync.Mutex
	fields map[string]string
	file   string
	reply  string
	status int
}

func (f *fakeWhisper) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/inference" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("multipart: %v", err)
		}
		f.mu.Lock()
		f.fields = map[string]string{"response_format": r.FormValue("response_format"), "language": r.FormValue("language")}
		if file, hdr, err := r.FormFile("file"); err == nil {
			b, _ := io.ReadAll(file)
			f.file = hdr.Filename + ":" + string(b)
		}
		f.mu.Unlock()
		if f.status != 0 {
			w.WriteHeader(f.status)
		}
		_, _ = io.WriteString(w, f.reply)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func noFFmpeg(t *testing.T) {
	old := voiceFFmpeg
	voiceFFmpeg = func() string { return "" }
	t.Cleanup(func() { voiceFFmpeg = old })
}

func TestTranscribeVoice_SendsInferenceForm(t *testing.T) {
	noFFmpeg(t)
	fw := &fakeWhisper{reply: " 내일 아홉 시에\n 깨워 줘\n"}
	srv := fw.serve(t)
	path := filepath.Join(t.TempDir(), "123.ogg")
	_ = os.WriteFile(path, []byte("OggS-data"), 0o600)

	got, err := transcribeVoice(context.Background(), srv.URL, path)
	if err != nil || got != "내일 아홉 시에 깨워 줘" {
		t.Fatalf("transcribeVoice = %q, %v", got, err)
	}
	if fw.fields["response_format"] != "text" || fw.fields["language"] != "ko" || fw.file != "123.ogg:OggS-data" {
		t.Errorf("form = %v, file %q", fw.fields, fw.file)
	}

	fw.status = http.StatusInternalServerError
	if _, err := transcribeVoice(context.Background(), srv.URL, path); err == nil {
		t.Error("an HTTP error should be reported")
	}
}

func voiceBot(t *testing.T, url string) (*Bot, *recCh, *[]string, string) {
	t.Helper()
	t.Setenv(dataDirEnv, t.TempDir())
	t.Setenv("AGLINK_WHISPER_URL", "")
	dir, _ := attachmentsDir()
	_ = os.MkdirAll(dir, 0o700)
	path := filepath.Join(dir, "1.ogg")
	_ = os.WriteFile(path, []byte("OggS"), 0o600)
	var dispatched []string
	b := &Bot{cfgh: NewConfigHolder(&Config{VoiceWhisperURL: url}), out: NewHub()}
	b.dispatchHook = func(_ int64, text string) { dispatched = append(dispatched, text) }
	rec := &recCh{}
	b.out.RegisterGlobal(rec)
	return b, rec, &dispatched, path
}

func TestIngestVoice_TranscriptBecomesMessage(t *testing.T) {
	noFFmpeg(t)
	fw := &fakeWhisper{reply: "서버 상태 확인해 줘"}
	srv := fw.serve(t)
	b, rec, dispatched, path := voiceBot(t, srv.URL)

	if !b.ingestVoice(1, path, "") {
		t.Fatal("ingestVoice = false")
	}
	if len(rec.texts) != 1 || rec.texts[0] != "🎤 들은 말: 서버 상태 확인해 줘" {
		t.Errorf("echo = %v", rec.texts)
	}
	if len(*dispatched) != 1 || !strings.HasPrefix((*dispatched)[0], "서버 상태 확인해 줘") || !strings.Contains((*dispatched)[0], path) {
		t.Errorf("dispatched = %v", *dispatched)
	}
}

// No server, a failing server or silence: not handled, nothing said — the
// caller falls back to the plain attachment path.
func TestIngestVoice_FallsBackQuietly(t *testing.T) {
	noFFmpeg(t)
	b, rec, dispatched, path := voiceBot(t, "")
	if b.ingestVoice(1, path, "") {
		t.Error("no server configured should not be handled")
	}

	fw := &fakeWhisper{status: http.StatusBadGateway}
	srv := fw.serve(t)
	b.cfgh = NewConfigHolder(&Config{VoiceWhisperURL: srv.URL})
	if b.ingestVoice(1, path, "") {
		t.Error("a server error should not be handled")
	}
	fw.status, fw.reply = 0, "감사합니다."
	if b.ingestVoice(1, path, "") {
		t.Error("silence should not be handled")
	}
	if len(rec.texts) != 0 || len(*dispatched) != 0 {
		t.Errorf("fallbacks must stay quiet: sent %v, dispatched %v", rec.texts, *dispatched)
	}
}
