//go:build windows

package main

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"testing"
)

// A trimmed real run of screen_ocr.ps1 (PowerShell progress noise first, a
// non-ASCII line escaped as \u, and a one-word line whose "w" is an object).
const sampleOCROutput = `#< CLIXML
{"lang":"ko","available":["en-US","ko"],"lines":[` +
	`{"t":"\ud30c\uc77c \ud3b8\uc9d1","w":[{"t":"\ud30c\uc77c","x":20,"y":10,"w":40,"h":20},{"t":"\ud3b8\uc9d1","x":80,"y":10,"w":40,"h":20}]},` +
	`{"t":"Sign in","w":[{"t":"Sign","x":20,"y":200,"w":50,"h":20},{"t":"in","x":76,"y":200,"w":20,"h":20}]},` +
	`{"t":"OK","w":{"t":"OK","x":300,"y":8,"w":30,"h":24}}]}
`

func TestParseOCROutputConvertsAndOrders(t *testing.T) {
	// Image was upscaled 2x before OCR; the capture origin is (1000, 500).
	res, err := parseOCROutput([]byte(sampleOCROutput), 1000, 500, 2)
	if err != nil {
		t.Fatal(err)
	}
	if res.Lang != "ko" || len(res.Available) != 2 {
		t.Fatalf("lang/available: %q %v", res.Lang, res.Available)
	}
	if len(res.Lines) != 3 {
		t.Fatalf("lines: %d", len(res.Lines))
	}
	// "OK" (y=8) shares the first row with "파일 편집" (y=10..30) and is to its right.
	if res.Lines[0].Text != "파일 편집" || res.Lines[1].Text != "OK" || res.Lines[2].Text != "Sign in" {
		t.Fatalf("order: %q %q %q", res.Lines[0].Text, res.Lines[1].Text, res.Lines[2].Text)
	}
	// 파일 편집 spans image x 20..120, y 10..30 → screen 1010..1060, 505..515.
	if r := res.Lines[0].Rect; r != (markRect{1010, 505, 1060, 515}) {
		t.Fatalf("line rect %v", r)
	}
	if c := res.Lines[0].Rect; c.CX() != 1035 || c.CY() != 510 {
		t.Fatalf("center (%d,%d)", c.CX(), c.CY())
	}
	txt := ocrText("window \"x\"", res.Lang, res.Lines, ocrMaxReplyChars)
	if !strings.Contains(txt, "파일 편집 — center(1035,510)") || !strings.Contains(txt, "3 line(s)") {
		t.Fatalf("ocrText:\n%s", txt)
	}
}

func TestParseOCROutputErrors(t *testing.T) {
	res, err := parseOCROutput([]byte(`{"error":"lang_unavailable","lang":"ja","available":"en-US"}`), 0, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "lang_unavailable" || len(res.Available) != 1 || res.Available[0] != "en-US" {
		t.Fatalf("got %+v", res)
	}
	if _, err := parseOCROutput([]byte("garbage only"), 0, 0, 1); err == nil {
		t.Fatal("no JSON line should be an error")
	}
	hint := ocrLangHint("ko", []string{"en-US"})
	if !strings.Contains(hint, "Language.OCR~~~ko-KR~0.0.1.0") || !strings.Contains(hint, "en-US") {
		t.Fatalf("hint: %s", hint)
	}
}

func TestFindOCRTextMatching(t *testing.T) {
	lines := []ocrLine{
		{Text: "로그 인 하기", Words: []ocrWord{
			{"로그", markRect{0, 0, 20, 10}}, {"인", markRect{25, 0, 35, 10}}, {"하기", markRect{40, 0, 60, 10}}}},
		{Text: "Sign In", Words: []ocrWord{{"Sign", markRect{0, 50, 40, 60}}, {"In", markRect{45, 50, 60, 60}}}},
		{Text: "Signing", Words: []ocrWord{{"Signing", markRect{0, 100, 70, 110}}}},
	}
	// Space-insensitive across OCR's invented space: spans 로그+인 only.
	m := findOCRText(lines, "로그인")
	if len(m) != 1 || m[0].Rect != (markRect{0, 0, 35, 10}) || !m[0].Exact {
		t.Fatalf("로그인: %+v", m)
	}
	// Case-insensitive; the whole-word match comes before the partial one
	// inside "Signing" (space-insensitive: "signin" ⊂ "signing").
	m = findOCRText(lines, "sign in")
	if len(m) != 2 || m[0].Text != "Sign In" || !m[0].Exact || m[1].Text != "Signing" || m[1].Exact {
		t.Fatalf("sign in: %+v", m)
	}
	m = findOCRText(lines, "SIGN")
	if len(m) != 2 || !m[0].Exact || m[0].Rect.T != 50 || m[1].Exact || m[1].Rect.T != 100 {
		t.Fatalf("SIGN ordering: %+v", m)
	}
	if findOCRText(lines, "  ") != nil {
		t.Fatal("blank needle should match nothing")
	}
}

func TestOCRUpscaleAndBilinear(t *testing.T) {
	if ocrUpscale(1200, 800) != 2 || ocrUpscale(2560, 1440) != 1 {
		t.Fatal("upscale factor policy")
	}
	src := image.NewRGBA(image.Rect(0, 0, 2, 1))
	src.SetRGBA(0, 0, color.RGBA{0, 0, 0, 255})
	src.SetRGBA(1, 0, color.RGBA{200, 200, 200, 255})
	dst := upscaleBilinear(src, 2).(*image.RGBA)
	if dst.Bounds().Dx() != 4 || dst.Bounds().Dy() != 2 {
		t.Fatalf("size %v", dst.Bounds())
	}
	l, m, r := dst.RGBAAt(0, 0).R, dst.RGBAAt(1, 0).R, dst.RGBAAt(3, 0).R
	if l != 0 || r != 200 || m == 0 || m == 200 {
		t.Fatalf("not interpolated: %d %d %d", l, m, r)
	}
}

func TestSanitizeOCRLangAndQuote(t *testing.T) {
	if sanitizeOCRLang(" ko-KR'; rm ") != "ko-KRrm" || sanitizeOCRLang("auto") != "" {
		t.Fatal("sanitize")
	}
	if psQuote(`C:\a'b`) != `C:\a''b` {
		t.Fatal("psQuote")
	}
}

// TestRunOCRUsesRunnerAndCleansUp exercises runOCR end to end with a fake
// script runner: the temp PNG exists while the runner runs, is gone after,
// and the runner's coordinates come back divided by the upscale factor.
func TestRunOCRUsesRunnerAndCleansUp(t *testing.T) {
	orig := ocrRunner
	t.Cleanup(func() { ocrRunner = orig })
	var seen string
	ocrRunner = func(path, lang string) ([]byte, error) {
		seen = path
		if _, err := os.Stat(path); err != nil {
			t.Errorf("temp image missing during OCR: %v", err)
		}
		if lang != "en-US" {
			t.Errorf("lang = %q", lang)
		}
		return []byte(`{"lang":"en-US","lines":[{"t":"Hi","w":[{"t":"Hi","x":40,"y":20,"w":20,"h":10}]}]}`), nil
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 100, 50)))
	res, err := runOCR(buf.Bytes(), 10, 10, "en-US")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(seen); !os.IsNotExist(err) {
		t.Errorf("temp image not removed: %v", err)
	}
	// 2x upscale: image (40..60, 20..30) → screen 10+20..10+30, 10+10..10+15.
	if r := res.Lines[0].Rect; r != (markRect{30, 20, 40, 25}) {
		t.Fatalf("rect %v", r)
	}

	ocrRunner = func(path, lang string) ([]byte, error) {
		return []byte(`{"error":"lang_unavailable","lang":"ko","available":["en-US"]}`), nil
	}
	if _, err := runOCR(buf.Bytes(), 0, 0, "ko"); err == nil || !strings.Contains(err.Error(), "Add-WindowsCapability") {
		t.Fatalf("missing language error: %v", err)
	}
	ocrRunner = func(path, lang string) ([]byte, error) { return nil, errors.New("boom") }
	if _, err := runOCR(buf.Bytes(), 0, 0, ""); err == nil {
		t.Fatal("runner error should propagate")
	}
}
