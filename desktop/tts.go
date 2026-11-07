package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Text-to-speech with Supertonic 3 (Supertone's open on-device model, ONNX,
// OpenRAIL-M weights), run locally by supertonic-server.exe — the small C#
// program in ./supertonic. It lives in <data dir>/supertonic/bin next to the
// model (onnx/, voice_styles/) and stays running so the models load once.
// Without it the frontend falls back to the Windows voice.

const defaultTTSVoice = "M2"

// cloneVoiceName is the recorded voice ("녹음한 목소리"): cloneBaseVoice
// repainted with the recording. The other voices are read plain.
const (
	cloneVoiceName = "C"
	cloneBaseVoice = "F2"
)

// ttsSteps: Supertonic's denoising steps. 5 (the upstream examples' default)
// takes ~35% less time than supertonic-server's own default of 8.
const ttsSteps = 5

// ttsSpeed: at 1.05 Korean sentence endings snapped shut and the reading
// sounded pushy; a touch under 1.0 lets them settle.
const ttsSpeed = 0.98

type ttsServer struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Scanner
}

// ttsTimeout: a sentence takes well under 3 s; a server this slow is stuck and
// is replaced.
const ttsTimeout = 20 * time.Second

// ttsRetryAfter: after a failed start the Windows voice is used for a while
// instead of trying again on every reply.
const ttsRetryAfter = time.Minute

var (
	ttsMu     sync.Mutex // one request at a time; guards tts, ttsBadAt
	tts       *ttsServer
	ttsBadAt  time.Time // last failed start
	ttsWarmed atomic.Bool
)

func ttsBad() bool {
	return !ttsBadAt.IsZero() && time.Since(ttsBadAt) < ttsRetryAfter
}

// warmTTSServer starts Supertonic in the background once, so the first reply
// does not wait ~1.5 s for its models to load.
func warmTTSServer() {
	if !supertonicReady() || ttsWarmed.Swap(true) {
		return
	}
	go func() {
		defer warmOpenVoice(cloneBaseVoice) // after Supertonic: it reads the reference text
		ttsMu.Lock()
		defer ttsMu.Unlock()
		if tts != nil || ttsBad() {
			return
		}
		s, err := startTTSServer()
		if err != nil {
			ttsBadAt = time.Now()
			log.Printf("[tts] start failed: %v", err)
			return
		}
		tts = s
	}()
}

func supertonicDir() (string, error) {
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "supertonic"), nil
}

func supertonicReady() bool {
	dir, err := supertonicDir()
	return err == nil &&
		fileExists(filepath.Join(dir, "bin", "supertonic-server.exe")) &&
		fileExists(filepath.Join(dir, "onnx", "vocoder.onnx"))
}

// TTSStatus reports {"ready":bool,"voice":"M1"} for the Supertonic voice.
func (v *VoiceService) TTSStatus() (string, error) {
	warmTTSServer()
	ttsMu.Lock()
	bad := ttsBad()
	ttsMu.Unlock()
	b, _ := json.Marshal(map[string]any{"ready": supertonicReady() && !bad, "voice": defaultTTSVoice})
	return string(b), nil
}

// ttsVoiceRE: Supertonic's voices, M1–M5 and F1–F5 (voice_styles/*.json).
var ttsVoiceRE = regexp.MustCompile(`^[MF][1-5]$`)

// Synthesize reads text aloud with Supertonic and returns a base64 WAV. The
// voice and speed come from the frontend's voice settings; "" and 0 mean the
// defaults.
func (v *VoiceService) Synthesize(text, voiceName string, speed float64) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("빈 문장")
	}
	clone := voiceName == cloneVoiceName
	if clone {
		voiceName = cloneBaseVoice
	}
	if !ttsVoiceRE.MatchString(voiceName) {
		voiceName = defaultTTSVoice
	}
	if speed <= 0 {
		speed = ttsSpeed
	}
	speed = min(max(speed, 0.7), 2.0)
	tmp, err := os.CreateTemp("", "aglink-tts-*.wav")
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	tmp.Close()
	defer os.Remove(path)

	started := time.Now()
	ttsMu.Lock()
	err = ttsRequest(map[string]any{"text": text, "voice": voiceName, "speed": speed, "steps": ttsSteps, "out": path})
	ttsMu.Unlock()
	if err != nil {
		return "", err
	}
	if clone {
		if conv := convertVoice(path, voiceName); conv != "" {
			defer os.Remove(conv)
			path = conv
		}
	}
	wav, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	log.Printf("[tts] %d chars → %d bytes in %s", len([]rune(text)), len(wav), time.Since(started).Round(time.Millisecond))
	return base64.StdEncoding.EncodeToString(wav), nil
}

// ttsRequest sends one request, starting the server if needed and restarting
// it once if it died. Callers hold ttsMu.
func ttsRequest(req map[string]any) error {
	line, _ := json.Marshal(req)
	for attempt := 0; attempt < 2; attempt++ {
		if tts == nil {
			if ttsBad() {
				return errors.New("Supertonic 을 시작하지 못했습니다")
			}
			s, err := startTTSServer()
			if err != nil {
				ttsBadAt = time.Now()
				log.Printf("[tts] start failed: %v", err)
				return err
			}
			tts = s
		}
		if _, err := tts.in.Write(append(line, '\n')); err == nil && scanWithin(tts.out, ttsTimeout) {
			var resp struct {
				OK    bool   `json:"ok"`
				Error string `json:"error"`
			}
			if err := json.Unmarshal(tts.out.Bytes(), &resp); err != nil {
				return err
			}
			if !resp.OK {
				return fmt.Errorf("Supertonic: %s", resp.Error)
			}
			return nil
		}
		log.Printf("[tts] supertonic-server died or hung — restarting")
		stopTTSServer() // start a fresh one
	}
	return errors.New("Supertonic 이 응답하지 않습니다")
}

// scanWithin reads the next line, giving up after d. A hung server is then
// killed by the caller, which also ends the Scan left running here.
func scanWithin(sc *bufio.Scanner, d time.Duration) bool {
	done := make(chan bool, 1)
	go func() { done <- sc.Scan() }()
	select {
	case ok := <-done:
		return ok
	case <-time.After(d):
		return false
	}
}

func startTTSServer() (*ttsServer, error) {
	if !supertonicReady() {
		return nil, errors.New("Supertonic 이 설치되지 않았습니다")
	}
	dir, _ := supertonicDir()
	cmd := exec.Command(filepath.Join(dir, "bin", "supertonic-server.exe"), dir)
	cmd.Dir = filepath.Join(dir, "bin")
	hideConsole(cmd)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	if !scanWithin(sc, time.Minute) || !strings.Contains(sc.Text(), `"ready":true`) {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("Supertonic 시작 실패: %s", sc.Text())
	}
	log.Printf("[tts] supertonic-server started (pid %d)", cmd.Process.Pid)
	return &ttsServer{cmd: cmd, in: in, out: sc}, nil
}

func stopTTSServer() {
	if tts == nil {
		return
	}
	_ = tts.in.Close()
	_ = tts.cmd.Process.Kill()
	_ = tts.cmd.Wait()
	tts = nil
}

// ServiceShutdown stops the TTS and speech-recognition servers with the app.
func (v *VoiceService) ServiceShutdown() error {
	ttsMu.Lock()
	stopTTSServer()
	ttsMu.Unlock()
	ovMu.Lock()
	stopOpenVoiceServer()
	ovMu.Unlock()
	stopSpeakerServer()
	stopWhisperServer()
	return nil
}

// PlayWav plays a base64 WAV from Synthesize through the OS and returns when it
// has finished. gen numbers the reading it belongs to (see StopWav). The
// frontend plays in the page first and comes here only when it cannot; it
// falls back to <audio> if this fails too.
func (v *VoiceService) PlayWav(b64 string, gen int) error {
	wav, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return err
	}
	return playWavNative(wav, gen)
}

// StopWav cuts off reading gen and keeps any of its clips still on the way
// from starting.
func (v *VoiceService) StopWav(gen int) {
	stopWavNative(gen)
}
