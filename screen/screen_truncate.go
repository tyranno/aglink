package main

import "fmt"

// Output caps for the read tools (get_text / snapshot / win_controls).
//
// Every tool result stays in the conversation and is re-sent on each later API
// round-trip of the turn, so one oversized read (a whole document, a giant UIA
// tree) is paid for many times over. These defaults keep a single result small
// and give the model an explicit way to continue (offset=…) instead of having
// to dump everything at once. Mirrors web/'s get_page_text (offset/maxChars,
// negative offset = tail).
const (
	// defaultTextMaxChars is get_text's default window size (runes).
	defaultTextMaxChars = 8000
	// ceilingTextMaxChars is the largest max_chars get_text honors.
	ceilingTextMaxChars = 50000

	// defaultSnapshotMaxElements / defaultSnapshotMaxChars bound one snapshot page.
	defaultSnapshotMaxElements = 150
	ceilingSnapshotMaxElements = 1000
	defaultSnapshotMaxChars    = 10000
	ceilingSnapshotMaxChars    = 50000

	// defaultWinControlsMax bounds one win_controls page.
	defaultWinControlsMax = 200
	// winControlLabelChars caps one control's label in win_controls output — an
	// Edit control's "label" (GetWindowText) is its entire content.
	winControlLabelChars = 80
)

// clampLimit maps a caller-supplied limit to a usable one: <=0 means "use the
// default", anything above ceiling is lowered to ceiling.
func clampLimit(v, def, ceiling int) int {
	if v <= 0 {
		return def
	}
	if v > ceiling {
		return ceiling
	}
	return v
}

// runeWindow returns the slice of s selected by offset/maxChars (both in runes,
// so it never splits a multi-byte character) plus a truncation note to append
// ("" when the whole text is returned).
//
//   - offset >= 0: start at that rune and return up to maxChars runes.
//   - offset < 0:  tail — return the last min(-offset, maxChars) runes.
//
// totalKnown=false means s itself was cut at a fetch ceiling, so the real total
// may be larger; the note then says "N자 이상".
func runeWindow(s string, offset, maxChars int, totalKnown bool) (string, string) {
	if maxChars <= 0 {
		maxChars = defaultTextMaxChars
	}
	r := []rune(s)
	total := len(r)
	var start, end int
	if offset < 0 {
		n := -offset
		if n > maxChars {
			n = maxChars
		}
		if n > total {
			n = total
		}
		start, end = total-n, total
	} else {
		start = offset
		if start > total {
			start = total
		}
		end = start + maxChars
		if end > total {
			end = total
		}
	}
	totalStr := fmt.Sprintf("%d자", total)
	totalEn := fmt.Sprintf("%d chars", total)
	if !totalKnown {
		totalStr = fmt.Sprintf("%d자 이상", total)
		totalEn = fmt.Sprintf("%d+ chars", total)
	}
	body := string(r[start:end])
	switch {
	case offset > 0 && offset >= total && total > 0:
		return "", fmt.Sprintf("[offset=%d 이 전체 %s 이상 — 읽을 내용 없음 / offset is past the end (%s)]", offset, totalStr, totalEn)
	case start == 0 && end == total && totalKnown:
		return body, ""
	case end < total || !totalKnown:
		return body, fmt.Sprintf("\n…[잘림: 전체 %s 중 %d–%d 표시. 이어 읽으려면 offset=%d / truncated: showing %d–%d of %s; continue with offset=%d]",
			totalStr, start, end, end, start, end, totalEn, end)
	default: // start > 0, reached the end
		return body, fmt.Sprintf("\n…[전체 %s 중 %d–%d 표시(끝까지). 앞부분은 offset=0 부터 / showing %d–%d of %s (to the end); earlier text from offset=0]",
			totalStr, start, end, start, end, totalEn)
	}
}

// pageNote is the continuation note for an element-list page (snapshot,
// win_controls): items [start,next) of total were covered; "" when the page
// reached the end and started at 0.
func pageNote(unit string, start, next, total int, reason string) string {
	if next < total {
		why := ""
		if reason != "" {
			why = " (" + reason + ")"
		}
		return fmt.Sprintf("…[잘림%s: 전체 %d개 %s 중 %d–%d 표시. 이어 보려면 offset=%d / truncated: %s %d–%d of %d; continue with offset=%d]",
			why, total, unit, start, next, next, unit, start, next, total, next)
	}
	if start > 0 {
		return fmt.Sprintf("…[전체 %d개 %s 중 %d–%d 표시(끝까지) / %s %d–%d of %d (to the end)]", total, unit, start, next, unit, start, next, total)
	}
	return ""
}

// maxFieldValueChars bounds a single UIA field value returned by get_value. A
// single control — a text editor, a document, a huge read-only box — could
// otherwise return its entire contents (tens of thousands of tokens) in one
// read. The value is trimmed with a note pointing at get_text, which can page
// through the rest with offset.
const maxFieldValueChars = 2000

// clampFieldValue trims a get_value result to maxFieldValueChars runes and
// returns the (possibly cut) value plus a note to append outside the quoted
// value ("" when nothing was cut). totalKnown=false means s was already cut at
// fetch time, so the real length is at least len(s).
func clampFieldValue(s string, totalKnown bool) (string, string) {
	r := []rune(s)
	if len(r) <= maxFieldValueChars && totalKnown {
		return s, ""
	}
	if len(r) > maxFieldValueChars {
		r = r[:maxFieldValueChars]
	}
	total := fmt.Sprintf("%d자", len([]rune(s)))
	totalEn := fmt.Sprintf("%d chars", len([]rune(s)))
	if !totalKnown {
		total += " 이상"
		totalEn = fmt.Sprintf("%d+ chars", len([]rune(s)))
	}
	n := len(r)
	return string(r), fmt.Sprintf("\n…[잘림: 전체 %s 중 0–%d 표시. 나머지는 get_text(name, offset=%d) / truncated: showing 0–%d of %s; read the rest with get_text offset=%d]",
		total, n, n, n, totalEn, n)
}

// pageBounds converts an offset/limit into [start,end) indices clamped to
// [0,total]. A negative offset is treated as 0.
func pageBounds(offset, limit, total int) (int, int) {
	start := offset
	if start < 0 {
		start = 0
	}
	if start > total {
		start = total
	}
	end := start + limit
	if limit <= 0 || end > total {
		end = total
	}
	return start, end
}
