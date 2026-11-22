package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// page_snapshot diff="true": only what changed since the previous snapshot of
// the same tab.
//
// The daemon sees every snapshot on its way back to the caller, from Chrome
// and app windows alike, so the diff is worked out here once. It asks the
// target to put a "[aglink-page tab=N url=…]" line on top (withPage), takes
// it off again, and remembers the last outline per (profile, session, tab).
// Refs are stable across snapshots (page-actions.js P.snapshot keeps an
// element's number while it exists), so lines are compared by content: a
// control whose state changed shows up as one line with the same ref, which
// the diff reports as changed rather than as a removal plus an addition.

const (
	// snapPageMarker starts the line extension/background.js pageSnapshot and
	// apptools.go put above the outline when asked withPage.
	snapPageMarker = "[aglink-page "
	snapCacheMax   = 16 // tabs remembered; the least recently used goes first
	// snapDiffMaxCells bounds the line-diff table; a bigger pair of outlines
	// (both near maxLines=1000 and mostly different) gets the full snapshot.
	snapDiffMaxCells = 2_500_000
)

var (
	snapPageRE = regexp.MustCompile(`^\[aglink-page tab=(\S*) url=(.*)\]$`)
	snapRefRE  = regexp.MustCompile(`\bref=(e\d+)\b`)
)

// splitSnapPage takes the withPage line off a snapshot: the tab it came from,
// its URL and the outline itself. ok is false when there was none (an
// extension not yet reloaded since this daemon was updated).
func splitSnapPage(text string) (tab, url, body string, ok bool) {
	first, rest, _ := strings.Cut(text, "\n")
	m := snapPageRE.FindStringSubmatch(first)
	if m == nil {
		return "", "", text, false
	}
	return m[1], m[2], rest, true
}

type snapEntry struct {
	url  string
	text string
	used uint64 // when it was stored, in swaps
}

// snapCache is the last snapshot of each recently seen tab. The zero value is
// ready to use.
type snapCache struct {
	mu   sync.Mutex
	m    map[string]snapEntry
	tick uint64 // a counter, not the clock: Windows time is too coarse to order swaps
}

func snapKey(profile, session, tab string) string {
	return strings.ToLower(strings.TrimSpace(profile)) + "\x00" + session + "\x00" + tab
}

// swap stores text as key's latest snapshot and returns the one before it.
func (c *snapCache) swap(key, url, text string) (prev snapEntry, had bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]snapEntry{}
	}
	prev, had = c.m[key]
	c.tick++
	c.m[key] = snapEntry{url: url, text: text, used: c.tick}
	for len(c.m) > snapCacheMax {
		oldest, at := "", uint64(0)
		for k, e := range c.m {
			if oldest == "" || e.used < at {
				oldest, at = k, e.used
			}
		}
		delete(c.m, oldest)
	}
	return prev, had
}

// snapshotCall is page_snapshot through the cache: every snapshot is
// remembered, and diff="true" answers with what changed since the last one.
func (d *Daemon) snapshotCall(sess callSession, params map[string]any, profile string) CallResult {
	p := make(map[string]any, len(params)+1)
	for k, v := range params {
		if k != "diff" {
			p[k] = v
		}
	}
	p["withPage"] = true
	res := d.callAsRoute(sess, "page_snapshot", p, profile)
	if !res.OK {
		return res
	}
	tab, url, body, ok := splitSnapPage(res.Text)
	if !ok || tab == "" {
		tab = strconv.Itoa(intp(params, "tabId", 0))
	}
	prev, had := d.snaps.swap(snapKey(profile, sess.key, tab), url, body)
	if !isTrue(str(params, "diff")) {
		res.Text = body
		return res
	}
	switch {
	case !had:
		res.Text = "[diff: no earlier snapshot of this tab to compare with — full snapshot follows]\n" + body
	case url != "" && prev.url != "" && url != prev.url:
		res.Text = fmt.Sprintf("[diff: the page navigated (was %s, now %s) — full snapshot follows]\n%s", prev.url, url, body)
	default:
		res.Text = snapshotDiff(prev.text, body)
	}
	return res
}

// splitSnapHead separates the "[snapshot N lines, R refs …]" header, which
// changes with any edit, from the outline lines.
func splitSnapHead(text string) (head string, lines []string) {
	all := strings.Split(text, "\n")
	if len(all) > 0 && strings.HasPrefix(all[0], "[snapshot ") {
		return all[0], all[1:]
	}
	return "", all
}

// diffOp is one line of an edit script from the old outline to the new one.
type diffOp struct {
	kind byte // '=' kept, '-' removed, '+' added
	line string
	at   int // index in the new lines (for '-', where the removal sits)
}

// lineDiff is a longest-common-subsequence diff over lines. Common leading and
// trailing lines are taken off first, which on a snapshot after one action is
// nearly all of it. ok is false when what is left is too big to compare.
func lineDiff(a, b []string) (ops []diffOp, ok bool) {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	ma, mb := a[pre:len(a)-suf], b[pre:len(b)-suf]
	n, m := len(ma), len(mb)
	if (n+1)*(m+1) > snapDiffMaxCells {
		return nil, false
	}
	for i := 0; i < pre; i++ {
		ops = append(ops, diffOp{'=', b[i], i})
	}
	// t[i][j] = LCS length of ma[i:] and mb[j:].
	w := m + 1
	t := make([]int32, (n+1)*w)
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if ma[i] == mb[j] {
				t[i*w+j] = t[(i+1)*w+j+1] + 1
			} else if t[(i+1)*w+j] >= t[i*w+j+1] {
				t[i*w+j] = t[(i+1)*w+j]
			} else {
				t[i*w+j] = t[i*w+j+1]
			}
		}
	}
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && ma[i] == mb[j]:
			ops = append(ops, diffOp{'=', mb[j], pre + j})
			i++
			j++
		case i < n && (j == m || t[(i+1)*w+j] >= t[i*w+j+1]):
			// Removals first, as a diff reads.
			ops = append(ops, diffOp{'-', ma[i], pre + j})
			i++
		default:
			ops = append(ops, diffOp{'+', mb[j], pre + j})
			j++
		}
	}
	for k := len(b) - suf; k < len(b); k++ {
		ops = append(ops, diffOp{'=', b[k], k})
	}
	return ops, true
}

func indentOf(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }

func refOf(line string) string {
	if m := snapRefRE.FindStringSubmatch(line); m != nil {
		return m[1]
	}
	return ""
}

// snapshotDiff renders what changed from prev to cur: each run of changes
// under an "@ <enclosing line>" context (the nearest less-indented line above
// it, e.g. the dialog it is in), lines as "+ added", "- removed" and
// "~ changed (was: …)" — a removed and an added line carrying the same ref
// are one control whose name or state changed. When most of the outline
// changed, the full snapshot is clearer and returned instead.
func snapshotDiff(prev, cur string) string {
	_, a := splitSnapHead(prev)
	head, b := splitSnapHead(cur)
	ops, ok := lineDiff(a, b)
	if !ok {
		return "[diff: too much changed to compare line by line — full snapshot follows]\n" + cur
	}
	var added, removed, changed int
	var out []string
	for k := 0; k < len(ops); {
		if ops[k].kind == '=' {
			k++
			continue
		}
		end := k
		for end < len(ops) && ops[end].kind != '=' {
			end++
		}
		hunk := ops[k:end]
		// Pair a removal with an addition of the same ref.
		gone := map[string]string{}
		for _, o := range hunk {
			if r := refOf(o.line); o.kind == '-' && r != "" {
				gone[r] = o.line
			}
		}
		paired := map[string]bool{}
		for _, o := range hunk {
			if r := refOf(o.line); o.kind == '+' && r != "" && gone[r] != "" {
				paired[r] = true
			}
		}
		out = append(out, hunkContext(b, hunk[0].at, hunk))
		for _, o := range hunk {
			r := refOf(o.line)
			switch {
			case o.kind == '-' && paired[r]:
				// reported with its replacement
			case o.kind == '-':
				removed++
				out = append(out, "- "+o.line)
			case paired[r]:
				changed++
				out = append(out, "~ "+o.line+"  (was: "+strings.TrimSpace(gone[r])+")")
			default:
				added++
				out = append(out, "+ "+o.line)
			}
		}
		k = end
	}
	if added+removed+changed == 0 {
		return fmt.Sprintf("[snapshot unchanged since the previous one — %d lines; refs from it still apply]", len(b))
	}
	if n := added + removed + changed; len(b) > 12 && n*10 > len(b)*6 {
		return "[diff: most of the screen changed — full snapshot follows]\n" + cur
	}
	summary := fmt.Sprintf("[snapshot diff since the previous one: %d added, %d removed, %d changed (now %d lines) — unchanged lines left out; refs are stable, so ref=eN from earlier snapshots still apply]", added, removed, changed, len(b))
	if head != "" {
		summary += "\n" + head
	}
	return summary + "\n" + strings.Join(compactContexts(out), "\n")
}

// hunkContext is the "@ …" line naming where a run of changes sits: the
// nearest line above it in the new outline that is indented less than the
// run's first line, or "@ (top)" at the outermost level.
func hunkContext(b []string, at int, hunk []diffOp) string {
	ind := indentOf(hunk[0].line)
	for k := at - 1; k >= 0 && k < len(b); k-- {
		if indentOf(b[k]) < ind {
			return "@ " + strings.TrimSpace(b[k])
		}
	}
	return "@ (top)"
}

// compactContexts drops an "@ …" line that repeats the one before it with no
// other context in between.
func compactContexts(lines []string) []string {
	out := lines[:0:0]
	last := ""
	for _, l := range lines {
		if strings.HasPrefix(l, "@ ") {
			if l == last {
				continue
			}
			last = l
		}
		out = append(out, l)
	}
	return out
}
