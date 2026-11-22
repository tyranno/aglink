//go:build windows

package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseControlType(t *testing.T) {
	cases := map[string]int32{
		"Button": 50000, "button": 50000, "CheckBox": 50002, "check box": 50002, "checkbox": 50002,
		"ComboBox": 50003, "Edit": 50004, "Hyperlink": 50005, "link": 50005,
		"ListItem": 50007, "list item": 50007, "list_item": 50007, "MenuItem": 50011,
		"RadioButton": 50013, "radio": 50013, "TabItem": 50019, "Text": 50020,
		"TreeItem": 50024, "tree-item": 50024, "Document": 50030, "50015": 50015,
	}
	for in, want := range cases {
		if got, ok := parseControlType(in); !ok || got != want {
			t.Errorf("parseControlType(%q) = %d,%v want %d", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "  ", "gizmo", "49999", "50041"} {
		if _, ok := parseControlType(bad); ok {
			t.Errorf("parseControlType(%q) should fail", bad)
		}
	}
}

func TestNewSelector(t *testing.T) {
	sel, err := newSelector("OK", "Button", 2, "")
	if err != nil || sel.ControlType != 50000 || sel.Nth != 2 || sel.Match != matchExact {
		t.Fatalf("got %+v, %v", sel, err)
	}
	if sel, err := newSelector("ok", "", 0, " Contains "); err != nil || sel.Match != matchContains {
		t.Fatalf("contains: %+v, %v", sel, err)
	}
	if _, err := newSelector("x", "", -1, ""); err == nil {
		t.Error("negative nth must fail")
	}
	if _, err := newSelector("x", "", 0, "regex"); err == nil {
		t.Error("unknown match must fail")
	}
	if _, err := newSelector("x", "Widget", 0, ""); err == nil || !strings.Contains(err.Error(), "control_type") {
		t.Errorf("unknown control_type: %v", err)
	}
}

func TestNameMatches(t *testing.T) {
	cases := []struct {
		mode, q, c string
		want       bool
	}{
		{matchExact, "Save", "Save", true},
		{matchExact, "Save", "save", false}, // exact stays case-sensitive (UIA's own match)
		{matchContains, "save", "Save As…", true},
		{matchContains, "AS", "Save As", true},
		{matchContains, "", "anything", false},
		{matchPrefix, "sav", "Save As", true},
		{matchPrefix, "as", "Save As", false},
		{matchPrefix, "파일", "파일(F)", true},
	}
	for _, c := range cases {
		if got := nameMatches(c.mode, c.q, c.c); got != c.want {
			t.Errorf("nameMatches(%s,%q,%q) = %v", c.mode, c.q, c.c, got)
		}
	}
}

func TestSelectorDescribe(t *testing.T) {
	cases := map[string]uiaSelector{
		`element named "OK"`:         {Name: "OK", Match: matchExact},
		`button "OK"`:                {Name: "OK", ControlType: 50000, Match: matchExact},
		`element containing "sa"`:    {Name: "sa", Match: matchContains},
		`checkbox starting with "W"`: {Name: "W", ControlType: 50002, Match: matchPrefix},
	}
	for want, sel := range cases {
		if got := sel.describe(); got != want {
			t.Errorf("describe(%+v) = %q want %q", sel, got, want)
		}
	}
}

func TestPickMatch(t *testing.T) {
	cases := []struct {
		total, nth, pref int
		idx              int
		note             string
		err              bool
	}{
		{1, 0, -1, 0, "", false},
		{3, 0, -1, 0, "matched 3; used 1st (use nth to pick another)", false},
		{3, 0, 0, 0, "matched 3; used 1st (use nth to pick another)", false},
		{3, 0, 2, 2, "matched 3; used 3rd — the first that supports this action (use nth to pick another)", false},
		{3, 2, 0, 1, "matched 3; used 2nd (nth=2)", false},
		{1, 1, -1, 0, "", false},
		{2, 5, -1, -1, "", true},
		{0, 0, -1, -1, "", true},
	}
	for _, c := range cases {
		idx, note, err := pickMatch(c.total, c.nth, c.pref)
		if idx != c.idx || note != c.note || (err != nil) != c.err {
			t.Errorf("pickMatch(%d,%d,%d) = %d,%q,%v", c.total, c.nth, c.pref, idx, note, err)
		}
	}
}

func TestOrdinal(t *testing.T) {
	for n, want := range map[int]string{1: "1st", 2: "2nd", 3: "3rd", 4: "4th", 11: "11th", 12: "12th", 13: "13th", 21: "21st", 22: "22nd", 101: "101st", 111: "111th"} {
		if got := ordinal(n); got != want {
			t.Errorf("ordinal(%d) = %q want %q", n, got, want)
		}
	}
}

func TestClosestNames(t *testing.T) {
	names := []string{"Save", "Save As", "Cancel", "Autosave settings", "Open", "save", "", "Sav", "Help"}
	got := closestNames("save", names, 5)
	want := []string{"Save", "save", "Save As", "Autosave settings", "Sav"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("closestNames = %q want %q", got, want)
	}
	// Typo tolerance, and unrelated names are not offered.
	if got := closestNames("Cancle", names, 5); !reflect.DeepEqual(got, []string{"Cancel"}) {
		t.Errorf("typo: %q", got)
	}
	if got := closestNames("Zebra", names, 5); len(got) != 0 {
		t.Errorf("unrelated: %q", got)
	}
	if got := closestNames("", names, 5); got != nil {
		t.Errorf("empty query: %q", got)
	}
	if got := closestNames("s", []string{"a1", "s1", "s2", "s3"}, 2); len(got) != 2 {
		t.Errorf("k not honored: %q", got)
	}
}

func TestLevenshtein(t *testing.T) {
	cases := []struct {
		a, b string
		d    int
	}{{"", "", 0}, {"abc", "", 3}, {"kitten", "sitting", 3}, {"cancle", "cancel", 2}, {"확인", "확안", 1}}
	for _, c := range cases {
		if got := levenshtein(c.a, c.b); got != c.d {
			t.Errorf("levenshtein(%q,%q) = %d want %d", c.a, c.b, got, c.d)
		}
	}
}

func TestNotFoundError(t *testing.T) {
	sel := uiaSelector{Name: "Sve", Match: matchExact}
	if e := notFoundError(sel, nil).Error(); e != `no element named "Sve" (try snapshot)` {
		t.Errorf("no hints: %q", e)
	}
	e := notFoundError(sel, []string{"Save (button)", "Save As (button)"}).Error()
	if !strings.Contains(e, `closest: "Save (button)", "Save As (button)"`) {
		t.Errorf("hints: %q", e)
	}
}

func TestPickItemIndex(t *testing.T) {
	names := []string{"Small", "Medium", "large", "Extra Large"}
	cases := map[string]int{"Medium": 1, "LARGE": 2, "ext": 3, "arg": 2, "med": 1, "Huge": -1, "": -1}
	for item, want := range cases {
		if got := pickItemIndex(names, item); got != want {
			t.Errorf("pickItemIndex(%q) = %d want %d", item, got, want)
		}
	}
}

func TestStateFlagsAndSnapshotLine(t *testing.T) {
	nd := uiaNode{name: "Word wrap", ctrlType: 50002, enabled: true, toggle: toggleOn, expand: stateNone,
		rect: rect{Left: 10, Top: 20, Right: 110, Bottom: 44}, depth: 2}
	if got := snapshotLine(nd); got != `    - checkbox | "Word wrap" [checked] @10,20,100×24` {
		t.Errorf("line = %q", got)
	}
	nd = uiaNode{name: "Folder", ctrlType: 50024, enabled: true, invoke: true, toggle: stateNone, expand: ecCollapsed,
		selectable: true, selected: true}
	if got := nodeDesc(nd); got != `tree item | "Folder" [invokable] [collapsed] [selected]` {
		t.Errorf("tree item = %q", got)
	}
	// Leaf nodes and unselected items add no noise; sliders show their value.
	nd = uiaNode{name: "Volume", ctrlType: 50015, enabled: false, toggle: stateNone, expand: ecLeafNode,
		selectable: true, hasRange: true, rangeValue: 42.5}
	if got := nodeDesc(nd); got != `slider | "Volume" [disabled] [value=42.5]` {
		t.Errorf("slider = %q", got)
	}
	nd = uiaNode{ctrlType: 50002, toggle: toggleIndeterminate, expand: ecPartiallyExpanded, enabled: true}
	if got := stateFlags(nd); got != " [mixed] [partially expanded]" {
		t.Errorf("flags = %q", got)
	}
	// Indentation is capped.
	if got := snapshotLine(uiaNode{name: "x", enabled: true, toggle: stateNone, expand: stateNone, depth: 40}); !strings.HasPrefix(got, strings.Repeat("  ", snapshotIndentCap)+"- ") {
		t.Errorf("indent cap: %q", got)
	}
}

func TestRectSuffix(t *testing.T) {
	cases := []struct {
		r    rect
		want string
	}{
		{rect{0, 0, 10, 10}, " @0,0,10×10"},
		{rect{-1920, 100, -1800, 130}, " @-1920,100,120×30"}, // monitor left of primary
		{rect{5, 5, 5, 20}, ""},                              // empty
		{rect{-32000, -32000, -31840, -31972}, ""},           // minimized placeholder
	}
	for _, c := range cases {
		if got := rectSuffix(c.r); got != c.want {
			t.Errorf("rectSuffix(%+v) = %q want %q", c.r, got, c.want)
		}
	}
}

func TestAnonymousNode(t *testing.T) {
	blank := uiaNode{toggle: stateNone, expand: stateNone}
	if !blank.anonymous() {
		t.Error("blank pane should be skipped")
	}
	unnamedCheck := blank
	unnamedCheck.toggle = toggleOff
	if unnamedCheck.anonymous() {
		t.Error("an unnamed checkbox carries state and must be listed")
	}
	slider := blank
	slider.hasRange = true
	if slider.anonymous() {
		t.Error("an unnamed slider must be listed")
	}
}

func TestStateChange(t *testing.T) {
	if got := stateChange("checked", "unchecked", "checked"); got != "checked: unchecked→checked" {
		t.Errorf("%q", got)
	}
	if got := stateChange("expanded", "expanded", "expanded"); got != "expanded: already expanded (no change)" {
		t.Errorf("%q", got)
	}
	if !sameRangeValue(50, 49.9999999999) || sameRangeValue(50, 49.9) || !sameRangeValue(0, 0) {
		t.Error("sameRangeValue tolerance wrong")
	}
	if formatNumber(42) != "42" || formatNumber(0.25) != "0.25" {
		t.Error("formatNumber")
	}
}

func TestNewWaitCond(t *testing.T) {
	c, err := newWaitCond("", "", false, "")
	if err != nil || c.Until != untilAppear {
		t.Fatalf("default: %+v %v", c, err)
	}
	if c, err := newWaitCond("", "", true, ""); err != nil || c.Until != untilValue {
		t.Fatalf("value alone (even empty) implies until=value: %+v %v", c, err)
	}
	if c, err := newWaitCond("", "", false, "done"); err != nil || c.Until != untilValue {
		t.Fatalf("contains implies value: %+v %v", c, err)
	}
	if _, err := newWaitCond("value", "", false, ""); err == nil {
		t.Error("until=value without value/contains must fail")
	}
	if _, err := newWaitCond("disappear", "x", true, ""); err == nil {
		t.Error("value with until=disappear must fail")
	}
	if _, err := newWaitCond("vanish", "", false, ""); err == nil {
		t.Error("unknown until must fail")
	}
	if c, err := newWaitCond(" Enabled ", "", false, ""); err != nil || c.Until != untilEnabled {
		t.Errorf("case/space: %+v %v", c, err)
	}
}

func TestWaitCondMet(t *testing.T) {
	gone := uiaProbeResult{}
	disabled := uiaProbeResult{Found: true}
	enabled := uiaProbeResult{Found: true, Enabled: true}
	val := func(s string) uiaProbeResult {
		return uiaProbeResult{Found: true, Enabled: true, Value: s, HasValue: true}
	}
	cases := []struct {
		c    waitCond
		p    uiaProbeResult
		want bool
	}{
		{waitCond{Until: untilAppear}, gone, false},
		{waitCond{Until: untilAppear}, disabled, true},
		{waitCond{Until: untilDisappear}, gone, true},
		{waitCond{Until: untilDisappear}, enabled, false},
		{waitCond{Until: untilEnabled}, disabled, false},
		{waitCond{Until: untilEnabled}, enabled, true},
		{waitCond{Until: untilEnabled}, gone, false},
		{waitCond{Until: untilValue, Value: "100%", ValueSet: true}, val("100%"), true},
		{waitCond{Until: untilValue, Value: "100%", ValueSet: true}, val("99%"), false},
		{waitCond{Until: untilValue, Value: "", ValueSet: true}, val(""), true},
		{waitCond{Until: untilValue, Value: "", ValueSet: true}, enabled, false}, // value unreadable
		{waitCond{Until: untilValue, Contains: "DONE"}, val("Build done in 3s"), true},
		{waitCond{Until: untilValue, Contains: "done"}, val("Building…"), false},
		{waitCond{Until: untilValue, Contains: "done"}, gone, false},
	}
	for i, c := range cases {
		if got := c.c.met(c.p); got != c.want {
			t.Errorf("case %d: %+v met(%+v) = %v", i, c.c, c.p, got)
		}
	}
}

func TestWaitCondDescribe(t *testing.T) {
	if d := (waitCond{Until: untilValue, Value: "a", ValueSet: true, Contains: "b"}).describe(); d != `to have value "a" and value containing "b"` {
		t.Errorf("%q", d)
	}
	if o := (waitCond{Until: untilEnabled}).observed(uiaProbeResult{Found: true}); o != "present but disabled" {
		t.Errorf("%q", o)
	}
	if o := (waitCond{Until: untilValue}).observed(uiaProbeResult{Found: true, HasValue: true, Value: "50%"}); o != `value "50%"` {
		t.Errorf("%q", o)
	}
}

func TestStateRequestEmpty(t *testing.T) {
	if !(stateRequest{}).empty() || !(stateRequest{SelectItem: "  "}).empty() {
		t.Error("empty request not detected")
	}
	f := false
	if (stateRequest{Checked: &f}).empty() {
		t.Error("checked=false is a request")
	}
}
