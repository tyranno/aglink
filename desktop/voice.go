package main

import (
	"archive/zip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Speech-to-text for the composer's mic button, done locally with whisper.cpp
// so no audio leaves the PC. The frontend records and VAD-trims the clip,
// encodes it as 16 kHz mono WAV and hands it to Transcribe; text-to-speech is
// done entirely in the frontend with the WebView's speechSynthesis (Windows
// voices), so it needs nothing here.
//
// whisper.cpp and the model are not bundled — InstallVoice downloads them into
// <data dir>/whisper on first use (~500 MB, mostly the model).
const (
	// Pinned build: whisper.cpp's version tags (v1.x) carry no binaries, the
	// b-numbered build tags do.
	whisperBinURL   = "https://github.com/ggml-org/whisper.cpp/releases/download/b5454/whisper-bin-x64.zip"
	whisperModel    = "ggml-small.bin"
	whisperModelURL = "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/" + whisperModel
)

// whisperHallucinations are what whisper emits for silence or noise — mostly
// YouTube outro lines from its training data. A clip that transcribes to only
// one of these is treated as no speech.
var whisperHallucinations = []string{
	"시청해주셔서 감사합니다",
	"시청해 주셔서 감사합니다",
	"구독과 좋아요 부탁드립니다",
	"MBC 뉴스",
}

// VoiceService is the Wails-bound service behind the mic button. Install
// progress is reported as the Wails event "voice:install" ({stage, done, total,
// error}).
type VoiceService struct {
	mu         sync.Mutex // guards installing
	installing bool
	runMu      sync.Mutex // one whisper process at a time
}

func NewVoiceService() *VoiceService { return &VoiceService{} }

func whisperDir() (string, error) {
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "whisper"), nil
}

func whisperPaths() (cli, model string, err error) {
	dir, err := whisperDir()
	if err != nil {
		return "", "", err
	}
	return filepath.Join(dir, "bin", "whisper-cli.exe"), filepath.Join(dir, whisperModel), nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// VoiceStatus reports whether speech recognition is ready, as JSON:
// {"ready":bool,"installing":bool,"supported":bool}.
func (v *VoiceService) VoiceStatus() (string, error) {
	cli, model, err := whisperPaths()
	if err != nil {
		return "", err
	}
	v.mu.Lock()
	installing := v.installing
	v.mu.Unlock()
	b, _ := json.Marshal(map[string]bool{
		"ready":      fileExists(cli) && fileExists(model),
		"installing": installing,
		"supported":  runtime.GOOS == "windows" && runtime.GOARCH == "amd64",
	})
	return string(b), nil
}

// InstallVoice downloads whisper.cpp and the model in the background. It
// returns at once; watch "voice:install" for progress and the final
// {stage:"done"} or {stage:"error"}.
func (v *VoiceService) InstallVoice() error {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		return errors.New("음성 인식은 Windows x64에서만 지원합니다")
	}
	v.mu.Lock()
	if v.installing {
		v.mu.Unlock()
		return nil
	}
	v.installing = true
	v.mu.Unlock()

	go func() {
		defer func() {
			v.mu.Lock()
			v.installing = false
			v.mu.Unlock()
		}()
		if err := installWhisper(); err != nil {
			log.Printf("[voice] install failed: %v", err)
			emitVoiceInstall(map[string]any{"stage": "error", "error": err.Error()})
			return
		}
		log.Printf("[voice] whisper installed")
		emitVoiceInstall(map[string]any{"stage": "done"})
	}()
	return nil
}

func emitVoiceInstall(data map[string]any) {
	if app := application.Get(); app != nil {
		app.Event.Emit("voice:install", data)
	}
}

func installWhisper() error {
	dir, err := whisperDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	cli, model, _ := whisperPaths()

	if !fileExists(cli) {
		zipPath := filepath.Join(dir, "whisper-bin.zip")
		if err := download(whisperBinURL, zipPath, "bin"); err != nil {
			return fmt.Errorf("whisper.cpp 다운로드 실패: %w", err)
		}
		err := extractRelease(zipPath, filepath.Join(dir, "bin"))
		os.Remove(zipPath)
		if err != nil {
			return fmt.Errorf("whisper.cpp 압축 해제 실패: %w", err)
		}
		if !fileExists(cli) {
			return errors.New("압축 파일에 whisper-cli.exe 가 없습니다")
		}
	}
	if !fileExists(model) {
		if err := download(whisperModelURL, model, "model"); err != nil {
			return fmt.Errorf("음성 모델 다운로드 실패: %w", err)
		}
	}
	return nil
}

// download fetches url to dst via a .part file, so an interrupted download is
// never mistaken for a finished one.
func download(url, dst, stage string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	part := dst + ".part"
	f, err := os.Create(part)
	if err != nil {
		return err
	}
	pw := &progressWriter{stage: stage, total: resp.ContentLength}
	_, err = io.Copy(f, io.TeeReader(resp.Body, pw))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(part)
		return err
	}
	return os.Rename(part, dst)
}

type progressWriter struct {
	stage       string
	done, total int64
	last        time.Time
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	if time.Since(p.last) > 300*time.Millisecond || p.done == p.total {
		p.last = time.Now()
		emitVoiceInstall(map[string]any{"stage": p.stage, "done": p.done, "total": p.total})
	}
	return len(b), nil
}

// extractRelease unpacks the files under Release/ in the whisper.cpp zip flat
// into dst (whisper-cli.exe needs its DLLs next to it).
func extractRelease(zipPath, dst string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	for _, f := range zr.File {
		name := filepath.Base(f.Name)
		if f.FileInfo().IsDir() || !strings.EqualFold(filepath.Base(filepath.Dir(f.Name)), "Release") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".dll" && name != "whisper-cli.exe" {
			continue
		}
		if err := extractOne(f, filepath.Join(dst, name)); err != nil {
			return err
		}
	}
	return nil
}

func extractOne(f *zip.File, dst string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Transcribe turns a base64 16 kHz mono WAV clip into Korean text. An empty
// result means no speech was recognised.
func (v *VoiceService) Transcribe(wavBase64 string) (string, error) {
	cli, model, err := whisperPaths()
	if err != nil {
		return "", err
	}
	if !fileExists(cli) || !fileExists(model) {
		return "", errors.New("음성 인식이 아직 설치되지 않았습니다")
	}
	wav, err := base64.StdEncoding.DecodeString(wavBase64)
	if err != nil {
		return "", fmt.Errorf("오디오 디코딩 실패: %w", err)
	}

	tmp, err := os.MkdirTemp("", "aglink-voice-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	in := filepath.Join(tmp, "clip.wav")
	if err := os.WriteFile(in, wav, 0o600); err != nil {
		return "", err
	}
	outBase := filepath.Join(tmp, "clip")

	v.runMu.Lock()
	defer v.runMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// -otxt writes UTF-8 to a file; reading stdout instead would depend on the
	// console code page.
	cmd := exec.CommandContext(ctx, cli,
		"-m", model, "-f", in, "-l", "ko",
		"-t", fmt.Sprint(min(runtime.NumCPU(), 8)),
		"-nt", "-np", "-otxt", "-of", outBase)
	cmd.Dir = filepath.Dir(cli)
	hideConsole(cmd)
	started := time.Now()
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("whisper 실행 실패: %v %s", err, strings.TrimSpace(string(out)))
	}
	b, err := os.ReadFile(outBase + ".txt")
	if err != nil {
		return "", err
	}
	text := cleanTranscript(string(b))
	log.Printf("[voice] transcribed %d bytes in %s → %d chars", len(wav), time.Since(started).Round(time.Millisecond), len([]rune(text)))
	return text, nil
}

// cleanTranscript joins whisper's lines and drops silence hallucinations.
func cleanTranscript(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	bare := strings.Trim(s, " .!?,")
	for _, h := range whisperHallucinations {
		if bare == h {
			return ""
		}
	}
	return s
}
