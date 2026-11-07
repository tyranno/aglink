package main

// Hearing speech and the end of a turn. The mic's level alone takes a fan, the
// TV's music or a door for speech, and a fixed pause either cuts the user off
// at "음…" or keeps them waiting after a finished sentence. turn_server.py runs
// two small ONNX models on the CPU in OpenVoice's Python environment, apart
// from speaker_server.py so its GPU scoring never holds up the mic:
//   - Silero VAD: speech probability per 32 ms, fed as the mic records
//     (~1 ms a call), so non-speech sound neither starts a clip nor keeps one open;
//   - Smart Turn v3: from the sound of the turn so far (intonation, pace, a
//     trailing "음"), whether the user has finished (~0.1 s a call).
//
// Anything missing or failing just means the level and the fixed pauses
// decide, as before.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

var (
	turnMu      sync.Mutex // one request at a time; guards turnSrv
	turnSrv     *ttsServer
	turnWarming atomic.Bool
	turnBadAt   time.Time // last failed start; guarded by turnMu
)

// turnTimeout: a request takes well under 0.2 s; one this slow is stuck. Much
// shorter than ttsTimeout, as the mic is waiting on it.
const turnTimeout = 3 * time.Second

var (
	errNoTurnModel  = errors.New("not installed")
	errTurnStarting = errors.New("starting")
)

func turnModel(dir, name string) string { return filepath.Join(dir, "turn", name) }

func vadInstalled(dir string) bool {
	return turnServerInstalled(dir) && fileExists(turnModel(dir, "silero_vad.onnx"))
}

func smartTurnInstalled(dir string) bool {
	return turnServerInstalled(dir) && fileExists(turnModel(dir, "smart-turn-v3.2-cpu.onnx"))
}

func turnServerInstalled(dir string) bool {
	return dir != "" && fileExists(ovPython(dir)) && fileExists(filepath.Join(dir, "turn_server.py"))
}

// warmTurn starts the server in the background (~1 s).
func warmTurn() {
	dir := openVoiceDir()
	if !(vadInstalled(dir) || smartTurnInstalled(dir)) || turnWarming.Swap(true) {
		return
	}
	go func() {
		defer turnWarming.Store(false)
		turnMu.Lock()
		defer turnMu.Unlock()
		if turnSrv != nil || (!turnBadAt.IsZero() && time.Since(turnBadAt) < ttsRetryAfter) {
			return
		}
		started := time.Now()
		s, err := startPythonServer(dir, "turn_server.py", "[turn]")
		if err != nil {
			turnBadAt = time.Now()
			log.Printf("[turn] start failed: %v", err)
			return
		}
		turnSrv = s
		log.Printf("[turn] ready in %s", time.Since(started).Round(time.Millisecond))
	}()
}

// turnRequest sends one request, or returns nil while the server is not up
// (it is started then, and never waited for). A dead or hung server is dropped
// and started again on a later request.
func turnRequest(req map[string]any) (json.RawMessage, error) {
	turnMu.Lock()
	defer turnMu.Unlock()
	if turnSrv == nil {
		if !turnWarming.Load() {
			go warmTurn()
		}
		return nil, nil
	}
	line, _ := json.Marshal(req)
	if _, err := turnSrv.in.Write(append(line, '\n')); err != nil || !scanWithin(turnSrv.out, turnTimeout) {
		stopTurnServerLocked()
		turnBadAt = time.Now()
		return nil, errors.New("turn server not answering")
	}
	var resp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	raw := json.RawMessage(append([]byte(nil), turnSrv.out.Bytes()...))
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("turn server: %s", resp.Error)
	}
	return raw, nil
}

// VadProbs gives Silero's speech probability for each 32 ms of pcm (base64
// 16 kHz mono 16-bit PCM) heard on the mic stream id; reset starts the stream
// afresh. The error "starting" means no answer this time; "not installed"
// means there will be none.
func (v *VoiceService) VadProbs(id, pcm string, reset bool) ([]float64, error) {
	if !vadInstalled(openVoiceDir()) {
		return nil, errNoTurnModel
	}
	raw, err := turnRequest(map[string]any{"op": "vad", "id": id, "pcm": pcm, "reset": reset})
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, errTurnStarting
	}
	var resp struct {
		Probs []float64 `json:"probs"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	return resp.Probs, nil
}

// TurnEnd says how likely the user has finished their turn, from its audio so
// far (base64 16 kHz mono 16-bit PCM, the pause included; the last 8 s count):
// 0–1, or -1 when it cannot tell (not installed, starting, or failed).
func (v *VoiceService) TurnEnd(pcm string) (float64, error) {
	if !smartTurnInstalled(openVoiceDir()) {
		return -1, nil
	}
	raw, err := turnRequest(map[string]any{"op": "turn", "pcm": pcm})
	if err != nil || raw == nil {
		if err != nil {
			log.Printf("[turn] %v", err)
		}
		return -1, nil
	}
	var resp struct {
		Prob float64 `json:"prob"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return -1, nil
	}
	return resp.Prob, nil
}

func stopTurnServerLocked() {
	if turnSrv == nil {
		return
	}
	_ = turnSrv.in.Close()
	_ = turnSrv.cmd.Process.Kill()
	_ = turnSrv.cmd.Wait()
	turnSrv = nil
}

func stopTurnServer() {
	turnMu.Lock()
	defer turnMu.Unlock()
	stopTurnServerLocked()
}
