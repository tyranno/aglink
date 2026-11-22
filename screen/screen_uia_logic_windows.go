//go:build windows

package main

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Pure (COM-free) logic behind the UIA tools: element selectors (name match
// mode, control type, nth), "did you mean" hints, the snapshot state flags,
// set_state's before→after wording and wait_for_control's conditions. Kept apart
// from screen_uia_windows.go so all of it is unit-testable without a live UIA
// tree.

// ---- selector ----

// Name match modes for uiaSelector.Match.
const (
	matchExact    = "exact"    // case-sensitive equality (UIA's own property match)
	matchContains = "contains" // case-insensitive substring
	matchPrefix   = "prefix"   // case-insensitive prefix
)

// uiaSelector picks an element by Name (falling back to AutomationId), narrowed
// by control type and disambiguated by nth.
type uiaSelector struct {
	Name        string
	ControlType int32  // UIA control type id; 0 = any
	Nth         int    // 1-based pick among the matches; 0 = not given
	Match       string // matchExact (default) | matchContains | matchPrefix
}

// newSelector validates the selector arguments shared by invoke / set_value /
// get_value / get_text / wait_for_control / set_state.
func newSelector(name, controlType string, nth int, match string) (uiaSelector, error) {
	sel := uiaSelector{Name: name, Nth: nth}
	if nth < 0 {
		return sel, fmt.Errorf("nth must be 1 or more (1 = first match), got %d", nth)
	}
	switch m := strings.ToLower(strings.TrimSpace(match)); m {
	case "", matchExact:
		sel.Match = matchExact
	case matchContains, matchPrefix:
		sel.Match = m
	default:
		return sel, fmt.Errorf("match must be exact, contains or prefix, got %q", match)
	}
	if strings.TrimSpace(controlType) != "" {
		id, ok := parseControlType(controlType)
		if !ok {
			return sel, fmt.Errorf("unknown control_type %q (e.g. Button, Edit, CheckBox, ComboBox, ListItem, MenuItem, TreeItem, TabItem, Hyperlink, Text)", controlType)
		}
		sel.ControlType = id
	}
	return sel, nil
}

// describe names the selector in messages: `button "OK"`, `element containing "Sa"`.
func (s uiaSelector) describe() string {
	kind := "element"
	if s.ControlType != 0 {
		kind = controlTypeName(s.ControlType)
	}
	switch s.Match {
	case matchContains:
		return fmt.Sprintf("%s containing %q", kind, s.Name)
	case matchPrefix:
		return fmt.Sprintf("%s starting with %q", kind, s.Name)
	}
	if kind == "element" {
		return fmt.Sprintf("element named %q", s.Name)
	}
	return fmt.Sprintf("%s %q", kind, s.Name)
}

// matches reports whether candidate (a Name or AutomationId) satisfies the
// selector's text rule.
func (s uiaSelector) matches(candidate string) bool {
	return nameMatches(s.Match, s.Name, candidate)
}

// nameMatches applies a match mode: exact is case-sensitive (what UIA's own
// property condition does), contains/prefix are case-insensitive.
func nameMatches(mode, query, candidate string) bool {
	switch mode {
	case matchContains:
		return query != "" && strings.Contains(strings.ToLower(candidate), strings.ToLower(query))
	case matchPrefix:
		return query != "" && strings.HasPrefix(strings.ToLower(candidate), strings.ToLower(query))
	default:
		return candidate == query
	}
}

// normControlType folds a control type spelling to a lookup key:
// "List Item", "list_item", "ListItem" → "listitem".
func normControlType(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.NewReplacer(" ", "", "_", "", "-", "").Replace(s)
}

// controlTypeAliases are common names that are not controlTypeName's label.
var controlTypeAliases = map[string]int32{
	"link":         50005,
	"textbox":      50004,
	"input":        50004,
	"dropdown":     50003,
	"radio":        50013,
	"check":        50002,
	"menuitem":     50011,
	"treeview":     50023,
	"listview":     50008,
	"listbox":      50008,
	"label":        50020,
	"statictext":   50020,
	"toggle":       50000, // a toggle button reports as a Button with the Toggle pattern
	"togglebutton": 50000,
}

// parseControlType maps a control type name (UIA's ControlType names like
// "CheckBox", snapshot's labels like "check box"/"checkbox", a few aliases, or
// the numeric id) to its UIA control type id.
func parseControlType(s string) (int32, bool) {
	key := normControlType(s)
	if key == "" {
		return 0, false
	}
	if n, err := strconv.Atoi(key); err == nil {
		if n >= 50000 && n <= 50040 {
			return int32(n), true
		}
		return 0, false
	}
	for id := int32(50000); id <= 50040; id++ {
		if normControlType(controlTypeName(id)) == key {
			return id, true
		}
	}
	if id, ok := controlTypeAliases[key]; ok {
		return id, true
	}
	return 0, false
}

// ordinal renders 1 → "1st", 2 → "2nd", 11 → "11th".
func ordinal(n int) string {
	suffix := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return strconv.Itoa(n) + suffix
}

// pickMatch chooses among total matches (document order). nth (1-based) wins
// when given; otherwise preferred (0-based index of the first match that
// supports what the caller needs, -1 = none) or the first. note tells the
// caller there were several, so a wrong pick is visible rather than silent.
func pickMatch(total, nth, preferred int) (idx int, note string, err error) {
	if total <= 0 {
		return -1, "", fmt.Errorf("no matches")
	}
	if nth > 0 {
		if nth > total {
			return -1, "", fmt.Errorf("nth=%d but only %d match(es)", nth, total)
		}
		if total > 1 {
			note = fmt.Sprintf("matched %d; used %s (nth=%d)", total, ordinal(nth), nth)
		}
		return nth - 1, note, nil
	}
	idx = 0
	if preferred > 0 && preferred < total {
		idx = preferred
	}
	if total > 1 {
		if idx == 0 {
			note = fmt.Sprintf("matched %d; used 1st (use nth to pick another)", total)
		} else {
			note = fmt.Sprintf("matched %d; used %s — the first that supports this action (use nth to pick another)", total, ordinal(idx+1))
		}
	}
	return idx, note, nil
}

// withNote appends a selector note to a tool's ok text.
func withNote(s, note string) string {
	if note == "" {
		return s
	}
	return s + " (" + note + ")"
}

// closestNames ranks candidate names by how close they are to query and
// returns up to k distinct ones — the "did you mean" hint when nothing matched.
// Case-insensitive: equal-but-different-case first, then names containing the
// query, then names the query contains, then small edit distances.
func closestNames(query string, names []string, k int) []string {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" || k <= 0 {
		return nil
	}
	type scored struct {
		name  string
		score int // lower is closer
		order int
	}
	seen := map[string]bool{}
	var out []scored
	for i, n := range names {
		t := strings.TrimSpace(n)
		if t == "" || seen[t] {
			continue
		}
		l := strings.ToLower(t)
		score := -1
		switch {
		case l == q:
			score = 0
		case strings.HasPrefix(l, q):
			score = 1
		case strings.Contains(l, q):
			score = 2
		case utf8.RuneCountInString(l) >= 2 && strings.Contains(q, l):
			score = 3
		default:
			// Typos: allow ~1 edit per 3 runes (at least 1, at most 3).
			limit := utf8.RuneCountInString(q) / 3
			if limit < 1 {
				limit = 1
			}
			if limit > 3 {
				limit = 3
			}
			if d := levenshtein(q, l); d <= limit {
				score = 3 + d
			}
		}
		if score < 0 {
			continue
		}
		seen[t] = true
		out = append(out, scored{t, score, i})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score < out[j].score
		}
		return out[i].order < out[j].order
	})
	var res []string
	for i := 0; i < len(out) && i < k; i++ {
		res = append(res, out[i].name)
	}
	return res
}

// levenshtein is the rune-wise edit distance between a and b.
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// notFoundError is the "nothing matched" error, with up to 5 near names.
func notFoundError(sel uiaSelector, hints []string) error {
	if len(hints) == 0 {
		return fmt.Errorf("no %s (try snapshot)", sel.describe())
	}
	q := make([]string, len(hints))
	for i, h := range hints {
		q[i] = strconv.Quote(h)
	}
	return fmt.Errorf("no %s; closest: %s (try snapshot, or match=\"contains\")", sel.describe(), strings.Join(q, ", "))
}

// pickItemIndex chooses the list item for set_state's select_item: exact
// name, then case-insensitive exact, prefix, substring. -1 when none.
func pickItemIndex(names []string, item string) int {
	for i, n := range names {
		if n == item {
			return i
		}
	}
	li := strings.ToLower(strings.TrimSpace(item))
	if li == "" {
		return -1
	}
	for _, rule := range []func(string) bool{
		func(l string) bool { return l == li },
		func(l string) bool { return strings.HasPrefix(l, li) },
		func(l string) bool { return strings.Contains(l, li) },
	} {
		for i, n := range names {
			if rule(strings.ToLower(strings.TrimSpace(n))) {
				return i
			}
		}
	}
	return -1
}

// ---- element state ----

// UIA ToggleState / ExpandCollapseState values.
const (
	toggleOff           = 0
	toggleOn            = 1
	toggleIndeterminate = 2

	ecCollapsed         = 0
	ecExpanded          = 1
	ecPartiallyExpanded = 2
	ecLeafNode          = 3
)

// stateNone marks a state the element does not expose.
const stateNone = -1

// toggleStateName is the snapshot/set_state word for a ToggleState.
func toggleStateName(s int32) string {
	switch s {
	case toggleOff:
		return "unchecked"
	case toggleOn:
		return "checked"
	case toggleIndeterminate:
		return "mixed"
	}
	return fmt.Sprintf("toggle%d", s)
}

// expandStateName is the snapshot/set_state word for an ExpandCollapseState.
func expandStateName(s int32) string {
	switch s {
	case ecCollapsed:
		return "collapsed"
	case ecExpanded:
		return "expanded"
	case ecPartiallyExpanded:
		return "partially expanded"
	case ecLeafNode:
		return "leaf"
	}
	return fmt.Sprintf("expand%d", s)
}

// formatNumber prints a RangeValue compactly: 42, 0.5, 1e+06.
func formatNumber(v float64) string {
	return strconv.FormatFloat(v, 'g', 6, 64)
}

// stateFlags renders the element-state flags shared by snapshot lines and
// element_at: [checked]/[unchecked]/[mixed], [expanded]/[collapsed],
// [selected], [value=N]. Leaf nodes and unselected items add nothing (noise).
func stateFlags(nd uiaNode) string {
	var b strings.Builder
	if nd.toggle != stateNone {
		fmt.Fprintf(&b, " [%s]", toggleStateName(nd.toggle))
	}
	if nd.expand != stateNone && nd.expand != ecLeafNode {
		fmt.Fprintf(&b, " [%s]", expandStateName(nd.expand))
	}
	if nd.selected {
		b.WriteString(" [selected]")
	}
	if nd.hasRange {
		fmt.Fprintf(&b, " [value=%s]", formatNumber(nd.rangeValue))
	}
	return b.String()
}

// rectSuffix renders a screen rectangle as " @x,y,w×h", "" when the element
// has no on-screen box (empty, or a minimized window's -32000 placeholder).
func rectSuffix(r rect) string {
	w, h := r.Right-r.Left, r.Bottom-r.Top
	if w <= 0 || h <= 0 || r.Left <= -32000 {
		return ""
	}
	return fmt.Sprintf(" @%d,%d,%d×%d", r.Left, r.Top, w, h)
}

// snapshotIndentCap bounds indentation so a 40-level Chromium tree cannot
// spend its character budget on leading spaces.
const snapshotIndentCap = 12

// stateChange is set_state's report for one property: "checked: unchecked→checked",
// or "checked: already checked (no change)".
func stateChange(what, before, after string) string {
	if before == after {
		return fmt.Sprintf("%s: already %s (no change)", what, after)
	}
	return fmt.Sprintf("%s: %s→%s", what, before, after)
}

// sameRangeValue treats two RangeValue values as equal within float noise
// (a slider may store 49.99999 for 50).
func sameRangeValue(a, b float64) bool {
	return math.Abs(a-b) <= 1e-9*math.Max(1, math.Max(math.Abs(a), math.Abs(b)))
}

// ---- wait_for_control conditions ----

// wait_for_control "until" conditions.
const (
	untilAppear    = "appear"
	untilDisappear = "disappear"
	untilEnabled   = "enabled"
	untilValue     = "value"
)

// waitCond is what wait_for_control waits for.
type waitCond struct {
	Until    string
	Value    string // until=value: exact value wanted (when ValueSet)
	ValueSet bool
	Contains string // until=value: substring wanted (case-insensitive)
}

// newWaitCond validates until/value/contains. Giving value or contains without
// until implies until=value.
func newWaitCond(until, value string, valueSet bool, contains string) (waitCond, error) {
	c := waitCond{Until: strings.ToLower(strings.TrimSpace(until)), Value: value, ValueSet: valueSet, Contains: contains}
	if c.Until == "" {
		c.Until = untilAppear
		if valueSet || contains != "" {
			c.Until = untilValue
		}
	}
	switch c.Until {
	case untilAppear, untilDisappear, untilEnabled:
		if valueSet || contains != "" {
			return c, fmt.Errorf("value/contains only apply to until=value")
		}
	case untilValue:
		if !valueSet && contains == "" {
			return c, fmt.Errorf("until=value needs 'value' (exact) or 'contains'")
		}
	default:
		return c, fmt.Errorf("until must be appear, disappear, enabled or value, got %q", until)
	}
	return c, nil
}

// uiaProbeResult is one wait_for_control observation of the target.
type uiaProbeResult struct {
	Found    bool
	Enabled  bool
	Value    string
	HasValue bool     // Value was readable (Value or Text pattern)
	Hints    []string // closest names, filled only when asked and not Found
	Note     string   // "matched N; used 1st (...)" when sel matched more than one — see pickCandidate
}

// met reports whether observation p satisfies the condition.
func (c waitCond) met(p uiaProbeResult) bool {
	switch c.Until {
	case untilDisappear:
		return !p.Found
	case untilEnabled:
		return p.Found && p.Enabled
	case untilValue:
		if !p.Found || !p.HasValue {
			return false
		}
		if c.ValueSet && p.Value != c.Value {
			return false
		}
		if c.Contains != "" && !strings.Contains(strings.ToLower(p.Value), strings.ToLower(c.Contains)) {
			return false
		}
		return true
	default:
		return p.Found
	}
}

// describe is the condition as a phrase: "to disappear", `to have value "x"`.
func (c waitCond) describe() string {
	switch c.Until {
	case untilDisappear:
		return "to disappear"
	case untilEnabled:
		return "to become enabled"
	case untilValue:
		var parts []string
		if c.ValueSet {
			parts = append(parts, fmt.Sprintf("value %q", c.Value))
		}
		if c.Contains != "" {
			parts = append(parts, fmt.Sprintf("value containing %q", c.Contains))
		}
		return "to have " + strings.Join(parts, " and ")
	}
	return "to appear"
}

// observed summarizes the last observation for a timeout message.
func (c waitCond) observed(p uiaProbeResult) string {
	switch {
	case !p.Found:
		return "not found"
	case c.Until == untilValue && !p.HasValue:
		return "found, but its value is unreadable"
	case c.Until == untilValue:
		return fmt.Sprintf("value %q", onelinePreview(p.Value, 80))
	case !p.Enabled:
		return "present but disabled"
	}
	return "present and enabled"
}
