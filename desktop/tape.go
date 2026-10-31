package main

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Voice recording ("녹음해 줘"): the frontend streams 16 kHz mono 16-bit PCM
// here until told to stop ("녹음 끝내"), and what it recognises along the way
// goes into a text file next to the WAV, stamped with the time into the
// recording. There is no length limit. The WAV header is brought up to date
// after every piece, so a crash or power cut still leaves a playable file.
const (
	tapeRate     = 16000
	tapeDirName  = "aglink 녹음"
	wavHeaderLen = 44
)

type tapeState struct {
	mu      sync.Mutex
	wav     *os.File
	txt     *os.File
	path    string
	samples int64 // written so far
}

var tape tapeState

// tapeDir is Documents\aglink 녹음, where the user finds it in Explorer.
func tapeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "Documents", tapeDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func wavHeader(dataBytes int64) []byte {
	h := make([]byte, wavHeaderLen)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+dataBytes))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1) // PCM
	binary.LittleEndian.PutUint16(h[22:], 1) // mono
	binary.LittleEndian.PutUint32(h[24:], tapeRate)
	binary.LittleEndian.PutUint32(h[28:], tapeRate*2)
	binary.LittleEndian.PutUint16(h[32:], 2)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(dataBytes))
	return h
}

// TapeStart opens a new recording and returns its WAV path. A recording left
// open is closed first.
func (v *VoiceService) TapeStart() (string, error) {
	tape.mu.Lock()
	defer tape.mu.Unlock()
	tape.closeLocked()
	dir, err := tapeDir()
	if err != nil {
		return "", err
	}
	base := filepath.Join(dir, "녹음 "+time.Now().Format("2006-01-02 15-04-05"))
	wav, err := os.Create(base + ".wav")
	if err != nil {
		return "", err
	}
	if _, err := wav.Write(wavHeader(0)); err != nil {
		wav.Close()
		return "", err
	}
	txt, err := os.Create(base + ".txt")
	if err != nil {
		wav.Close()
		return "", err
	}
	fmt.Fprintf(txt, "녹음 %s\n\n", time.Now().Format("2006-01-02 15:04:05"))
	tape.wav, tape.txt, tape.path, tape.samples = wav, txt, base+".wav", 0
	return tape.path, nil
}

// TapeAppend adds a piece of the recording: base64 16 kHz mono 16-bit PCM.
func (v *VoiceService) TapeAppend(b64 string) error {
	pcm, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return err
	}
	tape.mu.Lock()
	defer tape.mu.Unlock()
	if tape.wav == nil {
		return errors.New("녹음 중이 아닙니다")
	}
	pcm = pcm[:len(pcm)&^1]
	if _, err := tape.wav.Seek(wavHeaderLen+tape.samples*2, 0); err != nil {
		return err
	}
	if _, err := tape.wav.Write(pcm); err != nil {
		return err
	}
	tape.samples += int64(len(pcm) / 2)
	_, err = tape.wav.WriteAt(wavHeader(tape.samples*2), 0)
	return err
}

// TapeNote writes a recognised line to the text file, stamped with the time
// into the recording where it was said (atSec).
func (v *VoiceService) TapeNote(atSec float64, text string) error {
	tape.mu.Lock()
	defer tape.mu.Unlock()
	if tape.txt == nil {
		return errors.New("녹음 중이 아닙니다")
	}
	s := int(atSec)
	_, err := fmt.Fprintf(tape.txt, "[%s] %s\n", clockOf(s), strings.TrimSpace(text))
	return err
}

func clockOf(s int) string {
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%02d:%02d", s/60, s%60)
}

// TapeStop closes the recording and reports it as JSON {"path","seconds"}.
func (v *VoiceService) TapeStop() (string, error) {
	tape.mu.Lock()
	defer tape.mu.Unlock()
	if tape.wav == nil {
		return "", errors.New("녹음 중이 아닙니다")
	}
	path, secs := tape.path, float64(tape.samples)/tapeRate
	tape.closeLocked()
	b, _ := json.Marshal(map[string]any{"path": path, "seconds": secs})
	return string(b), nil
}

func (t *tapeState) closeLocked() {
	if t.wav != nil {
		t.wav.Close()
		t.wav = nil
	}
	if t.txt != nil {
		t.txt.Close()
		t.txt = nil
	}
}

// OpenTapeFolder shows the recordings folder in Explorer.
func (v *VoiceService) OpenTapeFolder() error {
	dir, err := tapeDir()
	if err != nil {
		return err
	}
	return exec.Command("explorer.exe", dir).Start()
}
