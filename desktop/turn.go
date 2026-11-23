package main

// The end of a turn. A fixed pause either cuts the user off at "음…" or keeps
// them waiting after a finished sentence. turn_server.py runs Smart Turn v3 on
// the CPU in OpenVoice's Python environment, apart from speaker_server.py so its
// GPU scoring never holds up the mic:
//   - Smart Turn v3: from the sound of the turn so far (intonation, pace, a
//     trailing "음"), whether the user has finished (~0.1 s a call).
//
// Silero VAD (hearing speech vs. noise) moved to a dedicated supertonic-server
// --vad process (vadserver.go): it needs no Python, and runs apart from both the
// TTS and Smart-Turn servers so it never waits behind them. Anything missing or
// failing just means the level and the fixed pauses decide, as before.

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
	turnMu        sync.Mutex // one request at a time; guards turnSrv
	turnSrv       *ttsServer
	turnWarming   atomic.Bool
	turnBadAt     time.Time   // last failed start; guarded by turnMu
	turnHangs     int         // stalls in a row; guarded by turnMu
	turnIdleTimer *time.Timer // guarded by turnMu; stops turnSrv after voiceServerIdleTimeout unused
)

// armTurnIdleLocked restarts the idle countdown on start and on every use.
// Callers hold turnMu.
func armTurnIdleLocked() {
	if turnIdleTimer != nil {
		turnIdleTimer.Stop()
	}
	turnIdleTimer = time.AfterFunc(voiceServerIdleTimeout, func() {
		turnMu.Lock()
		defer turnMu.Unlock()
		if turnSrv == nil {
			return
		}
		log.Printf("[turn] idle %s, stopping", voiceServerIdleTimeout)
		stopTurnServerLocked()
	})
}

// turnTimeout: a request takes well under 0.2 s; one this slow is stuck. Much
// shorter than ttsTimeout, as the mic is waiting on it.
const turnTimeout = 3 * time.Second

var errNoTurnModel = errors.New("not installed")

func turnModel(dir, name string) string { return filepath.Join(dir, "turn", name) }

func smartTurnInstalled(dir string) bool {
	return turnServerInstalled(dir) && fileExists(turnModel(dir, "smart-turn-v3.2-cpu.onnx"))
}

func turnServerInstalled(dir string) bool {
	return dir != "" && fileExists(ovPython(dir)) && fileExists(filepath.Join(dir, "turn_server.py"))
}

// warmTurn starts the server in the background (~1 s).
func warmTurn() {
	dir := openVoiceDir()
	if !smartTurnInstalled(dir) || turnWarming.Swap(true) {
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
		armTurnIdleLocked()
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
		// A one-off stall (the PC busy for a moment) restarts at once: a minute
		// without the model cuts the user off at every pause.
		if turnHangs++; turnHangs >= 3 {
			turnBadAt = time.Now()
			turnHangs = 0
		} else {
			go warmTurn()
		}
		return nil, fmt.Errorf("turn server not answering (op %v)", req["op"])
	}
	armTurnIdleLocked()
	turnHangs = 0
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
	if turnIdleTimer != nil {
		turnIdleTimer.Stop()
		turnIdleTimer = nil
	}
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
