//go:build windows

package main

import (
	"fmt"
	"image"
	"image/color"
	"sort"
	"strings"
	"sync"
	"time"
)

// Set-of-Mark ("show numbers", like Windows Voice Access): show_numbers puts a
// numbered badge on every actionable element of a window and remembers which
// number is which; click_number acts on one by number. This file is the pure
// part — reading order, de-duplication, the per-window number store and its
// staleness rules — so it is unit-testable without UIA or a screen.

// markRect is a screen rectangle in physical pixels (right/bottom exclusive).
type markRect struct{ L, T, R, B int }

func (r markRect) W() int      { return r.R - r.L }
func (r markRect) H() int      { return r.B - r.T }
func (r markRect) CX() int     { return (r.L + r.R) / 2 }
func (r markRect) CY() int     { return (r.T + r.B) / 2 }
func (r markRect) Empty() bool { return r.R <= r.L || r.B <= r.T }

// contains reports whether point (x,y) lies inside r.
func (r markRect) contains(x, y int) bool { return x >= r.L && x < r.R && y >= r.T && y < r.B }

// union returns the smallest rectangle covering r and o (an empty r yields o).
func (r markRect) union(o markRect) markRect {
	if r.Empty() {
		return o
	}
	if o.Empty() {
		return r
	}
	return markRect{min(r.L, o.L), min(r.T, o.T), max(r.R, o.R), max(r.B, o.B)}
}

// markItem is one numbered element. N is 1-based, assigned by numberMarks.
type markItem struct {
	N    int
	Type string // control type label, e.g. "button"
	Name string
	Rect markRect
	// Invokable: the element has a UIA pattern click_number can drive in the
	// background (Invoke/SelectionItem/Toggle/ExpandCollapse).
	Invokable bool
	// Index into the platform-side element slice (held COM pointers), -1 = none.
	Elem int
}

// readingOrder returns the indices of rects in reading order: top-to-bottom
// rows, left-to-right within a row. Two rects share a row when the later one's
// vertical centre lies within the row's first rect (so a slightly taller or
// lower neighbour on the same line does not start a new row). Sorting is
// deterministic for identical inputs.
func readingOrder(rects []markRect) []int {
	idx := make([]int, len(rects))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		ra, rb := rects[idx[a]], rects[idx[b]]
		if ra.T != rb.T {
			return ra.T < rb.T
		}
		return ra.L < rb.L
	})
	var out []int
	for i := 0; i < len(idx); {
		head := rects[idx[i]]
		j := i + 1
		for j < len(idx) {
			cy := rects[idx[j]].CY()
			if cy < head.T || cy >= head.B {
				break
			}
			j++
		}
		row := append([]int(nil), idx[i:j]...)
		sort.SliceStable(row, func(a, b int) bool {
			ra, rb := rects[row[a]], rects[row[b]]
			if ra.L != rb.L {
				return ra.L < rb.L
			}
			return ra.T < rb.T
		})
		out = append(out, row...)
		i = j
	}
	return out
}

// nearSameRect reports whether two rects are the same box give or take a few
// pixels — e.g. a list item and the invokable text inside it, which would
// otherwise get two badges on the same spot.
func nearSameRect(a, b markRect) bool {
	const tol = 3
	d := func(x, y int) bool { return x-y <= tol && y-x <= tol }
	return d(a.L, b.L) && d(a.T, b.T) && d(a.R, b.R) && d(a.B, b.B)
}

// numberMarks filters candidates to those visible inside clip (centre inside,
// non-empty), drops near-duplicates (keeping the earlier, which callers feed
// in tree order so the outer/actionable element wins), orders the rest in
// reading order and numbers them 1..N, capped at maxN. truncated reports how
// many were dropped by the cap.
func numberMarks(cands []markItem, clip markRect, maxN int) (items []markItem, truncated int) {
	var kept []markItem
	for _, c := range cands {
		if c.Rect.Empty() || !clip.contains(c.Rect.CX(), c.Rect.CY()) {
			continue
		}
		dup := false
		for _, k := range kept {
			if nearSameRect(k.Rect, c.Rect) {
				dup = true
				break
			}
		}
		if !dup {
			kept = append(kept, c)
		}
	}
	rects := make([]markRect, len(kept))
	for i, k := range kept {
		rects[i] = k.Rect
	}
	for _, i := range readingOrder(rects) {
		items = append(items, kept[i])
	}
	if maxN > 0 && len(items) > maxN {
		truncated = len(items) - maxN
		items = items[:maxN]
	}
	for i := range items {
		items[i].N = i + 1
	}
	return items, truncated
}

// markListText renders the show_numbers reply: one "N. [type] name — center(x,y)" line each.
func markListText(label string, items []markItem, truncated int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d numbered element(s) in %s:\n", len(items), label)
	for _, it := range items {
		name := onelinePreview(it.Name, 60)
		if name == "" {
			name = "(unnamed)"
		}
		fmt.Fprintf(&b, "%d. [%s] %s — center(%d,%d)\n", it.N, it.Type, name, it.Rect.CX(), it.Rect.CY())
	}
	if truncated > 0 {
		fmt.Fprintf(&b, "…[truncated: %d more element(s) not numbered — the cap is %d; use snapshot/win_controls, or show_numbers on a smaller window]\n", truncated, len(items))
	}
	b.WriteString("Act on one with click_number(n). Numbers stay valid until the window moves/resizes or show_numbers runs again.")
	return b.String()
}

// markTTL is how long a show_numbers result may be used by click_number. The UI
// can change without the window moving (a page loads, a list scrolls), so an
// old numbering is refused rather than clicked blindly.
var markTTL = 5 * time.Minute

// markSet is one show_numbers result for one top-level window.
type markSet struct {
	HWND    uintptr
	Title   string
	WinRect markRect
	Created time.Time
	Items   []markItem
}

// markStore keeps the latest markSet per window plus which one was latest
// overall (click_number without a window uses that).
type markStore struct {
	mu     sync.Mutex
	sets   map[uintptr]*markSet
	latest uintptr
}

func newMarkStore() *markStore { return &markStore{sets: map[uintptr]*markSet{}} }

// put replaces the window's set and returns the one it replaced (nil if none),
// so the caller can release platform resources tied to it.
func (s *markStore) put(ms *markSet) *markSet {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.sets[ms.HWND]
	s.sets[ms.HWND] = ms
	s.latest = ms.HWND
	return old
}

// errMarksStale is wrapped by every "call show_numbers again" refusal.
var errMarksStale = fmt.Errorf("numbering is stale")

// lookup returns item n of the set for hwnd (0 = the latest show_numbers
// result). alive/curRect describe the window now; a window that closed, moved
// or resized — or a set older than markTTL — is refused with a message telling
// the caller to run show_numbers again.
func (s *markStore) lookup(hwnd uintptr, n int, now time.Time, alive func(uintptr) (markRect, bool)) (*markSet, markItem, error) {
	s.mu.Lock()
	if hwnd == 0 {
		hwnd = s.latest
	}
	ms := s.sets[hwnd]
	s.mu.Unlock()
	if ms == nil {
		if hwnd == 0 {
			return nil, markItem{}, fmt.Errorf("no show_numbers result yet — call show_numbers first")
		}
		return nil, markItem{}, fmt.Errorf("no show_numbers result for this window — call show_numbers(window=...) first")
	}
	if age := now.Sub(ms.Created); age > markTTL {
		return nil, markItem{}, fmt.Errorf("%w: show_numbers for %q ran %s ago (limit %s) — call show_numbers again", errMarksStale, ms.Title, age.Round(time.Second), markTTL)
	}
	cur, ok := alive(ms.HWND)
	if !ok {
		return nil, markItem{}, fmt.Errorf("%w: window %q is gone (closed or minimized) — call show_numbers again", errMarksStale, ms.Title)
	}
	if cur != ms.WinRect {
		return nil, markItem{}, fmt.Errorf("%w: window %q moved or resized since show_numbers — call show_numbers again", errMarksStale, ms.Title)
	}
	if n < 1 || n > len(ms.Items) {
		return nil, markItem{}, fmt.Errorf("number %d is out of range: show_numbers for %q numbered 1..%d", n, ms.Title, len(ms.Items))
	}
	return ms, ms.Items[n-1], nil
}

// elementMoved reports whether an element now sits elsewhere than when it was
// numbered (the content scrolled or re-laid out), beyond a small tolerance.
func elementMoved(was, now markRect) bool {
	const tol = 4
	dx, dy := was.CX()-now.CX(), was.CY()-now.CY()
	return dx > tol || dx < -tol || dy > tol || dy < -tol
}

// badgeRect places a w×h badge at the element's top-left, pulled inside clip
// so a badge for an element at the window edge stays on screen.
func badgeRect(el markRect, w, h int, clip markRect) markRect {
	x, y := el.L, el.T
	if x+w > clip.R {
		x = clip.R - w
	}
	if y+h > clip.B {
		y = clip.B - h
	}
	if x < clip.L {
		x = clip.L
	}
	if y < clip.T {
		y = clip.T
	}
	return markRect{x, y, x + w, y + h}
}

// markActionable decides whether an element earns a number. Elements with a
// pattern click_number can drive (invokable) or an editable Value count, as do
// the inherently clickable control types even when their provider exposes no
// pattern (common in owner-drawn and Chromium UIs). Containers never do — a
// pane or list that happens to be invokable would just cover its children.
func markActionable(ctrlType int32, invokable, editable bool) bool {
	switch ctrlType {
	case 50032, 50033, 50026, 50008, 50023, 50018, 50036, 50028, 50021, 50010,
		50009, 50014, 50027, 50037, 50038, 50017, 50022, 50012, 50039, 50040:
		// window, pane, group, list, tree, tab, table, data grid, tool bar,
		// menu bar, menu, scroll bar, thumb, title bar, separator, status bar,
		// tooltip, progress bar, semantic zoom, app bar
		return false
	case 50000, 50002, 50003, 50004, 50005, 50007, 50011, 50013, 50019,
		50024, 50031, 50015, 50029, 50016, 50035:
		// button, checkbox, combobox, edit, hyperlink, list item, menu item,
		// radio button, tab item, tree item, split button, slider, data item,
		// spinner, header item
		return true
	}
	return invokable || editable
}

// ---- numbered badges drawn into a capture (for vision models) ----

// markDigits is a 5×7 bitmap font for 0–9 ('#' = ink). Only digits are ever
// drawn, so this beats pulling in a font rasterizer.
var markDigits = [10][7]string{
	{".###.", "#...#", "#..##", "#.#.#", "##..#", "#...#", ".###."},
	{"..#..", ".##..", "..#..", "..#..", "..#..", "..#..", ".###."},
	{".###.", "#...#", "....#", "...#.", "..#..", ".#...", "#####"},
	{"#####", "...#.", "..#..", "...#.", "....#", "#...#", ".###."},
	{"...#.", "..##.", ".#.#.", "#..#.", "#####", "...#.", "...#."},
	{"#####", "#....", "####.", "....#", "....#", "#...#", ".###."},
	{"..##.", ".#...", "#....", "####.", "#...#", "#...#", ".###."},
	{"#####", "....#", "...#.", "..#..", ".#...", ".#...", ".#..."},
	{".###.", "#...#", "#...#", ".###.", "#...#", "#...#", ".###."},
	{".###.", "#...#", "#...#", ".####", "....#", "...#.", ".##.."},
}

// Badge colours: dark purple fill and the agent cursor's violet as a border.
var (
	markFill   = color.RGBA{0x3A, 0x1F, 0x7A, 0xFF}
	markBorder = color.RGBA{0x7A, 0x48, 0xD6, 0xFF}
	markInk    = color.RGBA{0xFF, 0xFF, 0xFF, 0xFF}
)

// markPixelScale picks the size of one font pixel so badges stay legible after
// the image is downscaled by factor f (<1) for the vision cap: about 2 output
// pixels per font pixel.
func markPixelScale(f float64) int {
	if f <= 0 || f >= 1 {
		return 2
	}
	s := int(2/f + 0.999)
	if s < 2 {
		s = 2
	}
	return s
}

// imageBadgeSize is the w×h of a badge for number n at pixel scale s.
func imageBadgeSize(n, s int) (int, int) {
	digits := len(fmt.Sprint(n))
	return (digits*6 + 3) * s, 11 * s
}

// drawMarkBadges draws each item's number at its top-left into img, whose
// pixel (0,0) is screen point (originX, originY).
func drawMarkBadges(img *image.RGBA, originX, originY int, items []markItem, s int) {
	b := img.Bounds()
	clip := markRect{0, 0, b.Dx(), b.Dy()}
	for _, it := range items {
		w, h := imageBadgeSize(it.N, s)
		el := markRect{it.Rect.L - originX, it.Rect.T - originY, it.Rect.R - originX, it.Rect.B - originY}
		br := badgeRect(el, w, h, clip)
		fill := func(x0, y0, x1, y1 int, c color.RGBA) {
			for y := max(y0, 0); y < min(y1, clip.B); y++ {
				for x := max(x0, 0); x < min(x1, clip.R); x++ {
					img.SetRGBA(b.Min.X+x, b.Min.Y+y, c)
				}
			}
		}
		fill(br.L, br.T, br.R, br.B, markBorder)
		fill(br.L+s/2+1, br.T+s/2+1, br.R-s/2-1, br.B-s/2-1, markFill)
		x := br.L + 2*s
		for _, ch := range fmt.Sprint(it.N) {
			g := markDigits[ch-'0']
			for gy, row := range g {
				for gx, px := range row {
					if px == '#' {
						fill(x+gx*s, br.T+2*s+gy*s, x+(gx+1)*s, br.T+2*s+(gy+1)*s, markInk)
					}
				}
			}
			x += 6 * s
		}
	}
}
