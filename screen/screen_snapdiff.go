package main

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// Incremental snapshots (snapshot diff=true), after Playwright MCP's
// incremental page snapshots: the last snapshot text of each window is kept, so
// after an action the model can ask for only what changed instead of re-reading
// the whole tree. Pure logic (no Win32), so it is unit-tested on any OS.

// snapCacheWindows bounds how many windows' last snapshots are kept.
const snapCacheWindows = 8

// snapEntry is one window's last snapshot page.
type snapEntry struct {
	offset int
	text   string
}

// snapCache is a small LRU of the last snapshot per window handle.
type snapCache struct {
	mu    sync.Mutex
	max   int
	order []uintptr // least recently used first
	m     map[uintptr]snapEntry
}

func newSnapCache(max int) *snapCache {
	return &snapCache{max: max, m: map[uintptr]snapEntry{}}
}

// lastSnapshots is the process-wide cache the snapshot tool uses.
var lastSnapshots = newSnapCache(snapCacheWindows)

// swap stores text as hwnd's latest snapshot and returns the previous one. ok
// is false when there was none, or it was a different page (offset), which is
// not comparable.
func (c *snapCache) swap(hwnd uintptr, offset int, text string) (prev string, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	old, had := c.m[hwnd]
	c.m[hwnd] = snapEntry{offset: offset, text: text}
	for i, h := range c.order {
		if h == hwnd {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	c.order = append(c.order, hwnd)
	for len(c.order) > c.max {
		delete(c.m, c.order[0])
		c.order = c.order[1:]
	}
	if !had || old.offset != offset {
		return "", false
	}
	return old.text, true
}

// snapOp is one line of a snapshot diff: '+' added, '-' removed, '~' changed
// (same element, different details; old holds the previous line).
type snapOp struct {
	kind byte
	line string
	old  string
}

// snapLineKey is an element line's identity — indentation, control type, name
// and automation id — without the parts an action changes (state, rectangle,
// preview). Two lines with the same key are reported as one changed element.
func snapLineKey(line string) string {
	trimmed := strings.TrimLeft(line, " ")
	indent := line[:len(line)-len(trimmed)]
	rest, ok := strings.CutPrefix(trimmed, "- ")
	if !ok {
		return line
	}
	typ, after, ok := strings.Cut(rest, " | ")
	if !ok {
		return line
	}
	name, err := strconv.QuotedPrefix(after)
	if err != nil {
		return line
	}
	key := indent + typ + " | " + name
	if id, ok := strings.CutPrefix(after[len(name):], " | id="); ok {
		if i := strings.IndexByte(id, ' '); i >= 0 {
			id = id[:i]
		}
		key += " | id=" + id
	}
	return key
}

// isSnapElementLine tells an element line ("  - Button | …") from the header
// and the paging note.
func isSnapElementLine(line string) bool {
	return strings.HasPrefix(strings.TrimLeft(line, " "), "- ")
}

// diffSnapLines returns the edit script turning prev into cur, in cur's
// document order: an LCS line diff whose removed/added lines within one hunk
// are paired by element key into changes.
func diffSnapLines(prev, cur []string) []snapOp {
	// Trim the common prefix/suffix so the LCS table covers only the hunk(s).
	pre := 0
	for pre < len(prev) && pre < len(cur) && prev[pre] == cur[pre] {
		pre++
	}
	suf := 0
	for suf < len(prev)-pre && suf < len(cur)-pre && prev[len(prev)-1-suf] == cur[len(cur)-1-suf] {
		suf++
	}
	a, b := prev[pre:len(prev)-suf], cur[pre:len(cur)-suf]
	n, m := len(a), len(b)
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}

	var ops []snapOp
	var removed, added []snapOp
	flush := func() {
		// Pair each removed line with the first unpaired added line of the
		// same element (reported in the removed line's place); the rest stay
		// plain removals/additions.
		used := make([]bool, len(added))
		for _, r := range removed {
			k := snapLineKey(r.line)
			op := r
			for j, ad := range added {
				if !used[j] && snapLineKey(ad.line) == k {
					used[j] = true
					op = snapOp{kind: '~', line: ad.line, old: r.line}
					break
				}
			}
			ops = append(ops, op)
		}
		for j, ad := range added {
			if !used[j] {
				ops = append(ops, ad)
			}
		}
		removed, added = removed[:0], added[:0]
	}
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			flush()
			i++
			j++
		case j < m && (i == n || lcs[i][j+1] >= lcs[i+1][j]):
			added = append(added, snapOp{kind: '+', line: b[j]})
			j++
		default:
			removed = append(removed, snapOp{kind: '-', line: a[i]})
			i++
		}
	}
	flush()
	return ops
}

// snapshotDiffReply renders snapshot diff=true's reply. Without a comparable
// previous snapshot (none yet, another window, another page) it returns the
// full snapshot behind a note; when most of the window changed the full
// snapshot is cheaper to read than the diff, so it is returned too.
func snapshotDiffReply(prev string, ok bool, cur string) string {
	if !ok {
		return "(diff: no previous snapshot of this window/page to compare with — full snapshot follows; the next diff=true call will be incremental)\n" + cur
	}
	var meta, curEls, prevEls []string
	for _, l := range strings.Split(cur, "\n") {
		if isSnapElementLine(l) {
			curEls = append(curEls, l)
		} else {
			meta = append(meta, l)
		}
	}
	for _, l := range strings.Split(prev, "\n") {
		if isSnapElementLine(l) {
			prevEls = append(prevEls, l)
		}
	}
	ops := diffSnapLines(prevEls, curEls)
	var nAdd, nDel, nChg int
	for _, op := range ops {
		switch op.kind {
		case '+':
			nAdd++
		case '-':
			nDel++
		default:
			nChg++
		}
	}
	summary := fmt.Sprintf("%d added, %d removed, %d changed", nAdd, nDel, nChg)
	if len(ops) == 0 {
		summary = fmt.Sprintf("no changes since the previous snapshot (%d elements)", len(curEls))
	} else if len(curEls) > 8 && len(ops)*2 > len(curEls) {
		return fmt.Sprintf("(diff: %s — most of the window changed, so the full snapshot follows)\n%s", summary, cur)
	}

	var b strings.Builder
	header, footer := "", meta
	if len(meta) > 0 && !strings.HasPrefix(meta[0], "…") {
		header, footer = meta[0], meta[1:]
	}
	if header != "" {
		b.WriteString(header + "\n")
	}
	fmt.Fprintf(&b, "diff vs previous snapshot: %s\n", summary)
	for _, op := range ops {
		switch op.kind {
		case '~':
			fmt.Fprintf(&b, "~ %s\n    was: %s\n", op.line, strings.TrimLeft(op.old, " "))
		default:
			fmt.Fprintf(&b, "%c %s\n", op.kind, op.line)
		}
	}
	for _, l := range footer {
		if strings.TrimSpace(l) != "" {
			b.WriteString(l + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
