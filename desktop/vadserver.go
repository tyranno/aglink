package main

// Silero VAD for the mic, run by a dedicated supertonic-server --vad process.
// It is separate from the TTS server (tts.go) on purpose: VAD runs continuously
// while the mic records, TTS runs in bursts while a reply is read, and neither
// should ever wait behind the other. No Python — it reuses the C# onnxruntime
// that already ships with supertonic. When the model or the server is missing,
// VadProbs returns errNoTurnModel and the frontend falls back to the level gate.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// vadTimeout: a VAD call is ~1 ms; this slow means the server is stuck.
const vadTimeout = 3 * time.Second

var (
	vadMu    sync.Mutex // one request at a time; guards vadSrv, vadBadAt
	vadSrv   *ttsServer
	vadBadAt time.Time // last failed start; guarded by vadMu
)

// vadServerInstalled reports whether the supertonic server and the Silero model
// are both present. It does not need Python or OpenVoice.
func vadServerInstalled() bool {
	dir, err := supertonicDir()
	if err != nil {
		return false
	}
	return fileExists(filepath.Join(dir, "bin", "supertonic-server.exe")) &&
		fileExists(filepath.Join(dir, "silero_vad.onnx"))
}

func startVadServer() (*ttsServer, error) {
	dir, err := supertonicDir()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(filepath.Join(dir, "bin", "supertonic-server.exe"), dir, "--vad")
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
	if !scanWithin(sc, 30*time.Second) || !strings.Contains(sc.Text(), `"ready":true`) {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("VAD 시작 실패: %s", sc.Text())
	}
	log.Printf("[vad] supertonic-server --vad started (pid %d)", cmd.Process.Pid)
	return &ttsServer{cmd: cmd, in: in, out: sc}, nil
}

// vadRequest sends one request, starting the server if needed. A server that
// died or hung is killed and this request fails; the next one starts a fresh
// one. Caller holds vadMu.
func vadRequest(req map[string]any) (json.RawMessage, error) {
	if vadSrv == nil {
		if !vadBadAt.IsZero() && time.Since(vadBadAt) < ttsRetryAfter {
			return nil, errNoTurnModel
		}
		s, err := startVadServer()
		if err != nil {
			vadBadAt = time.Now()
			log.Printf("[vad] start failed: %v", err)
			return nil, err
		}
		vadSrv = s
	}
	line, _ := json.Marshal(req)
	if _, err := vadSrv.in.Write(append(line, '\n')); err == nil && scanWithin(vadSrv.out, vadTimeout) {
		raw := json.RawMessage(append([]byte(nil), vadSrv.out.Bytes()...))
		var resp struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return nil, err
		}
		if !resp.OK {
			return nil, fmt.Errorf("vad: %s", resp.Error)
		}
		return raw, nil
	}
	log.Printf("[vad] supertonic-server --vad died or hung — dropping it")
	stopVadServerLocked()
	return nil, errors.New("VAD 서버가 응답하지 않습니다")
}

// VadProbs gives Silero's speech probability for each 32 ms of pcm (base64
// 16 kHz mono 16-bit PCM) on the mic stream id; reset starts the stream afresh.
// "not installed" means there will be no probabilities (the level gate decides).
func (v *VoiceService) VadProbs(id, pcm string, reset bool) ([]float64, error) {
	if !vadServerInstalled() {
		return nil, errNoTurnModel
	}
	vadMu.Lock()
	defer vadMu.Unlock()
	raw, err := vadRequest(map[string]any{"op": "vad", "id": id, "pcm": pcm, "reset": reset})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Probs []float64 `json:"probs"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	return resp.Probs, nil
}

func stopVadServerLocked() {
	if vadSrv == nil {
		return
	}
	vadSrv.stop()
	vadSrv = nil
}

func stopVadServer() {
	vadMu.Lock()
	defer vadMu.Unlock()
	stopVadServerLocked()
}
