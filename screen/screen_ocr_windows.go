//go:build windows

package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
)

// ocr / click_text — the Windows side: capture, run the built-in OCR engine
// through Windows PowerShell 5.1 (which can load WinRT types without any
// extra dependency), and click. Pure logic is in screen_ocr_logic_windows.go.

//go:embed screen_ocr.ps1
var ocrScript string

// ocrTimeout bounds one PowerShell OCR run (startup ~0.5s, OCR well under 1s
// for a window; the bound is for a wedged WinRT call).
var ocrTimeout = 20 * time.Second

// ocrRunner runs the script and returns its stdout; a var for tests.
var ocrRunner = runOCRScript

// psQuote makes s safe inside a single-quoted PowerShell string literal.
func psQuote(s string) string { return strings.ReplaceAll(s, "'", "''") }

// sanitizeOCRLang keeps a BCP-47 tag's legal characters only ("" = auto).
func sanitizeOCRLang(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		if r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	if strings.EqualFold(b.String(), "auto") {
		return ""
	}
	return b.String()
}

func powershellPath() string {
	if root := os.Getenv("SystemRoot"); root != "" {
		p := filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "powershell.exe"
}

func runOCRScript(imagePath, lang string) ([]byte, error) {
	script := strings.NewReplacer("__PATH__", psQuote(imagePath), "__LANG__", psQuote(lang)).Replace(ocrScript)
	u := utf16.Encode([]rune(script))
	raw := make([]byte, len(u)*2)
	for i, c := range u {
		raw[2*i], raw[2*i+1] = byte(c), byte(c>>8)
	}
	ctx, cancel := context.WithTimeout(context.Background(), ocrTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, powershellPath(), "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", base64.StdEncoding.EncodeToString(raw))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("Windows OCR timed out after %s", ocrTimeout)
	}
	if err != nil && stdout.Len() == 0 {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		return nil, fmt.Errorf("powershell OCR failed: %v %s", err, msg)
	}
	return stdout.Bytes(), nil
}

// runOCR OCRs a PNG whose pixel (0,0) is screen (originX, originY) and returns
// lines in screen coordinates.
func runOCR(pngBytes []byte, originX, originY int, lang string) (ocrResult, error) {
	src, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		return ocrResult{}, fmt.Errorf("ocr: decode capture: %w", err)
	}
	b := src.Bounds()
	f := ocrUpscale(b.Dx(), b.Dy())
	enc, err := encodePNG(upscaleBilinear(src, f))
	if err != nil {
		return ocrResult{}, err
	}
	tmp, err := os.CreateTemp("", "aglink-ocr-*.png")
	if err != nil {
		return ocrResult{}, fmt.Errorf("ocr: temp file: %w", err)
	}
	path := tmp.Name()
	defer os.Remove(path)
	if _, err := tmp.Write(enc); err != nil {
		tmp.Close()
		return ocrResult{}, fmt.Errorf("ocr: write temp file: %w", err)
	}
	tmp.Close()

	lang = sanitizeOCRLang(lang)
	out, err := ocrRunner(path, lang)
	if err != nil {
		return ocrResult{}, err
	}
	res, err := parseOCROutput(out, originX, originY, float64(f))
	if err != nil {
		return ocrResult{}, err
	}
	switch res.Error {
	case "":
		return res, nil
	case "lang_unavailable", "no_engine":
		return res, errors.New(ocrLangHint(lang, res.Available))
	default:
		return res, fmt.Errorf("Windows OCR failed: %s", res.Error)
	}
}

// ocrRegion describes what ocr captures: an explicit rectangle (absolute, or
// relative to window), else the window, else the foreground window.
type ocrRegion struct {
	Window     string
	HasRect    bool
	X, Y, W, H int
}

// ocrCapture grabs the region and returns the PNG, its screen origin, the
// target hwnd (0 for a bare screen rectangle) and a label for messages.
func ocrCapture(r ocrRegion) (pngBytes []byte, originX, originY int, hwnd uintptr, label string, err error) {
	if r.HasRect {
		pngBytes, originX, originY, err = captureRegionAt(r.Window, r.X, r.Y, r.W, r.H)
		if err != nil {
			return nil, 0, 0, 0, "", err
		}
		label = fmt.Sprintf("screen region (%d,%d) %dx%d", originX, originY, r.W, r.H)
		if strings.TrimSpace(r.Window) != "" {
			hwnd, _ = findTopWindow(r.Window)
			label = fmt.Sprintf("region of window %q at screen (%d,%d) %dx%d", getWindowText(hwnd), originX, originY, r.W, r.H)
		}
		return pngBytes, originX, originY, hwnd, label, nil
	}
	hwnd, err = markTargetWindow(r.Window)
	if err != nil {
		return nil, 0, 0, 0, "", err
	}
	pngBytes, originX, originY, _, _, err = captureWindow(fmt.Sprintf("0x%x", hwnd))
	if err != nil {
		return nil, 0, 0, 0, "", err
	}
	return pngBytes, originX, originY, hwnd, fmt.Sprintf("window %q", getWindowText(hwnd)), nil
}

// ocrTool implements the ocr tool.
func ocrTool(r ocrRegion, lang string) (string, error) {
	ensureDPIAware()
	pngBytes, ox, oy, _, label, err := ocrCapture(r)
	if err != nil {
		return "", err
	}
	res, err := runOCR(pngBytes, ox, oy, lang)
	if err != nil {
		return "", err
	}
	return ocrText(label, res.Lang, res.Lines, ocrMaxReplyChars), nil
}

// clickText implements click_text: OCR, find, click.
func clickText(text string, r ocrRegion, nth int, double bool, lang string) (string, error) {
	ensureDPIAware()
	if normalizeOCRText(text) == "" {
		return "", fmt.Errorf("empty text")
	}
	// The click is a real mouse click, so the target must be on top — and the
	// capture must show it rather than whatever covers it.
	if w := strings.TrimSpace(r.Window); w != "" {
		h, err := resolveTargetWindow(w)
		if err != nil {
			return "", err
		}
		if fg, _, _ := procGetForegroundWindow.Call(); fg != h {
			if err := bringToFront(h); err != nil {
				return "", fmt.Errorf("focus %q: %w", w, err)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	pngBytes, ox, oy, hwnd, label, err := ocrCapture(r)
	if err != nil {
		return "", err
	}
	res, err := runOCR(pngBytes, ox, oy, lang)
	if err != nil {
		return "", err
	}
	ms := findOCRText(res.Lines, text)
	if len(ms) == 0 {
		var seen []string
		for i, l := range res.Lines {
			if i == 15 {
				seen = append(seen, "…")
				break
			}
			seen = append(seen, onelinePreview(l.Text, 60))
		}
		return "", fmt.Errorf("no text matching %q found by OCR in %s (%d line(s) read: %s)", text, label, len(res.Lines), strings.Join(seen, " | "))
	}
	if nth < 0 || nth >= len(ms) {
		nth = 0
	}
	m := ms[nth]
	x, y := m.Rect.CX(), m.Rect.CY()
	how := "left-clicked"
	if double {
		how = "double-clicked"
		err = mouseDouble(x, y)
	} else {
		err = mouseClick(x, y, "left")
	}
	if err != nil {
		return "", err
	}
	warn := ""
	if hwnd != 0 {
		warn = uipiWarning(hwnd, "click")
	}
	return fmt.Sprintf("%s %q (line %q) at (%d,%d) [%d match(es); nth=%d]%s", how, m.Text, onelinePreview(m.Line, 80), x, y, len(ms), nth, warn), nil
}
