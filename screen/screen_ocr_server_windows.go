//go:build windows

package main

import (
	"bufio"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
)

// A one-shot OCR call pays Add-Type + WinRT type load every time — about
// 0.5s of PowerShell cold start on top of the OCR itself (screen_ocr_windows.go's
// ocrTimeout comment already flags this). A persistent PowerShell process that
// does that setup once and then serves one OCR request per stdin line removes
// that cost from every call after the first. screen_ocr.ps1 (runOCRScript) stays
// as it was — the fallback this falls back to if the server never starts or
// drops mid-session, so OCR keeps working either way.

//go:embed screen_ocr_server.ps1
var ocrServerScript string

// ocrServerRequestTimeout bounds one request/response round trip through the
// warm server — generous since a first-ever call for a new language still has
// to create that OcrEngine, but far under ocrTimeout (the cold-start fallback's
// much larger ceiling, which also has to cover PowerShell's own startup).
var ocrServerRequestTimeout = 10 * time.Second

// ocrServerStartTimeout bounds waiting for the one-time setup (Add-Type, WinRT
// type load) to finish and the server to print its {"ready":true} line.
var ocrServerStartTimeout = 15 * time.Second

type ocrServer struct {
	mu     sync.Mutex // serializes requests; one OCR call at a time through one pipe
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lines  chan string // one decoded line per Out-Json call, fed by readLoop
	broken chan struct{}
}

var (
	ocrServerMu   sync.Mutex // guards the singleton below, separate from the server's own call-serializing mu
	ocrServerInst *ocrServer
)

// runOCRScriptFast is ocrRunner's default: the warm server when it is up (or
// can be started), the original one-shot script otherwise. Either path
// returns the same shape of bytes (one Out-Json line), so parseOCROutput does
// not need to know which one answered.
func runOCRScriptFast(imagePath, lang string) ([]byte, error) {
	srv, err := getOCRServer()
	if err != nil {
		return runOCRScript(imagePath, lang)
	}
	out, err := srv.call(imagePath, lang)
	if err != nil {
		// The server might just have died (the user closed PowerShell some
		// other way, a WinRT call wedged it, …) — drop it so the next call
		// gets a fresh one, and finish this one through the cold path so a
		// single flaky server never turns into a visible OCR failure.
		ocrServerMu.Lock()
		if ocrServerInst == srv {
			ocrServerInst = nil
		}
		ocrServerMu.Unlock()
		return runOCRScript(imagePath, lang)
	}
	return out, nil
}

// getOCRServer returns the running server, starting one on first use.
func getOCRServer() (*ocrServer, error) {
	ocrServerMu.Lock()
	defer ocrServerMu.Unlock()
	if ocrServerInst != nil {
		select {
		case <-ocrServerInst.broken:
			ocrServerInst = nil
		default:
			return ocrServerInst, nil
		}
	}
	srv, err := startOCRServer()
	if err != nil {
		return nil, err
	}
	ocrServerInst = srv
	return srv, nil
}

func startOCRServer() (*ocrServer, error) {
	u := utf16.Encode([]rune(ocrServerScript))
	raw := make([]byte, len(u)*2)
	for i, c := range u {
		raw[2*i], raw[2*i+1] = byte(c), byte(c>>8)
	}
	cmd := exec.Command(powershellPath(), "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", base64.StdEncoding.EncodeToString(raw))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	srv := &ocrServer{cmd: cmd, stdin: stdin, lines: make(chan string, 4), broken: make(chan struct{})}
	go srv.readLoop(stdout)
	go func() {
		_ = cmd.Wait()
		srv.markBroken()
	}()
	if _, err := srv.readLine(ocrServerStartTimeout); err != nil { // the {"ready":true} line
		srv.kill()
		return nil, fmt.Errorf("ocr server did not start: %w", err)
	}
	return srv, nil
}

func (s *ocrServer) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // one OCR result line can hold many words
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		select {
		case s.lines <- line:
		case <-s.broken:
			return
		}
	}
	s.markBroken()
}

func (s *ocrServer) markBroken() {
	select {
	case <-s.broken:
	default:
		close(s.broken)
	}
}

func (s *ocrServer) readLine(timeout time.Duration) ([]byte, error) {
	select {
	case line := <-s.lines:
		return []byte(line), nil
	case <-s.broken:
		return nil, fmt.Errorf("ocr server process exited")
	case <-time.After(timeout):
		return nil, fmt.Errorf("ocr server did not answer within %s", timeout)
	}
}

func (s *ocrServer) call(path, lang string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req, err := json.Marshal(map[string]string{"path": path, "lang": lang})
	if err != nil {
		return nil, err
	}
	if _, err := s.stdin.Write(append(req, '\n')); err != nil {
		return nil, err
	}
	return s.readLine(ocrServerRequestTimeout)
}

func (s *ocrServer) kill() {
	s.markBroken()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
}
