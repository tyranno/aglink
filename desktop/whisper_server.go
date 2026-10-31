package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// The fast path for speech recognition: whisper.cpp's CUDA build, run as
// whisper-server.exe from <data dir>/whisper/cuda and kept running so the
// model loads once. A short reply then transcribes in ~0.3 s instead of the
// ~1.5 s of starting whisper-cli on the CPU for every clip. Without a CUDA
// build (or an NVIDIA GPU) Transcribe keeps using whisper-cli.
const (
	whisperCUDAURL = "https://github.com/ggml-org/whisper.cpp/releases/download/b5454/whisper-bin-win-cuda-12.4.0-x64.zip"
	whisperCUDADir = "cuda"
)

type whisperServer struct {
	cmd  *exec.Cmd
	base string // http://127.0.0.1:port
}

// wsMaxFailures: a server that fails to start or dies this many times
// rests for wsRestFor (whisper-cli does the work meanwhile) and is then tried
// again — one timed-out request or driver hiccup should not cost the rest of
// the day, nor should a bad stretch.
const (
	wsMaxFailures = 3
	wsRestFor     = 10 * time.Minute
)

var (
	wsMu       sync.Mutex // guards ws, wsFailures, wsFailedAt, wsStarting
	ws         *whisperServer
	wsFailures int
	wsFailedAt time.Time
	wsStarting bool
)

func whisperServerPath() string {
	dir, err := whisperDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, whisperCUDADir, "whisper-server.exe")
}

// fastWhisperReady reports whether the resident GPU server is up.
func fastWhisperReady() bool {
	wsMu.Lock()
	defer wsMu.Unlock()
	return ws != nil
}

// warmWhisperServer starts the server in the background if it is installed and
// not running yet. Safe to call often.
func warmWhisperServer() {
	wsMu.Lock()
	defer wsMu.Unlock()
	if wsFailures >= wsMaxFailures && time.Since(wsFailedAt) >= wsRestFor {
		log.Printf("[voice] whisper-server: trying again after a rest")
		wsFailures = 0
	}
	if ws != nil || wsFailures >= wsMaxFailures || wsStarting || !fileExists(whisperServerPath()) {
		return
	}
	wsStarting = true
	go func() {
		s, err := startWhisperServer()
		wsMu.Lock()
		defer wsMu.Unlock()
		wsStarting = false
		if err != nil {
			wsFailures++
			wsFailedAt = time.Now()
			log.Printf("[voice] whisper-server unavailable (%d/%d), using whisper-cli: %v", wsFailures, wsMaxFailures, err)
			return
		}
		ws = s
	}()
}

func startWhisperServer() (*whisperServer, error) {
	_, model, err := whisperPaths()
	if err != nil {
		return nil, err
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	exe := whisperServerPath()
	cmd := exec.Command(exe, "-m", model, "-l", "ko", "-t", "4",
		"--host", "127.0.0.1", "--port", fmt.Sprint(port))
	cmd.Dir = filepath.Dir(exe)
	hideConsole(cmd)
	started := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	killWithApp(cmd.Process) // never left holding the GPU after the app is killed
	s := &whisperServer{cmd: cmd, base: fmt.Sprintf("http://127.0.0.1:%d", port)}
	// Loading the model, then a first request (which also compiles the CUDA
	// kernels on a new driver), before anyone waits on it.
	deadline := time.Now().Add(90 * time.Second)
	for {
		if _, err := s.transcribe(silentWav(time.Second), 10*time.Second); err == nil {
			break
		}
		if time.Now().After(deadline) {
			s.stop()
			return nil, errors.New("응답 없음")
		}
		time.Sleep(300 * time.Millisecond)
	}
	log.Printf("[voice] whisper-server ready on GPU in %s (pid %d)", time.Since(started).Round(time.Millisecond), cmd.Process.Pid)
	return s, nil
}

func (s *whisperServer) stop() {
	_ = s.cmd.Process.Kill()
	_ = s.cmd.Wait()
}

func (s *whisperServer) transcribe(wav []byte, timeout time.Duration) (string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "clip.wav")
	_, _ = fw.Write(wav)
	_ = mw.WriteField("response_format", "text")
	_ = mw.WriteField("language", "ko")
	_ = mw.Close()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+"/inference", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %s: %s", resp.Status, bytes.TrimSpace(b))
	}
	return string(b), nil
}

// transcribeFast runs a clip through the resident server. ok is false when the
// server is not up, so the caller uses whisper-cli; a server that fails a
// request is replaced by a fresh one in the background.
func transcribeFast(wav []byte) (text string, ok bool) {
	wsMu.Lock()
	s := ws
	wsMu.Unlock()
	if s == nil {
		return "", false
	}
	// A minute of speech takes ~1 s on the GPU; 20 s means it is stuck.
	text, err := s.transcribe(wav, 20*time.Second)
	if err != nil {
		wsMu.Lock()
		if ws == s {
			ws = nil
			wsFailures++
			wsFailedAt = time.Now()
		}
		n := wsFailures
		wsMu.Unlock()
		log.Printf("[voice] whisper-server failed (%d/%d), whisper-cli for now: %v", n, wsMaxFailures, err)
		s.stop()
		warmWhisperServer()
		return "", false
	}
	return text, true
}

func stopWhisperServer() {
	wsMu.Lock()
	s := ws
	ws = nil
	wsMu.Unlock()
	if s != nil {
		s.stop()
	}
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// silentWav is d of 16 kHz mono 16-bit silence.
func silentWav(d time.Duration) []byte {
	n := int(d.Seconds() * 16000)
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+n*2))
	b.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(16000), uint32(32000), uint16(2), uint16(16)} {
		_ = binary.Write(&b, binary.LittleEndian, v)
	}
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(n*2))
	b.Write(make([]byte, n*2))
	return b.Bytes()
}

// installWhisperCUDA adds the CUDA build next to the CPU one when the PC has an
// NVIDIA GPU. It is optional: a failure leaves speech recognition on the CPU.
func installWhisperCUDA() {
	if fileExists(whisperServerPath()) || !hasNvidiaGPU() {
		return
	}
	dir, err := whisperDir()
	if err != nil {
		return
	}
	zipPath := filepath.Join(dir, "whisper-cuda.zip")
	err = download(whisperCUDAURL, zipPath, "gpu")
	if err == nil {
		err = extractRelease(zipPath, filepath.Join(dir, whisperCUDADir), "whisper-server.exe")
	}
	os.Remove(zipPath)
	if err != nil {
		log.Printf("[voice] CUDA build not installed: %v", err)
		return
	}
	warmWhisperServer()
}

func hasNvidiaGPU() bool {
	cmd := exec.Command("nvidia-smi", "-L")
	hideConsole(cmd)
	out, err := cmd.Output()
	return err == nil && bytes.Contains(out, []byte("GPU"))
}
