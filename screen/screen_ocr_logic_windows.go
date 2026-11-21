//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"
	"unicode"
)

// Built-in Windows OCR (Windows.Media.Ocr) for windows UI Automation can't see
// into — games, remote desktop, canvases, images. The capture is OCR'd by a
// short PowerShell script (screen_ocr_windows.go); this file is the pure part:
// parsing its JSON, mapping image pixels back to screen pixels, rendering the
// reply and matching text for click_text.

// ocrWordJSON / ocrLineJSON are what the script prints (image pixel coords).
type ocrWordJSON struct {
	T string  `json:"t"`
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

type ocrLineJSON struct {
	T string          `json:"t"`
	W json.RawMessage `json:"w"`
}

type ocrOutputJSON struct {
	Error     string          `json:"error"`
	Lang      string          `json:"lang"`
	Available json.RawMessage `json:"available"`
	Lines     json.RawMessage `json:"lines"`
}

// ocrWord / ocrLine are OCR results in SCREEN coordinates.
type ocrWord struct {
	Text string
	Rect markRect
}

type ocrLine struct {
	Text  string
	Rect  markRect
	Words []ocrWord
}

// ocrResult is one parsed OCR run. Error is the script's own error code or
// message ("lang_unavailable", "no_engine", or an exception text).
type ocrResult struct {
	Error     string
	Lang      string
	Available []string
	Lines     []ocrLine
}

// oneOrMany decodes a JSON array — or a lone object/value, which is what
// PowerShell's ConvertTo-Json emits for a one-element collection in some
// shapes — into dst (a pointer to a slice). null/absent leaves it empty.
func oneOrMany[T any](raw json.RawMessage, dst *[]T) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if raw[0] == '[' {
		return json.Unmarshal(raw, dst)
	}
	var one T
	if err := json.Unmarshal(raw, &one); err != nil {
		return err
	}
	*dst = append(*dst, one)
	return nil
}

// parseOCROutput reads the script's stdout (the last line that starts with
// '{' — anything PowerShell printed before it is ignored) and converts image
// coordinates to screen coordinates: screen = origin + image/scale, where
// scale is how much the image was enlarged before OCR (1 = untouched).
func parseOCROutput(stdout []byte, originX, originY int, scale float64) (ocrResult, error) {
	var line []byte
	for _, l := range bytes.Split(stdout, []byte("\n")) {
		l = bytes.TrimSpace(l)
		if len(l) > 0 && l[0] == '{' {
			line = l
		}
	}
	if line == nil {
		out := strings.TrimSpace(string(stdout))
		if len(out) > 300 {
			out = out[:300] + "…"
		}
		return ocrResult{}, fmt.Errorf("OCR script printed no result (output: %q)", out)
	}
	var raw ocrOutputJSON
	if err := json.Unmarshal(line, &raw); err != nil {
		return ocrResult{}, fmt.Errorf("OCR script output is not valid JSON: %w", err)
	}
	if scale <= 0 {
		scale = 1
	}
	res := ocrResult{Error: raw.Error, Lang: raw.Lang}
	if err := oneOrMany(raw.Available, &res.Available); err != nil {
		return ocrResult{}, fmt.Errorf("OCR output: available: %w", err)
	}
	var lines []ocrLineJSON
	if err := oneOrMany(raw.Lines, &lines); err != nil {
		return ocrResult{}, fmt.Errorf("OCR output: lines: %w", err)
	}
	conv := func(v float64, origin int) int { return origin + int(math.Round(v/scale)) }
	for _, lj := range lines {
		var words []ocrWordJSON
		if err := oneOrMany(lj.W, &words); err != nil {
			return ocrResult{}, fmt.Errorf("OCR output: words: %w", err)
		}
		ol := ocrLine{Text: strings.TrimSpace(lj.T)}
		for _, w := range words {
			r := markRect{conv(w.X, originX), conv(w.Y, originY), conv(w.X+w.W, originX), conv(w.Y+w.H, originY)}
			ol.Words = append(ol.Words, ocrWord{Text: w.T, Rect: r})
			ol.Rect = ol.Rect.union(r)
		}
		if ol.Text == "" && len(ol.Words) == 0 {
			continue
		}
		res.Lines = append(res.Lines, ol)
	}
	// The engine's own order is per text region; put lines in reading order so
	// a two-column layout reads row by row like everything else here.
	rects := make([]markRect, len(res.Lines))
	for i, l := range res.Lines {
		rects[i] = l.Rect
	}
	ordered := make([]ocrLine, 0, len(res.Lines))
	for _, i := range readingOrder(rects) {
		ordered = append(ordered, res.Lines[i])
	}
	res.Lines = ordered
	return res, nil
}

// ocrUpscale is how much to enlarge a w×h capture before OCR. The engine reads
// small UI text (9pt at 100% scaling) noticeably better at 2×; the long edge is
// kept under ocrMaxEdge so the run stays fast.
const ocrMaxEdge = 4096

func ocrUpscale(w, h int) int {
	if max(w, h)*2 <= ocrMaxEdge {
		return 2
	}
	return 1
}

// upscaleBilinear enlarges src by an integer factor f with bilinear filtering
// (f <= 1 returns src). Smooth edges matter: OCR on pixel-doubled text is
// worse than on interpolated text.
func upscaleBilinear(src image.Image, f int) image.Image {
	if f <= 1 {
		return src
	}
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	rgba, ok := src.(*image.RGBA)
	if !ok {
		rgba = image.NewRGBA(image.Rect(0, 0, sw, sh))
		for y := 0; y < sh; y++ {
			for x := 0; x < sw; x++ {
				rgba.Set(x, y, src.At(sb.Min.X+x, sb.Min.Y+y))
			}
		}
		sb = rgba.Bounds()
	}
	dw, dh := sw*f, sh*f
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	at := func(x, y int) color.RGBA {
		x = min(max(x, 0), sw-1)
		y = min(max(y, 0), sh-1)
		return rgba.RGBAAt(sb.Min.X+x, sb.Min.Y+y)
	}
	for dy := 0; dy < dh; dy++ {
		fy := (float64(dy)+0.5)/float64(f) - 0.5
		y0 := int(math.Floor(fy))
		ty := fy - float64(y0)
		for dx := 0; dx < dw; dx++ {
			fx := (float64(dx)+0.5)/float64(f) - 0.5
			x0 := int(math.Floor(fx))
			tx := fx - float64(x0)
			c00, c10, c01, c11 := at(x0, y0), at(x0+1, y0), at(x0, y0+1), at(x0+1, y0+1)
			mix := func(a, b, c, d uint8) uint8 {
				top := float64(a)*(1-tx) + float64(b)*tx
				bot := float64(c)*(1-tx) + float64(d)*tx
				return uint8(math.Round(top*(1-ty) + bot*ty))
			}
			dst.SetRGBA(dx, dy, color.RGBA{
				mix(c00.R, c10.R, c01.R, c11.R),
				mix(c00.G, c10.G, c01.G, c11.G),
				mix(c00.B, c10.B, c01.B, c11.B),
				0xFF,
			})
		}
	}
	return dst
}

// ocrMaxReplyChars bounds the ocr reply, like the other read tools.
const ocrMaxReplyChars = 8000

// ocrText renders lines as "text — center(x,y)", capped at maxChars.
func ocrText(label, lang string, lines []ocrLine, maxChars int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "OCR (%s) of %s: %d line(s)\n", lang, label, len(lines))
	shown := 0
	for _, l := range lines {
		s := fmt.Sprintf("%s — center(%d,%d)\n", onelinePreview(l.Text, 200), l.Rect.CX(), l.Rect.CY())
		if shown > 0 && b.Len()+len(s) > maxChars {
			break
		}
		b.WriteString(s)
		shown++
	}
	if shown < len(lines) {
		fmt.Fprintf(&b, "…[truncated: %d of %d lines shown — ocr a smaller region (x,y,width,height) for the rest]\n", shown, len(lines))
	}
	if len(lines) > 0 {
		b.WriteString("Coordinates are screen pixels: click(x,y), or click_text(text) to find and click in one call.")
	} else {
		b.WriteString("(no text recognized)")
	}
	return strings.TrimRight(b.String(), "\n")
}

// normalizeOCRText lower-cases s and drops all whitespace, so matching ignores
// the spacing OCR invents or loses ("로그 인" vs "로그인").
func normalizeOCRText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsSpace(r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// ocrMatch is one place a click_text needle was found.
type ocrMatch struct {
	Text  string   // the OCR words the match spans, joined
	Line  string   // the whole line it is on
	Rect  markRect // union of the spanned words (screen)
	Exact bool     // the match starts and ends on word boundaries
}

// findOCRText finds needle (case- and space-insensitive substring) in lines,
// in reading order. Each occurrence maps to the words it spans, so clicking
// lands on the matched words rather than the middle of a long line. Matches
// that cover whole words come first, then partial-word ones.
func findOCRText(lines []ocrLine, needle string) []ocrMatch {
	n := normalizeOCRText(needle)
	if n == "" {
		return nil
	}
	var exact, partial []ocrMatch
	for _, l := range lines {
		words := l.Words
		if len(words) == 0 {
			words = []ocrWord{{Text: l.Text, Rect: l.Rect}}
		}
		// Concatenate normalized words, remembering where each starts.
		var cat strings.Builder
		starts := make([]int, len(words))
		ends := make([]int, len(words))
		for i, w := range words {
			starts[i] = cat.Len()
			cat.WriteString(normalizeOCRText(w.Text))
			ends[i] = cat.Len()
		}
		hay := cat.String()
		for from := 0; from <= len(hay)-len(n); {
			k := strings.Index(hay[from:], n)
			if k < 0 {
				break
			}
			s, e := from+k, from+k+len(n)
			m := ocrMatch{Line: l.Text}
			var parts []string
			startOK, endOK := false, false
			for i := range words {
				if ends[i] <= s || starts[i] >= e {
					continue
				}
				m.Rect = m.Rect.union(words[i].Rect)
				parts = append(parts, words[i].Text)
				if starts[i] == s {
					startOK = true
				}
				if ends[i] == e {
					endOK = true
				}
			}
			m.Text = strings.Join(parts, " ")
			m.Exact = startOK && endOK
			if m.Exact {
				exact = append(exact, m)
			} else {
				partial = append(partial, m)
			}
			from = s + 1
			for from < len(hay) && !utf8Start(hay[from]) {
				from++
			}
		}
	}
	return append(exact, partial...)
}

// utf8Start reports whether b begins a UTF-8 sequence (is not a continuation byte).
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// ocrLangHint explains how to get a missing OCR language.
func ocrLangHint(want string, available []string) string {
	have := strings.Join(available, ", ")
	if have == "" {
		have = "none"
	}
	tag := want
	if strings.HasPrefix(strings.ToLower(want), "ko") || want == "" {
		tag = "ko-KR"
	}
	return fmt.Sprintf("Windows OCR language %q is not installed (installed: %s). Add it in Settings > Time & language > Language & region > add/open the language > Language options > install the optical character recognition (OCR) feature, "+
		"or in an elevated PowerShell: Add-WindowsCapability -Online -Name \"Language.OCR~~~%s~0.0.1.0\". Or pass lang= one of the installed ones.", want, have, tag)
}
