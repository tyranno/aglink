package main

import (
	"fmt"
	"strings"
	"testing"
)

const snapHeader = "UIA elements of window \"App\" (3 shown; scanned 0–3 of 3; document order, indented by containment; @x,y,w×h = screen px):"

func TestSnapLineKey(t *testing.T) {
	cases := []struct{ a, b string }{
		{`  - CheckBox | "Wrap" | id=wrap [invokable] [unchecked] @1,2,3×4`, `  - CheckBox | "Wrap" | id=wrap [invokable] [checked] @1,2,3×4`},
		{`- Edit | "Name" [editable] @1,2,3×4 = "a"`, `- Edit | "Name" [editable] @5,6,3×4 = "ab"`},
		{`- Button | "say \"hi\" [x]" @1,1,1×1`, `- Button | "say \"hi\" [x]" [disabled] @1,1,1×1`},
	}
	for _, c := range cases {
		if ka, kb := snapLineKey(c.a), snapLineKey(c.b); ka != kb {
			t.Errorf("same element got different keys:\n%q\n%q", ka, kb)
		}
	}
	diff := [][2]string{
		{`- Button | "OK" @1,1,1×1`, `- Button | "Cancel" @1,1,1×1`},
		{`- Button | "OK" | id=a`, `- Button | "OK" | id=b`},
		{`- Button | "OK"`, `  - Button | "OK"`}, // indentation is identity too
	}
	for _, c := range diff {
		if snapLineKey(c[0]) == snapLineKey(c[1]) {
			t.Errorf("different elements share a key: %q / %q", c[0], c[1])
		}
	}
}

func TestDiffSnapLines(t *testing.T) {
	prev := []string{
		`- Button | "A"`,
		`- CheckBox | "Wrap" [unchecked]`,
		`- Button | "Gone"`,
		`- Button | "Z"`,
	}
	cur := []string{
		`- Button | "A"`,
		`- CheckBox | "Wrap" [checked]`,
		`- Button | "Z"`,
		`- Text | "New"`,
	}
	ops := diffSnapLines(prev, cur)
	var got []string
	for _, op := range ops {
		got = append(got, fmt.Sprintf("%c%s", op.kind, op.line))
	}
	want := []string{`~- CheckBox | "Wrap" [checked]`, `-- Button | "Gone"`, `+- Text | "New"`}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ops:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if ops[0].old != `- CheckBox | "Wrap" [unchecked]` {
		t.Errorf("changed op lost its old line: %q", ops[0].old)
	}
	if len(diffSnapLines(prev, prev)) != 0 {
		t.Error("identical snapshots should produce no ops")
	}
}

func TestSnapshotDiffReply(t *testing.T) {
	var els []string
	for i := 0; i < 20; i++ {
		els = append(els, fmt.Sprintf(`- Button | "B%d" @%d,0,10×10`, i, i*10))
	}
	prev := snapHeader + "\n" + strings.Join(els, "\n")
	cur2 := append([]string{}, els...)
	cur2[3] = `- Button | "B3" [disabled] @30,0,10×10`
	cur2 = append(cur2, `- Text | "Saved"`)
	cur := snapHeader + "\n" + strings.Join(cur2, "\n") + "\n…[truncated: elements 0–21 of 40; continue with offset=21]"

	out := snapshotDiffReply(prev, true, cur)
	if !strings.Contains(out, "1 added, 0 removed, 1 changed") {
		t.Errorf("summary missing:\n%s", out)
	}
	if !strings.HasPrefix(out, snapHeader) {
		t.Errorf("header should come first:\n%s", out)
	}
	if !strings.Contains(out, `+ - Text | "Saved"`) || !strings.Contains(out, `~ - Button | "B3" [disabled]`) || !strings.Contains(out, `was: - Button | "B3" @30,0,10×10`) {
		t.Errorf("diff lines missing:\n%s", out)
	}
	if strings.Contains(out, `"B7"`) {
		t.Errorf("unchanged lines must be left out:\n%s", out)
	}
	if !strings.HasSuffix(out, "offset=21]") {
		t.Errorf("paging note should be kept:\n%s", out)
	}

	if out := snapshotDiffReply(prev, true, prev); !strings.Contains(out, "no changes since the previous snapshot (20 elements)") {
		t.Errorf("no-change reply:\n%s", out)
	}
	if out := snapshotDiffReply("", false, cur); !strings.Contains(out, "full snapshot follows") || !strings.HasSuffix(out, cur) {
		t.Errorf("no previous snapshot should return the full one:\n%s", out)
	}
	// Everything changed: the full snapshot is cheaper than the diff.
	var other []string
	for i := 0; i < 20; i++ {
		other = append(other, fmt.Sprintf(`- Text | "T%d"`, i))
	}
	full := snapHeader + "\n" + strings.Join(other, "\n")
	if out := snapshotDiffReply(prev, true, full); !strings.Contains(out, "most of the window changed") || !strings.HasSuffix(out, full) {
		t.Errorf("mostly-changed reply:\n%s", out)
	}
}

func TestSnapCache(t *testing.T) {
	c := newSnapCache(2)
	if _, ok := c.swap(1, 0, "a"); ok {
		t.Error("first snapshot of a window has no previous")
	}
	if prev, ok := c.swap(1, 0, "b"); !ok || prev != "a" {
		t.Errorf("swap = %q,%v; want a,true", prev, ok)
	}
	if _, ok := c.swap(1, 50, "p2"); ok {
		t.Error("another page (offset) is not comparable")
	}
	c.swap(2, 0, "x")
	c.swap(1, 0, "c") // touch 1 so 2 is the oldest
	c.swap(3, 0, "y") // evicts 2
	if _, ok := c.swap(2, 0, "x2"); ok {
		t.Error("window 2 should have been evicted")
	}
	if len(c.m) != 2 || len(c.order) != 2 {
		t.Errorf("cache not bounded: %d entries, order %v", len(c.m), c.order)
	}
}
