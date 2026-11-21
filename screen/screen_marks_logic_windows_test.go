//go:build windows

package main

import (
	"errors"
	"image"
	"strings"
	"testing"
	"time"
)

func TestReadingOrderRowsThenColumns(t *testing.T) {
	// Two rows; within the first row the right item is listed first and sits a
	// few pixels higher — it must still read left-to-right on the same row.
	rects := []markRect{
		{200, 8, 260, 30},    // row 1, right
		{10, 10, 80, 30},     // row 1, left
		{10, 100, 80, 120},   // row 2, left
		{120, 102, 160, 118}, // row 2, right
		{90, 12, 150, 28},    // row 1, middle
	}
	got := readingOrder(rects)
	want := []int{1, 4, 0, 2, 3}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("readingOrder = %v, want %v", got, want)
		}
	}
}

func TestNumberMarksFiltersDedupesAndCaps(t *testing.T) {
	clip := markRect{0, 0, 500, 400}
	cands := []markItem{
		{Name: "outside", Rect: markRect{600, 10, 640, 30}},
		{Name: "empty", Rect: markRect{10, 10, 10, 30}},
		{Name: "B", Rect: markRect{100, 10, 150, 30}},
		{Name: "A", Rect: markRect{10, 10, 60, 30}},
		{Name: "A-inner-dup", Rect: markRect{11, 11, 59, 29}},
		{Name: "C", Rect: markRect{10, 200, 60, 220}},
	}
	items, trunc := numberMarks(cands, clip, 0)
	if trunc != 0 {
		t.Fatalf("truncated = %d, want 0", trunc)
	}
	var names []string
	for i, it := range items {
		if it.N != i+1 {
			t.Errorf("item %d has N=%d", i, it.N)
		}
		names = append(names, it.Name)
	}
	if strings.Join(names, ",") != "A,B,C" {
		t.Fatalf("numbered %v, want A,B,C", names)
	}
	items, trunc = numberMarks(cands, clip, 2)
	if len(items) != 2 || trunc != 1 || items[1].Name != "B" {
		t.Fatalf("cap 2: got %d items, trunc %d (%v)", len(items), trunc, items)
	}
}

func TestMarkListTextFormat(t *testing.T) {
	items := []markItem{{N: 1, Type: "button", Name: "저장", Rect: markRect{10, 10, 30, 30}}, {N: 2, Type: "edit", Rect: markRect{0, 40, 100, 60}}}
	s := markListText("window \"x\"", items, 3)
	for _, want := range []string{"1. [button] 저장 — center(20,20)", "2. [edit] (unnamed) — center(50,50)", "truncated: 3 more"} {
		if !strings.Contains(s, want) {
			t.Errorf("list text missing %q:\n%s", want, s)
		}
	}
}

func TestMarkStoreLookupStaleness(t *testing.T) {
	st := newMarkStore()
	now := time.Now()
	win := markRect{0, 0, 800, 600}
	alive := func(h uintptr) (markRect, bool) { return win, true }

	if _, _, err := st.lookup(0, 1, now, alive); err == nil || !strings.Contains(err.Error(), "show_numbers first") {
		t.Fatalf("empty store: err = %v", err)
	}
	st.put(&markSet{HWND: 0x10, Title: "A", WinRect: win, Created: now, Items: []markItem{{N: 1, Name: "ok"}}})

	if _, it, err := st.lookup(0, 1, now, alive); err != nil || it.Name != "ok" {
		t.Fatalf("latest lookup: %v %v", it, err)
	}
	if _, it, err := st.lookup(0x10, 1, now, alive); err != nil || it.Name != "ok" {
		t.Fatalf("by window: %v %v", it, err)
	}
	if _, _, err := st.lookup(0x99, 1, now, alive); err == nil {
		t.Fatal("unknown window should fail")
	}
	if _, _, err := st.lookup(0, 2, now, alive); err == nil || !strings.Contains(err.Error(), "1..1") {
		t.Fatalf("out of range: %v", err)
	}
	if _, _, err := st.lookup(0, 1, now.Add(markTTL+time.Second), alive); !errors.Is(err, errMarksStale) {
		t.Fatalf("expired: %v", err)
	}
	moved := func(h uintptr) (markRect, bool) { return markRect{5, 0, 805, 600}, true }
	if _, _, err := st.lookup(0, 1, now, moved); !errors.Is(err, errMarksStale) || !strings.Contains(err.Error(), "moved") {
		t.Fatalf("moved: %v", err)
	}
	gone := func(h uintptr) (markRect, bool) { return markRect{}, false }
	if _, _, err := st.lookup(0, 1, now, gone); !errors.Is(err, errMarksStale) {
		t.Fatalf("gone: %v", err)
	}
	// A newer set for another window becomes the default target.
	old := st.put(&markSet{HWND: 0x20, Title: "B", WinRect: win, Created: now, Items: []markItem{{N: 1, Name: "b"}}})
	if old != nil {
		t.Fatal("put for a new window should not return a replaced set")
	}
	if _, it, _ := st.lookup(0, 1, now, alive); it.Name != "b" {
		t.Fatalf("latest should be window B, got %v", it)
	}
	if old := st.put(&markSet{HWND: 0x20, Created: now}); old == nil || old.Title != "B" {
		t.Fatal("replacing a set should return the old one")
	}
}

func TestElementMovedTolerance(t *testing.T) {
	a := markRect{100, 100, 140, 120}
	if elementMoved(a, markRect{102, 101, 142, 121}) {
		t.Error("a 2px shift should not count as moved")
	}
	if !elementMoved(a, markRect{100, 140, 140, 160}) {
		t.Error("a 40px scroll should count as moved")
	}
}

func TestBadgeRectClampsInsideClip(t *testing.T) {
	clip := markRect{0, 0, 100, 100}
	if r := badgeRect(markRect{10, 10, 50, 30}, 20, 12, clip); r != (markRect{10, 10, 30, 22}) {
		t.Errorf("plain: %v", r)
	}
	if r := badgeRect(markRect{95, 95, 99, 99}, 20, 12, clip); r != (markRect{80, 88, 100, 100}) {
		t.Errorf("bottom-right edge: %v", r)
	}
	if r := badgeRect(markRect{-30, -5, 10, 10}, 20, 12, clip); r.L != 0 || r.T != 0 {
		t.Errorf("negative: %v", r)
	}
}

func TestMarkActionable(t *testing.T) {
	cases := []struct {
		ct       int32
		inv, ed  bool
		want     bool
		describe string
	}{
		{50000, false, false, true, "button without patterns"},
		{50033, true, true, false, "invokable pane is a container"},
		{50020, true, false, true, "invokable text"},
		{50020, false, false, false, "plain text"},
		{50025, false, true, true, "editable custom"},
		{50007, false, false, true, "list item"},
	}
	for _, c := range cases {
		if got := markActionable(c.ct, c.inv, c.ed); got != c.want {
			t.Errorf("%s: got %v want %v", c.describe, got, c.want)
		}
	}
}

func TestDrawMarkBadgesInksDigits(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 200, 100))
	items := []markItem{{N: 12, Rect: markRect{1010, 2010, 1060, 2030}}}
	drawMarkBadges(img, 1000, 2000, items, 2)
	w, h := imageBadgeSize(12, 2)
	if w != (2*6+3)*2 || h != 22 {
		t.Fatalf("badge size %dx%d", w, h)
	}
	ink, fill := 0, 0
	for y := 10; y < 10+h; y++ {
		for x := 10; x < 10+w; x++ {
			switch img.RGBAAt(x, y) {
			case markInk:
				ink++
			case markFill:
				fill++
			}
		}
	}
	if ink == 0 || fill == 0 {
		t.Fatalf("badge not drawn: ink=%d fill=%d", ink, fill)
	}
	if img.RGBAAt(5, 5) != (img.RGBAAt(150, 90)) {
		t.Error("pixels outside the badge should be untouched")
	}
	if s := markPixelScale(0.5); s != 4 {
		t.Errorf("markPixelScale(0.5) = %d, want 4", s)
	}
	if s := markPixelScale(1); s != 2 {
		t.Errorf("markPixelScale(1) = %d, want 2", s)
	}
}
