package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Telegram voice notes → text. A voice or audio message is sent to a
// whisper.cpp server (the same /inference protocol aglink-desktop uses, see
// desktop/whisper_server.go and whisper-server-linux/), and the transcript
// becomes the user's message, with the file path kept beside it.
//
// Telegram voice is OGG/Opus, which whisper.cpp reads only when its server
// runs with --convert. When ffmpeg is on PATH the clip is converted to 16 kHz
// mono WAV first, so any whisper server works; without ffmpeg the OGG is sent
// as is. No server configured, or a failure: the message goes to the worker
// as a plain attachment, as before, with only a log line.

// maxVoiceSeconds: longer audio (a podcast, a recording) is left as a file.
const maxVoiceSeconds = 10 * 60

// whisperURL is the server to use: voice.whisper_url, else AGLINK_WHISPER_URL.
func whisperURL(cfg *Config) string {
	u := ""
	if cfg != nil {
		u = cfg.VoiceWhisperURL
	}
	if u == "" {
		u = os.Getenv("AGLINK_WHISPER_URL")
	}
	return strings.TrimRight(strings.TrimSpace(u), "/")
}

// voiceFFmpeg finds ffmpeg; replaceable in tests.
var voiceFFmpeg = func() string {
	p, err := exec.LookPath("ffmpeg")
	if err != nil {
		return ""
	}
	return p
}

// toWav converts an audio file to 16 kHz mono WAV with ffmpeg.
func toWav(ctx context.Context, ffmpeg, in string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "aglink-voice-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "clip.wav")
	cmd := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-loglevel", "error", "-y", "-i", in, "-ar", "16000", "-ac", "1", "-f", "wav", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %v %s", err, strings.TrimSpace(string(b)))
	}
	return os.ReadFile(out)
}

// transcribeVoice sends an audio file to the whisper server and returns the
// cleaned transcript ("" = no speech recognised).
func transcribeVoice(ctx context.Context, base, path string) (string, error) {
	name := filepath.Base(path)
	var audio []byte
	var err error
	if ff := voiceFFmpeg(); ff != "" {
		if audio, err = toWav(ctx, ff, path); err != nil {
			log.Printf("[voice] %v — sending the original file", err)
		} else {
			name = "clip.wav"
		}
	}
	if audio == nil {
		if audio, err = os.ReadFile(path); err != nil {
			return "", err
		}
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", name)
	_, _ = fw.Write(audio)
	_ = mw.WriteField("response_format", "text")
	_ = mw.WriteField("language", "ko")
	_ = mw.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/inference", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %s: %s", resp.Status, truncate(string(bytes.TrimSpace(b)), 200))
	}
	return cleanTranscript(string(b)), nil
}

// Whisper's output for silence or noise, mirrored from desktop/voice.go.
var (
	bracketNoteRe         = regexp.MustCompile(`\[[^\]]*\]|\([^)]{0,10}\)`)
	newsSignoffRe         = regexp.MustCompile(`(MBC|KBS|SBS|YTN|JTBC)\s*뉴스\s*\S{0,6}(입니다|였습니다)[.!]?`)
	whisperHallucinations = []string{"감사합니다", "시청해주셔서 감사합니다", "시청해 주셔서 감사합니다", "구독과 좋아요 부탁드립니다", "MBC 뉴스"}
)

// cleanTranscript joins whisper's lines and drops silence hallucinations.
func cleanTranscript(s string) string {
	s = bracketNoteRe.ReplaceAllString(s, " ")
	s = newsSignoffRe.ReplaceAllString(s, " ")
	s = strings.Join(strings.Fields(s), " ")
	phrases := strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune(".!?,-", r) })
	for _, p := range phrases {
		if p = strings.TrimSpace(p); p != "" && !slices.Contains(whisperHallucinations, p) {
			return s
		}
	}
	return ""
}

// voicePrompt is the worker's message for a transcribed voice note.
func voicePrompt(transcript, caption, savePath string) string {
	p := transcript
	if caption != "" {
		p = caption + "\n\n" + transcript
	}
	return p + "\n\n[음성 메시지를 받아 적은 것 — 원본: " + savePath + "]"
}

// ingestVoice transcribes a saved voice/audio file and dispatches the text as
// the user's message, echoing back what was heard. false = not handled (no
// server, failure, silence): the caller treats it as a plain attachment.
func (b *Bot) ingestVoice(chatID int64, savePath, caption string) bool {
	base := whisperURL(b.cfg())
	if base == "" {
		return false
	}
	dir, err := attachmentsDir()
	if err != nil || !insideDir(dir, savePath) {
		return false
	}
	b.Typing(chatID)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	started := time.Now()
	text, err := transcribeVoice(ctx, base, savePath)
	if err != nil {
		log.Printf("[voice] transcription failed (%s): %v — passing the file as an attachment", base, err)
		return false
	}
	if text == "" {
		log.Printf("[voice] no speech recognised in %s — passing the file as an attachment", filepath.Base(savePath))
		return false
	}
	log.Printf("[voice] transcribed %s in %s → %d chars", filepath.Base(savePath), time.Since(started).Round(time.Millisecond), len([]rune(text)))
	pruneAttachments(dir, maxAttachments)
	_ = b.Send(chatID, "🎤 들은 말: "+truncate(text, 300))
	b.dispatchText(chatID, voicePrompt(text, caption, savePath), OriginTelegram)
	return true
}
