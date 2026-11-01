package main

// Telling the user's voice from everyone else's. In voice conversation the mic
// also picks up people talking nearby (and our own reply from the speakers);
// once the user has enrolled their voice, a clip that does not sound like
// them is not sent. speaker_server.py (ECAPA-TDNN, ~0.03 s a clip on the GPU)
// runs in OpenVoice's Python environment under <data>/openvoice, next to the
// enrollment: me.wav (the recording) and me.pt (its voice embedding).
//
// Anything missing or failing just means every clip counts as the user's,
// as before.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

var (
	spkMu      sync.Mutex // one request at a time; guards spk
	spk        *ttsServer
	spkWarming atomic.Bool
	spkBadAt   time.Time // last failed start; guarded by spkMu
)

func speakerInstalled(dir string) bool {
	return dir != "" &&
		fileExists(ovPython(dir)) &&
		fileExists(filepath.Join(dir, "speaker_server.py")) &&
		fileExists(filepath.Join(dir, "ecapa", "embedding_model.ckpt"))
}

func speakerEnrollment(dir string) string { return filepath.Join(dir, "me.pt") }

// speakerAnchor is the embedding of the enrollment recording itself. me.pt
// learns from everyday clips; a clip far from the anchor is never learned, so
// me.pt cannot drift toward someone else's voice.
func speakerAnchor(dir string) string { return filepath.Join(dir, "me.base.pt") }

// warmSpeaker starts the server in the background (~5 s) so the first clip
// is not held up by it.
func warmSpeaker() {
	dir := openVoiceDir()
	if !speakerInstalled(dir) || spkWarming.Swap(true) {
		return
	}
	go func() {
		defer spkWarming.Store(false)
		spkMu.Lock()
		defer spkMu.Unlock()
		_ = ensureSpeakerLocked(dir)
	}()
}

// ensureSpeakerLocked starts the server unless it runs or failed to start a
// moment ago. Callers hold spkMu.
func ensureSpeakerLocked(dir string) error {
	if spk != nil {
		return nil
	}
	if !spkBadAt.IsZero() && time.Since(spkBadAt) < ttsRetryAfter {
		return errors.New("목소리 구별기를 잠시 쓸 수 없습니다")
	}
	started := time.Now()
	s, err := startPythonServer(dir, "speaker_server.py", "[speaker]")
	if err != nil {
		spkBadAt = time.Now()
		log.Printf("[speaker] start failed: %v", err)
		return err
	}
	spk = s
	log.Printf("[speaker] ready in %s", time.Since(started).Round(time.Millisecond))
	return nil
}

// spkRequestLocked sends one request; a dead or hung server is dropped and
// started again on a later request. Callers hold spkMu.
func spkRequestLocked(req map[string]any) (float64, error) {
	line, _ := json.Marshal(req)
	if _, err := spk.in.Write(append(line, '\n')); err != nil || !scanWithin(spk.out, ttsTimeout) {
		_ = spk.in.Close()
		_ = spk.cmd.Process.Kill()
		_ = spk.cmd.Wait()
		spk = nil
		return 0, errors.New("목소리 구별기가 응답하지 않습니다")
	}
	var resp struct {
		OK    bool    `json:"ok"`
		Error string  `json:"error"`
		Score float64 `json:"score"`
	}
	if err := json.Unmarshal(spk.out.Bytes(), &resp); err != nil {
		return 0, err
	}
	if !resp.OK {
		return 0, fmt.Errorf("목소리 구별기: %s", resp.Error)
	}
	return resp.Score, nil
}

func writeTempWav(b64 string) (string, error) {
	wav, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "aglink-speaker-*.wav")
	if err != nil {
		return "", err
	}
	_, err = f.Write(wav)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// SpeakerStatus reports {"available":…, "enrolled":…} as JSON.
func (v *VoiceService) SpeakerStatus() string {
	dir := openVoiceDir()
	ok := speakerInstalled(dir)
	if ok {
		warmSpeaker()
	}
	b, _ := json.Marshal(map[string]bool{
		"available": ok,
		"enrolled":  ok && fileExists(speakerEnrollment(dir)),
	})
	return string(b)
}

// EnrollSpeaker takes a recording of the user (base64 WAV, ~15 s of them
// talking) as the voice to listen to from now on.
func (v *VoiceService) EnrollSpeaker(b64 string) error {
	dir := openVoiceDir()
	if !speakerInstalled(dir) {
		return errors.New("목소리 구별기가 설치되어 있지 않습니다")
	}
	wav, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return err
	}
	ref := filepath.Join(dir, "me.wav")
	if err := os.WriteFile(ref, wav, 0o600); err != nil {
		return err
	}
	spkMu.Lock()
	defer spkMu.Unlock()
	if err := ensureSpeakerLocked(dir); err != nil {
		return err
	}
	for _, out := range []string{speakerEnrollment(dir), speakerAnchor(dir)} {
		if _, err := spkRequestLocked(map[string]any{"op": "embed", "wav": ref, "out": out}); err != nil {
			return err
		}
	}
	log.Printf("[speaker] enrolled %d bytes", len(wav))
	return nil
}

// ClearSpeaker forgets the enrolled voice: every clip counts as the user's again.
func (v *VoiceService) ClearSpeaker() error {
	dir := openVoiceDir()
	for _, name := range []string{"me.wav", "me.pt", "me.base.pt"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// SpeakerScore says how much a clip (base64 WAV) sounds like the enrolled
// user: a cosine similarity, about 0.5 and up for them and under 0.3 for
// others. -1 means it cannot tell (nobody enrolled, or the server is down).
func (v *VoiceService) SpeakerScore(b64 string) (float64, error) {
	dir := openVoiceDir()
	me := speakerEnrollment(dir)
	if !speakerInstalled(dir) || !fileExists(me) {
		return -1, nil
	}
	path, err := writeTempWav(b64)
	if err != nil {
		return -1, err
	}
	defer os.Remove(path)
	spkMu.Lock()
	defer spkMu.Unlock()
	if spk == nil {
		// Not waited for here: a clip must not sit behind a ~5 s start-up.
		if !spkWarming.Load() {
			go warmSpeaker()
		}
		return -1, nil
	}
	anchor := speakerAnchor(dir)
	if !fileExists(anchor) && fileExists(filepath.Join(dir, "me.wav")) {
		// Enrolled before learning existed: the anchor is the recording itself.
		_, _ = spkRequestLocked(map[string]any{"op": "embed", "wav": filepath.Join(dir, "me.wav"), "out": anchor})
	}
	score, err := spkRequestLocked(map[string]any{"op": "score", "wav": path, "ref": me, "base": anchor, "adapt_min": speakerAdaptMin})
	if err != nil {
		log.Printf("[speaker] score failed: %v", err)
		return -1, nil
	}
	return score, nil
}

func stopSpeakerServer() {
	spkMu.Lock()
	defer spkMu.Unlock()
	if spk == nil {
		return
	}
	_ = spk.in.Close()
	_ = spk.cmd.Process.Kill()
	_ = spk.cmd.Wait()
	spk = nil
}

// speakerAdaptMin: a clip scoring at least this is surely the user, so
// speaker_server.py nudges the enrolled voice toward it. The user's own clips
// score ~0.45–0.7 against the one enrollment recording and others ≤0.3;
// learning from everyday clips widens that gap.
const speakerAdaptMin = 0.6
