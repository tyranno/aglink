package main

// Spoken replies in someone else's voice: Supertonic still does the talking
// (fast, natural Korean), then OpenVoice V2's tone color converter repaints
// each clip with the timbre of a short recording. It is a Python process
// (<data>/openvoice: venv/, src/ (OpenVoice), ckpt/, server.py — see
// openvoice/README.md) kept running
// on the GPU like whisper-server; a conversion takes ~0.3 s per sentence.
//
// It is on only while <data>/openvoice/voice.wav exists — that recording
// (10–30 s of one person speaking clearly) is the target voice. Anything going
// wrong just leaves the Supertonic voice as it was.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ovRefText is read by the Supertonic voice to learn its own timbre, which the
// converter subtracts before painting in the target's.
const ovRefText = "안녕하세요. 오늘은 날씨가 정말 좋네요. 점심은 무엇을 드셨나요? 저는 아까 회의를 마치고 산책을 다녀왔어요. 내일은 비가 온다고 하니 우산을 챙기세요."

// ovTau: how far the converter moves toward the target; OpenVoice's default.
const ovTau = 0.3

var (
	ovMu     sync.Mutex // one request at a time; guards ov
	ov       *ttsServer
	ovReady  atomic.Bool // server up and both voice embeddings made
	ovWarmed atomic.Bool
	ovFailed atomic.Bool  // off until ovRetryAfter has passed
	ovFailAt atomic.Int64 // when it went off (UnixNano)
)

// ovRetryAfter: a converter that died or failed to start is tried again after
// this long, so one crash does not leave the plain voice for the rest of the run.
const ovRetryAfter = 5 * time.Minute

func ovFail() {
	ovReady.Store(false)
	ovFailAt.Store(time.Now().UnixNano())
	ovFailed.Store(true)
}

// ovRetryDue reopens a failed converter once ovRetryAfter has passed.
func ovRetryDue(voiceName string) {
	if !ovFailed.Load() || time.Since(time.Unix(0, ovFailAt.Load())) < ovRetryAfter {
		return
	}
	if ovFailed.CompareAndSwap(true, false) {
		log.Printf("[openvoice] trying again")
		ovWarmed.Store(false)
		warmOpenVoice(voiceName)
	}
}

func openVoiceDir() string {
	dir, err := dataDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "openvoice")
}

func ovPython(dir string) string { return filepath.Join(dir, "venv", "Scripts", "python.exe") }

func openVoiceInstalled(dir string) bool {
	return dir != "" &&
		fileExists(ovPython(dir)) &&
		fileExists(filepath.Join(dir, "server.py")) &&
		fileExists(filepath.Join(dir, "ckpt", "checkpoint.pth"))
}

// openVoiceTarget is the target recording, or "" when the voice is not changed.
func openVoiceTarget(dir string) string {
	p := filepath.Join(dir, "voice.wav")
	if openVoiceInstalled(dir) && fileExists(p) {
		return p
	}
	return ""
}

// warmOpenVoice starts the converter and makes the embeddings in the
// background (~15 s, mostly CUDA start-up). Until it is ready replies keep
// the plain Supertonic voice.
func warmOpenVoice(voiceName string) {
	dir := openVoiceDir()
	target := openVoiceTarget(dir)
	if target == "" || ovWarmed.Swap(true) {
		return
	}
	go func() {
		started := time.Now()
		if err := prepareOpenVoice(dir, target, voiceName); err != nil {
			ovMu.Lock()
			stopOpenVoiceServer()
			ovMu.Unlock()
			ovFail()
			log.Printf("[openvoice] off: %v", err)
			return
		}
		ovReady.Store(true)
		log.Printf("[openvoice] ready in %s (voice %s → %s)", time.Since(started).Round(time.Millisecond), voiceName, filepath.Base(target))
	}()
}

func prepareOpenVoice(dir, target, voiceName string) error {
	ovMu.Lock()
	defer ovMu.Unlock()
	s, err := startOpenVoiceServer(dir)
	if err != nil {
		return err
	}
	ov = s
	// The target's embedding is made again whenever the recording changes.
	tgtSE := filepath.Join(dir, "voice.se.pt")
	if stale(tgtSE, target) {
		if err := ovRequest(map[string]any{"op": "embed", "ref": target, "out": tgtSE}); err != nil {
			return fmt.Errorf("목소리 녹음 분석 실패: %w", err)
		}
	}
	return ensureSourceSELocked(dir, voiceName)
}

// ensureSourceSELocked makes the embedding of a Supertonic voice's own timbre
// the first time that voice is converted — the voice can be changed in the
// settings at any time. Callers hold ovMu.
func ensureSourceSELocked(dir, voiceName string) error {
	srcSE := filepath.Join(dir, "src_"+voiceName+".se.pt")
	if !fileExists(srcSE) {
		ref := filepath.Join(dir, "src_"+voiceName+".wav")
		ttsMu.Lock()
		err := ttsRequest(map[string]any{"text": ovRefText, "voice": voiceName, "speed": ttsSpeed, "steps": ttsSteps, "out": ref})
		ttsMu.Unlock()
		if err != nil {
			return err
		}
		if err := ovRequest(map[string]any{"op": "embed", "ref": ref, "out": srcSE}); err != nil {
			return err
		}
	}
	return nil
}

func stale(derived, source string) bool {
	d, err := os.Stat(derived)
	if err != nil {
		return true
	}
	s, err := os.Stat(source)
	return err != nil || s.ModTime().After(d.ModTime())
}

// convertVoice returns wavPath repainted in the target voice, or "" when the
// voice is not being changed (or the conversion failed).
func convertVoice(wavPath, voiceName string) string {
	ovRetryDue(voiceName)
	if !ovReady.Load() || ovFailed.Load() {
		return ""
	}
	dir := openVoiceDir()
	out := strings.TrimSuffix(wavPath, ".wav") + ".ov.wav"
	ovMu.Lock()
	err := ensureSourceSELocked(dir, voiceName)
	if err == nil {
		err = ovRequest(map[string]any{
			"op":     "convert",
			"src":    wavPath,
			"src_se": filepath.Join(dir, "src_"+voiceName+".se.pt"),
			"tgt_se": filepath.Join(dir, "voice.se.pt"),
			"out":    out,
			"tau":    ovTau,
		})
	}
	ovMu.Unlock()
	if err != nil {
		log.Printf("[openvoice] convert failed, plain voice: %v", err)
		return ""
	}
	return out
}

// ovRequest sends one request to the running converter. Callers hold ovMu.
// A dead or hung converter is dropped and only brought back after
// ovRetryAfter: it costs ~15 s to start, and the plain voice is a fine
// fallback meanwhile.
func ovRequest(req map[string]any) error {
	if ov == nil {
		return errors.New("OpenVoice 가 실행 중이 아닙니다")
	}
	line, _ := json.Marshal(req)
	if _, err := ov.in.Write(append(line, '\n')); err != nil || !scanWithin(ov.out, ttsTimeout) {
		stopOpenVoiceServer()
		ovFail()
		return errors.New("OpenVoice 가 응답하지 않습니다")
	}
	var resp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(ov.out.Bytes(), &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("OpenVoice: %s", resp.Error)
	}
	return nil
}

func startOpenVoiceServer(dir string) (*ttsServer, error) {
	return startPythonServer(dir, "server.py", "[openvoice]")
}

// startPythonServer runs a script of OpenVoice's Python environment that
// answers one JSON line per request, and waits for its "ready" line.
func startPythonServer(dir, script, tag string) (*ttsServer, error) {
	cmd := exec.Command(ovPython(dir), "-u", filepath.Join(dir, script))
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8")
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
	killWithApp(cmd.Process) // never left holding the GPU after the app is killed
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	if !scanWithin(sc, 2*time.Minute) || !strings.Contains(sc.Text(), `"ready": true`) {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("%s 시작 실패: %s", script, sc.Text())
	}
	log.Printf("%s server started (pid %d) %s", tag, cmd.Process.Pid, sc.Text())
	return &ttsServer{cmd: cmd, in: in, out: sc}, nil
}

func stopOpenVoiceServer() {
	if ov == nil {
		return
	}
	_ = ov.in.Close()
	_ = ov.cmd.Process.Kill()
	_ = ov.cmd.Wait()
	ov = nil
}
