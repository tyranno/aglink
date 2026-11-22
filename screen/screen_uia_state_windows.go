//go:build windows

package main

import (
	"fmt"
	"math"
	"strings"
	"time"
	"unsafe"

	ole "github.com/go-ole/go-ole"
)

// set_state — drive an element to a wanted STATE (checked, expanded,
// selected, a slider value, scrolled into view, a combo box item) instead of
// "activating" it like invoke does. Each part reads the current state first and
// acts only when it differs, so a set_state call is idempotent and its reply
// reports before→after. Runs on the UIA STA thread like every UIA op.

// stateRequest is what one set_state call asks for; nil/zero fields are left alone.
type stateRequest struct {
	Checked        *bool
	Expanded       *bool
	Selected       *bool
	Value          *float64
	ScrollIntoView bool
	SelectItem     string // name of the item to choose inside a combo box / list / tab
}

func (r stateRequest) empty() bool {
	return r.Checked == nil && r.Expanded == nil && r.Selected == nil && r.Value == nil &&
		!r.ScrollIntoView && strings.TrimSpace(r.SelectItem) == ""
}

// pred prefers, among same-named elements, one with the pattern the request
// needs (see findElement).
func (r stateRequest) pred() func(*ole.IUnknown) bool {
	var ids []int
	if r.Checked != nil {
		ids = append(ids, uiaTogglePatternId)
	}
	if r.Expanded != nil {
		ids = append(ids, uiaExpandCollapsePatternId)
	}
	if r.Selected != nil {
		ids = append(ids, uiaSelectionItemPatternId)
	}
	if r.Value != nil {
		ids = append(ids, uiaRangeValuePatternId)
	}
	if r.ScrollIntoView {
		ids = append(ids, uiaScrollItemPatternId)
	}
	if strings.TrimSpace(r.SelectItem) != "" {
		ids = append(ids, uiaExpandCollapsePatternId, uiaSelectionPatternId)
	}
	return func(el *ole.IUnknown) bool {
		for _, id := range ids {
			if elemSupportsPattern(el, id) {
				return true
			}
		}
		return false
	}
}

// stateSettle is how long set_state waits for a state change to show up in a
// re-read (providers update ExpandCollapseState / list contents asynchronously).
var stateSettle = 600 * time.Millisecond

// uiaSetStateIn finds sel under window hwnd (0 = foreground; background when
// != 0, see uiaInvokeIn) and applies req: scroll_into_view, expanded, checked,
// selected, value, select_item — in that order. The reply lists one
// before→after part per requested property.
func uiaSetStateIn(hwnd uintptr, sel uiaSelector, req stateRequest) (string, error) {
	if req.empty() {
		return "", fmt.Errorf("set_state needs one of checked, expanded, selected, value, scroll_into_view, select_item")
	}
	if err := beginUIAOp(hwnd); err != nil {
		return "", err
	}
	cx, cy, marked := 0, 0, false
	out, err := uiaDo(func(uia *ole.IUnknown) (string, error) {
		root, err := uiaRoot(uia, hwnd)
		if err != nil {
			return "", err
		}
		defer release(root)
		el, note, err := findElement(uia, root, sel, req.pred())
		if err != nil {
			return "", err
		}
		defer release(el)

		var parts []string
		step := func(s string, err error) error {
			if err != nil {
				if len(parts) > 0 {
					return fmt.Errorf("%v (already done: %s)", err, strings.Join(parts, "; "))
				}
				return err
			}
			parts = append(parts, s)
			return nil
		}
		if req.ScrollIntoView {
			if err := step(stateScrollIntoView(el)); err != nil {
				return "", err
			}
		}
		if req.Expanded != nil {
			if err := step(stateExpand(el, *req.Expanded)); err != nil {
				return "", err
			}
		}
		if req.Checked != nil {
			if err := step(stateToggle(el, *req.Checked)); err != nil {
				return "", err
			}
		}
		if req.Selected != nil {
			if err := step(stateSelect(el, *req.Selected)); err != nil {
				return "", err
			}
		}
		if req.Value != nil {
			if err := step(stateRange(el, *req.Value)); err != nil {
				return "", err
			}
		}
		if item := strings.TrimSpace(req.SelectItem); item != "" {
			if err := step(stateSelectItem(uia, root, el, item)); err != nil {
				return "", err
			}
		}
		if hwnd != 0 {
			cx, cy, marked = elemCenter(el)
		}
		nd := readNodeOne(uia, el)
		label := fmt.Sprintf("%s %q", controlTypeName(nd.ctrlType), nd.name)
		return withNote(label+": "+strings.Join(parts, "; "), note), nil
	})
	if err == nil && marked {
		showAgentCursor(cx, cy)
	}
	return out, err
}

// curInt / curBool / curFloat read a live pattern-state property.
func curInt(el *ole.IUnknown, prop int) (int32, bool) {
	return elemPropInt(el, elemGetCurrentPropertyVal, prop)
}

func curBool(el *ole.IUnknown, prop int) (bool, bool) {
	return elemPropBool(el, elemGetCurrentPropertyVal, prop)
}

func curFloat(el *ole.IUnknown, prop int) (float64, bool) {
	return elemPropFloat(el, elemGetCurrentPropertyVal, prop)
}

// settle re-reads until done() or stateSettle passes.
func settle(done func() bool) {
	deadline := time.Now().Add(stateSettle)
	for !done() && time.Now().Before(deadline) {
		time.Sleep(40 * time.Millisecond)
	}
}

// patternOrErr fetches pattern id or explains that the element lacks it.
func patternOrErr(el *ole.IUnknown, id int, what, hint string) (*ole.IUnknown, error) {
	if p := getPattern(el, id); p != nil {
		return p, nil
	}
	return nil, fmt.Errorf("element has no %s pattern (%s)", what, hint)
}

// stateToggle toggles until the ToggleState is on/off as wanted — a
// three-state checkbox can pass through "mixed", so up to 3 toggles.
func stateToggle(el *ole.IUnknown, want bool) (string, error) {
	p, err := patternOrErr(el, uiaTogglePatternId, "Toggle", "not a checkbox/toggle button")
	if err != nil {
		return "", err
	}
	defer release(p)
	target := int32(toggleOff)
	if want {
		target = toggleOn
	}
	before, ok := curInt(el, uiaToggleToggleStatePropertyId)
	if !ok {
		return "", fmt.Errorf("cannot read the toggle state")
	}
	cur := before
	for i := 0; i < 3 && cur != target; i++ {
		prev := cur
		if hr := vcall(p, togglePatternToggle); failed(hr) {
			return "", fmt.Errorf("Toggle failed (hr=0x%x)", uint32(hr))
		}
		settle(func() bool { s, _ := curInt(el, uiaToggleToggleStatePropertyId); cur = s; return s != prev })
	}
	if cur != target {
		return "", fmt.Errorf("toggled 3 times but the state is %s (wanted %s)", toggleStateName(cur), toggleStateName(target))
	}
	return stateChange("checked", toggleStateName(before), toggleStateName(cur)), nil
}

// stateExpand expands or collapses via ExpandCollapse.
func stateExpand(el *ole.IUnknown, want bool) (string, error) {
	p, err := patternOrErr(el, uiaExpandCollapsePatternId, "ExpandCollapse", "not a tree item/combo box/expander")
	if err != nil {
		return "", err
	}
	defer release(p)
	before, ok := curInt(el, uiaExpandCollapseStatePropertyId)
	if !ok {
		return "", fmt.Errorf("cannot read the expand/collapse state")
	}
	if before == ecLeafNode {
		return "", fmt.Errorf("element is a leaf node — nothing to expand or collapse")
	}
	done := func(s int32) bool {
		if want {
			return s == ecExpanded
		}
		return s == ecCollapsed
	}
	if done(before) {
		return stateChange("expanded", expandStateName(before), expandStateName(before)), nil
	}
	slot, verb := expandCollapseExpand, "Expand"
	if !want {
		slot, verb = expandCollapseCollapse, "Collapse"
	}
	if hr := vcall(p, slot); failed(hr) {
		return "", fmt.Errorf("%s failed (hr=0x%x)", verb, uint32(hr))
	}
	after := before
	settle(func() bool { s, _ := curInt(el, uiaExpandCollapseStatePropertyId); after = s; return done(s) })
	return stateChange("expanded", expandStateName(before), expandStateName(after)), nil
}

// stateSelect selects (Select) or deselects (RemoveFromSelection) a
// SelectionItem. Deselecting fails in single-select containers that require a
// selection — the provider's error is reported.
func stateSelect(el *ole.IUnknown, want bool) (string, error) {
	p, err := patternOrErr(el, uiaSelectionItemPatternId, "SelectionItem", "not a list/tab/tree item or radio button")
	if err != nil {
		return "", err
	}
	defer release(p)
	name := func(b bool) string {
		if b {
			return "selected"
		}
		return "not selected"
	}
	before, _ := curBool(el, uiaSelectionItemIsSelectedPropertyId)
	if before == want {
		return stateChange("selected", name(before), name(before)), nil
	}
	slot, verb := selectionItemSelect, "Select"
	if !want {
		slot, verb = selectionItemRemove, "RemoveFromSelection"
	}
	if hr := vcall(p, slot); failed(hr) {
		return "", fmt.Errorf("%s failed (hr=0x%x)", verb, uint32(hr))
	}
	after := before
	settle(func() bool { s, _ := curBool(el, uiaSelectionItemIsSelectedPropertyId); after = s; return s == want })
	return stateChange("selected", name(before), name(after)), nil
}

// stateRange sets a RangeValue (slider, spinner, progress-like control).
//
// SetValue takes a double. On windows/amd64 Go's syscall trampoline copies the
// first four integer argument registers into XMM0–XMM3 as well, so passing the
// float's bits as a uintptr lands it in XMM1 where the x64 ABI expects it.
func stateRange(el *ole.IUnknown, want float64) (string, error) {
	p, err := patternOrErr(el, uiaRangeValuePatternId, "RangeValue", "not a slider/spinner")
	if err != nil {
		return "", err
	}
	defer release(p)
	before, ok := curFloat(el, uiaRangeValueValuePropertyId)
	if !ok {
		return "", fmt.Errorf("cannot read the current value")
	}
	if sameRangeValue(before, want) {
		return stateChange("value", formatNumber(before), formatNumber(before)), nil
	}
	if ro, _ := curBool(el, uiaRangeValueIsReadOnlyPropertyId); ro {
		return "", fmt.Errorf("value is read-only (currently %s)", formatNumber(before))
	}
	lo, okLo := curFloat(el, uiaRangeValueMinimumPropertyId)
	hi, okHi := curFloat(el, uiaRangeValueMaximumPropertyId)
	if okLo && okHi && lo <= hi && (want < lo || want > hi) {
		return "", fmt.Errorf("value %s is outside the range %s–%s", formatNumber(want), formatNumber(lo), formatNumber(hi))
	}
	if hr := vcall(p, rangeValueSetValue, uintptr(math.Float64bits(want))); failed(hr) {
		return "", fmt.Errorf("RangeValue.SetValue failed (hr=0x%x)", uint32(hr))
	}
	after := before
	settle(func() bool {
		v, _ := curFloat(el, uiaRangeValueValuePropertyId)
		after = v
		return sameRangeValue(v, want)
	})
	return stateChange("value", formatNumber(before), formatNumber(after)), nil
}

// stateScrollIntoView scrolls a list/tree item into view (ScrollItem) unless
// it is already on screen.
func stateScrollIntoView(el *ole.IUnknown) (string, error) {
	off := elemInt32(el, elemGetCurrentIsOffscreen) != 0
	if !off {
		return "scroll_into_view: already on screen (no change)", nil
	}
	p, err := patternOrErr(el, uiaScrollItemPatternId, "ScrollItem", "its container cannot scroll it into view")
	if err != nil {
		return "", err
	}
	defer release(p)
	if hr := vcall(p, scrollItemScrollIntoView); failed(hr) {
		return "", fmt.Errorf("ScrollIntoView failed (hr=0x%x)", uint32(hr))
	}
	settle(func() bool { return elemInt32(el, elemGetCurrentIsOffscreen) == 0 })
	if elemInt32(el, elemGetCurrentIsOffscreen) != 0 {
		return "scroll_into_view: offscreen→still offscreen", nil
	}
	return "scroll_into_view: offscreen→on screen", nil
}

// itemControlTypes are what a popup list's entries report as; select_item's
// window-wide fallback search is limited to them.
var itemControlTypes = map[int32]bool{50007: true, 50011: true, 50024: true, 50029: true, 50019: true} // list/menu/tree/data/tab item

// stateSelectItem chooses the item named item inside container el — a combo
// box (expanded first when collapsed, collapsed again afterwards), list, tab
// control or tree. The item is searched in el's subtree, then (for popups some
// frameworks host outside the combo) among list-like items of the whole window.
func stateSelectItem(uia, root, el *ole.IUnknown, item string) (string, error) {
	current := func() string {
		if s, ok := elemValue(el); ok && s != "" {
			return s
		}
		return selectedItemName(uia, el)
	}
	before := current()

	expandedByUs := false
	var ec *ole.IUnknown
	if p := getPattern(el, uiaExpandCollapsePatternId); p != nil {
		ec = p
		defer release(ec)
		if s, _ := curInt(el, uiaExpandCollapseStatePropertyId); s == ecCollapsed {
			if hr := vcall(ec, expandCollapseExpand); !failed(hr) {
				expandedByUs = true
			}
		}
	}
	collapse := func() {
		if expandedByUs {
			if s, _ := curInt(el, uiaExpandCollapseStatePropertyId); s != ecCollapsed {
				vcall(ec, expandCollapseCollapse)
			}
		}
	}

	var target *ole.IUnknown
	var names []string
	settle(func() bool {
		target, names = findItem(uia, el, item, nil)
		return target != nil
	})
	if target == nil && root != nil {
		target, _ = findItem(uia, root, item, itemControlTypes)
	}
	if target == nil {
		collapse()
		hint := ""
		if h := closestNames(item, names, 5); len(h) > 0 {
			hint = "; closest: " + quoteJoin(h)
		} else if len(names) > 0 {
			if len(names) > 8 {
				names = names[:8]
			}
			hint = "; items: " + quoteJoin(names)
		}
		return "", fmt.Errorf("no item %q in this container%s", item, hint)
	}
	defer release(target)

	itemName := elemString(target, elemGetCurrentName)
	if sel, _ := curBool(target, uiaSelectionItemIsSelectedPropertyId); sel {
		collapse()
		return fmt.Sprintf("select_item: %q already selected (no change)", itemName), nil
	}
	if p := getPattern(target, uiaSelectionItemPatternId); p != nil {
		hr := vcall(p, selectionItemSelect)
		release(p)
		if failed(hr) {
			collapse()
			return "", fmt.Errorf("Select on item %q failed (hr=0x%x)", itemName, uint32(hr))
		}
	} else if err := activateElement(target, itemName); err != nil {
		collapse()
		return "", err
	}
	collapse()
	after := before
	settle(func() bool { after = current(); return after != before })
	if after == "" {
		after = itemName
	}
	return fmt.Sprintf("select_item: %q→%q", before, after), nil
}

// findItem looks for item among the SelectionItem-capable (or, with types,
// those control types') elements under container; names lists what was there,
// for the not-found hint. Caller must release the element.
func findItem(uia, container *ole.IUnknown, item string, types map[int32]bool) (*ole.IUnknown, []string) {
	arr, n, err := findAllCached(uia, container, 0, []int{uiaNamePropertyId, uiaControlTypePropertyId, uiaIsSelectionItemAvailablePropertyId})
	if err != nil {
		return nil, nil
	}
	defer release(arr)
	var els []*ole.IUnknown
	var names []string
	for i := int32(0); i < n; i++ {
		var e *ole.IUnknown
		if hr := vcall(arr, arrGetElement, uintptr(i), uintptr(unsafe.Pointer(&e))); failed(hr) || e == nil {
			continue
		}
		ok := false
		if types != nil {
			ok = types[elemInt32(e, elemGetCachedControlType)]
		} else {
			ok, _ = elemPropBool(e, elemGetCachedPropertyVal, uiaIsSelectionItemAvailablePropertyId)
		}
		if !ok {
			release(e)
			continue
		}
		els = append(els, e)
		names = append(names, elemString(e, elemGetCachedName))
	}
	idx := pickItemIndex(names, item)
	for i, e := range els {
		if i != idx {
			release(e)
		}
	}
	if idx < 0 {
		return nil, names
	}
	return els[idx], names
}

// selectedItemName is the name of the selected item under container ("" if none).
func selectedItemName(uia, container *ole.IUnknown) string {
	arr, n, err := findAllCached(uia, container, 0, []int{uiaNamePropertyId, uiaIsSelectionItemAvailablePropertyId, uiaSelectionItemIsSelectedPropertyId})
	if err != nil {
		return ""
	}
	defer release(arr)
	for i := int32(0); i < n; i++ {
		var e *ole.IUnknown
		if hr := vcall(arr, arrGetElement, uintptr(i), uintptr(unsafe.Pointer(&e))); failed(hr) || e == nil {
			continue
		}
		avail, _ := elemPropBool(e, elemGetCachedPropertyVal, uiaIsSelectionItemAvailablePropertyId)
		sel, _ := elemPropBool(e, elemGetCachedPropertyVal, uiaSelectionItemIsSelectedPropertyId)
		name := elemString(e, elemGetCachedName)
		release(e)
		if avail && sel {
			return name
		}
	}
	return ""
}

func quoteJoin(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(q, ", ")
}
