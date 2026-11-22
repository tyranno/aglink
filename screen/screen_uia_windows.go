//go:build windows

package main

import (
	"fmt"
	"math"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	ole "github.com/go-ole/go-ole"
	"golang.org/x/sys/windows"
)

// Design Ref: §2 (snapshot / invoke / set_value), §4 (screen_uia_windows.go).
//
// CGO-free Windows UI Automation (UIA) access for the "screen" MCP server.
//
// We talk to IUIAutomation (a pure COM / IUnknown-based interface, NOT IDispatch)
// by calling its vtable methods directly via syscall on the interface pointers.
// go-ole is used only for COM init, CoCreateInstance, and BSTR helpers.
//
// All COM calls must run on a single, OS-locked STA thread. We dedicate one
// goroutine (uiaWorker) that initializes COM once and serves every UIA request
// over a channel, so the MCP handlers (which run on arbitrary goroutines) never
// touch COM directly.

// ---- Well-known UIA GUIDs / IDs (from UIAutomationClient.h) ----

// CLSID_CUIAutomation {ff48dba4-60ef-4201-aa87-54103eef594e}
var clsidCUIAutomation = ole.NewGUID("{ff48dba4-60ef-4201-aa87-54103eef594e}")

// IID_IUIAutomation {30cbe57d-d9d0-452a-ab13-7ac5ac4825ee}
var iidIUIAutomation = ole.NewGUID("{30cbe57d-d9d0-452a-ab13-7ac5ac4825ee}")

const (
	// Property IDs.
	uiaBoundingRectanglePropertyId = 30001
	uiaControlTypePropertyId       = 30003
	uiaNamePropertyId              = 30005
	uiaIsEnabledPropertyId         = 30010
	uiaAutomationIdPropertyId      = 30011

	// Is<Pattern>PatternAvailable property IDs (VT_BOOL) — read from a cache
	// instead of a GetCurrentPattern round trip per pattern per element.
	uiaIsExpandCollapseAvailablePropertyId = 30028
	uiaIsInvokeAvailablePropertyId         = 30031
	uiaIsRangeValueAvailablePropertyId     = 30033
	uiaIsSelectionItemAvailablePropertyId  = 30036
	uiaIsTextAvailablePropertyId           = 30040
	uiaIsToggleAvailablePropertyId         = 30041
	uiaIsValueAvailablePropertyId          = 30043

	// Pattern state property IDs.
	uiaValueValuePropertyId              = 30045 // VT_BSTR
	uiaRangeValueValuePropertyId         = 30047 // VT_R8
	uiaRangeValueIsReadOnlyPropertyId    = 30048 // VT_BOOL
	uiaRangeValueMinimumPropertyId       = 30049 // VT_R8
	uiaRangeValueMaximumPropertyId       = 30050 // VT_R8
	uiaExpandCollapseStatePropertyId     = 30070 // VT_I4 ExpandCollapseState
	uiaSelectionItemIsSelectedPropertyId = 30079 // VT_BOOL
	uiaToggleToggleStatePropertyId       = 30086 // VT_I4 ToggleState

	// Control pattern IDs.
	uiaInvokePatternId         = 10000
	uiaSelectionPatternId      = 10001
	uiaValuePatternId          = 10002
	uiaRangeValuePatternId     = 10003
	uiaTextPatternId           = 10014
	uiaTogglePatternId         = 10015
	uiaSelectionItemPatternId  = 10010
	uiaExpandCollapsePatternId = 10005
	uiaScrollItemPatternId     = 10017

	// TreeScope flags.
	treeScopeElement     = 1
	treeScopeChildren    = 2
	treeScopeDescendants = 4
	treeScopeSubtree     = 7
)

// ---- IUIAutomation vtable slot indices (incl. IUnknown 0,1,2) ----
const (
	uiaGetRootElement          = 5  // GetRootElement(out **IUIAutomationElement)
	uiaElementFromHandle       = 6  // ElementFromHandle(HWND, out **IUIAutomationElement)
	uiaElementFromPoint        = 7  // ElementFromPoint(POINT, out **IUIAutomationElement)
	uiaGetFocusedElement       = 8  // GetFocusedElement(out **IUIAutomationElement)
	uiaCreateCacheRequest      = 20 // CreateCacheRequest(out **IUIAutomationCacheRequest)
	uiaCreateTrueCondition     = 21
	uiaCreatePropertyCondition = 23 // CreatePropertyCondition(PROPERTYID, VARIANT, out **IUIAutomationCondition)
)

// ---- IUIAutomationCacheRequest vtable slot indices (incl. IUnknown 0,1,2) ----
// UIAutomationClient.h order: AddProperty, AddPattern, Clone, get_TreeScope,
// put_TreeScope, get_TreeFilter, put_TreeFilter, get/put_AutomationElementMode.
const (
	cacheAddProperty   = 3 // AddProperty(PROPERTYID)
	cachePutTreeScope  = 7 // put_TreeScope(TreeScope)
	cachePutTreeFilter = 9 // put_TreeFilter(*IUIAutomationCondition)
)

// ---- IUIAutomationElement vtable slot indices (incl. IUnknown 0,1,2) ----
const (
	elemSetFocus               = 3
	elemFindFirst              = 5  // FindFirst(scope, *cond, out **elem)
	elemFindAll                = 6  // FindAll(scope, *cond, out **IUIAutomationElementArray)
	elemFindAllBuildCache      = 8  // FindAllBuildCache(scope, *cond, *cacheRequest, out **IUIAutomationElementArray)
	elemBuildUpdatedCache      = 9  // BuildUpdatedCache(*cacheRequest, out **elem)
	elemGetCurrentPropertyVal  = 10 // GetCurrentPropertyValue(PROPERTYID, out *VARIANT)
	elemGetCachedPropertyVal   = 12 // GetCachedPropertyValue(PROPERTYID, out *VARIANT)
	elemGetCurrentPattern      = 16 // GetCurrentPattern(patternId, out **IUnknown)
	elemGetCachedChildren      = 19 // GetCachedChildren(out **IUIAutomationElementArray)
	elemGetCurrentControlType  = 21 // -> *int32 (CONTROLTYPEID)
	elemGetCurrentName         = 23 // -> *BSTR
	elemGetCurrentIsEnabled    = 28 // -> *BOOL(int32)
	elemGetCurrentAutomationId = 29 // -> *BSTR
	elemGetCurrentIsPassword   = 35 // -> *BOOL(int32)
	elemGetCurrentBoundingRect = 43 // -> *RECT

	// The get_Cached* getters (slots 52–83) mirror the 32 get_Current* ones
	// (20–51) in the same order, so each sits exactly 32 slots later.
	elemGetCachedControlType  = elemGetCurrentControlType + 32  // 53
	elemGetCachedName         = elemGetCurrentName + 32         // 55
	elemGetCachedIsEnabled    = elemGetCurrentIsEnabled + 32    // 60
	elemGetCachedAutomationId = elemGetCurrentAutomationId + 32 // 61
	elemGetCachedBoundingRect = elemGetCurrentBoundingRect + 32 // 75
)

// ---- IUIAutomationElementArray vtable slot indices ----
const (
	arrGetLength  = 3 // get_Length(out *int32)
	arrGetElement = 4 // GetElement(int32 index, out **elem)
)

// ---- Pattern vtable slots (all inherit IUnknown 0,1,2) ----
const (
	invokePatternInvoke      = 3 // Invoke()
	valuePatternSetValue     = 3 // SetValue(BSTR)
	valuePatternCurrentValue = 4 // get_CurrentValue(BSTR*) — verified against
	// Microsoft Learn's IUIAutomationValuePattern vtable order: SetValue,
	// get_CurrentValue, get_CurrentIsReadOnly, get_CachedValue, get_CachedIsReadOnly.
	togglePatternToggle  = 3 // Toggle()
	selectionItemSelect  = 3 // Select()
	expandCollapseExpand = 3 // Expand()

	// set_state. Pattern STATE is read through the property IDs above
	// (GetCurrentPropertyValue), so only each pattern's leading action methods
	// are called — the slots least likely to be miscounted.
	expandCollapseCollapse   = 4 // Collapse()  (Expand, Collapse, get_CurrentExpandCollapseState, …)
	selectionItemRemove      = 5 // RemoveFromSelection()  (Select, AddToSelection, RemoveFromSelection, …)
	rangeValueSetValue       = 3 // SetValue(double)
	scrollItemScrollIntoView = 3 // ScrollIntoView()

	// IUIAutomationTextPattern: get_DocumentRange returns a TextRange spanning the
	// whole control; IUIAutomationTextRange.GetText(maxLength, *BSTR) reads it.
	// This is how Documents / read-only text areas / editors expose their content
	// (they have no Value pattern), so it's the read path for "what does this say".
	textPatternGetDocumentRange = 7  // get_DocumentRange(out **IUIAutomationTextRange)
	textRangeGetText            = 12 // GetText(int maxLength, out *BSTR)
)

// controlTypeName maps a UIA control-type id to a short readable label.
func controlTypeName(id int32) string {
	switch id {
	case 50000:
		return "button"
	case 50001:
		return "calendar"
	case 50002:
		return "checkbox"
	case 50003:
		return "combobox"
	case 50004:
		return "edit"
	case 50005:
		return "hyperlink"
	case 50006:
		return "image"
	case 50007:
		return "list item"
	case 50008:
		return "list"
	case 50009:
		return "menu"
	case 50010:
		return "menu bar"
	case 50011:
		return "menu item"
	case 50012:
		return "progress bar"
	case 50013:
		return "radio button"
	case 50014:
		return "scroll bar"
	case 50015:
		return "slider"
	case 50016:
		return "spinner"
	case 50017:
		return "status bar"
	case 50018:
		return "tab"
	case 50019:
		return "tab item"
	case 50020:
		return "text"
	case 50021:
		return "tool bar"
	case 50022:
		return "tooltip"
	case 50023:
		return "tree"
	case 50024:
		return "tree item"
	case 50025:
		return "custom"
	case 50026:
		return "group"
	case 50027:
		return "thumb"
	case 50028:
		return "data grid"
	case 50029:
		return "data item"
	case 50030:
		return "document"
	case 50031:
		return "split button"
	case 50032:
		return "window"
	case 50033:
		return "pane"
	case 50034:
		return "header"
	case 50035:
		return "header item"
	case 50036:
		return "table"
	case 50037:
		return "title bar"
	case 50038:
		return "separator"
	case 50039:
		return "semantic zoom"
	case 50040:
		return "app bar"
	default:
		return fmt.Sprintf("type%d", id)
	}
}

// ---- Low-level vtable call helper ----

// vcall invokes vtable slot `slot` of the COM object `this` with the given
// uintptr args and returns the HRESULT.
func vcall(this *ole.IUnknown, slot int, args ...uintptr) uintptr {
	if this == nil {
		return uintptr(0x80004003) // E_POINTER
	}
	// this.RawVTable points at the start of the vtable (array of fn pointers).
	// View it as a large fixed array and index the requested slot. We go
	// straight from the RawVTable pointer (no uintptr round-trip) so the
	// vet "misuse of unsafe.Pointer" heuristic stays quiet.
	vtbl := (*[256]uintptr)(unsafe.Pointer(this.RawVTable))
	fn := vtbl[slot]
	all := append([]uintptr{uintptr(unsafe.Pointer(this))}, args...)
	ret, _, _ := syscall.SyscallN(fn, all...)
	return ret
}

func failed(hr uintptr) bool { return int32(hr) < 0 }

// addRefRelease helpers via IUnknown vtable.
func release(u *ole.IUnknown) {
	if u != nil {
		vcall(u, 2)
	}
}

// ---- UIA worker goroutine (single STA thread) ----

type uiaRequest struct {
	fn    func(*ole.IUnknown) (string, error) // receives the live IUIAutomation
	reply chan uiaResult
}

type uiaResult struct {
	text string
	err  error
}

var (
	uiaOnce    sync.Once
	uiaReqCh   chan uiaRequest
	uiaInitErr error
)

// startUIAWorker spins up the dedicated STA COM thread once.
func startUIAWorker() {
	uiaReqCh = make(chan uiaRequest)
	ready := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		// COINIT_APARTMENTTHREADED == 0x2.
		if err := ole.CoInitializeEx(0, 0x2); err != nil {
			uiaInitErr = fmt.Errorf("CoInitializeEx: %w", err)
			close(ready)
			return
		}
		defer ole.CoUninitialize()

		unk, err := ole.CreateInstance(clsidCUIAutomation, iidIUIAutomation)
		if err != nil {
			uiaInitErr = fmt.Errorf("create CUIAutomation: %w", err)
			close(ready)
			return
		}
		defer release(unk)
		close(ready)

		for req := range uiaReqCh {
			text, e := req.fn(unk)
			req.reply <- uiaResult{text: text, err: e}
		}
	}()
	<-ready
}

// uiaDo runs fn on the UIA STA thread and returns its result.
func uiaDo(fn func(*ole.IUnknown) (string, error)) (string, error) {
	uiaOnce.Do(startUIAWorker)
	if uiaInitErr != nil {
		return "", uiaInitErr
	}
	reply := make(chan uiaResult, 1)
	uiaReqCh <- uiaRequest{fn: fn, reply: reply}
	r := <-reply
	return r.text, r.err
}

// ---- Element helpers (run on the STA thread) ----

// foregroundElement returns the IUIAutomationElement for the foreground window,
// or falls back to the root (desktop) element if there is no foreground window.
func foregroundElement(uia *ole.IUnknown) (*ole.IUnknown, error) {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd != 0 {
		var elem *ole.IUnknown
		hr := vcall(uia, uiaElementFromHandle, hwnd, uintptr(unsafe.Pointer(&elem)))
		if !failed(hr) && elem != nil {
			return elem, nil
		}
	}
	var root *ole.IUnknown
	hr := vcall(uia, uiaGetRootElement, uintptr(unsafe.Pointer(&root)))
	if failed(hr) || root == nil {
		return nil, fmt.Errorf("UIA: no foreground window and GetRootElement failed (hr=0x%x)", uint32(hr))
	}
	return root, nil
}

// uiaRoot returns the element searches start from: the given top-level window
// when hwnd != 0 (background use — the window need not be foreground), else the
// foreground window as before. Caller must release the result.
func uiaRoot(uia *ole.IUnknown, hwnd uintptr) (*ole.IUnknown, error) {
	if hwnd == 0 {
		return foregroundElement(uia)
	}
	var elem *ole.IUnknown
	hr := vcall(uia, uiaElementFromHandle, hwnd, uintptr(unsafe.Pointer(&elem)))
	if failed(hr) || elem == nil {
		return nil, fmt.Errorf("UIA: ElementFromHandle(0x%x) failed (hr=0x%x)", hwnd, uint32(hr))
	}
	return elem, nil
}

// elemCenter returns the centre of el's bounding rectangle, false when it has
// none (offscreen, or a minimized window's placeholder coordinates).
func elemCenter(el *ole.IUnknown) (int, int, bool) {
	var rc rect
	if hr := vcall(el, elemGetCurrentBoundingRect, uintptr(unsafe.Pointer(&rc))); failed(hr) {
		return 0, 0, false
	}
	if rc.Right <= rc.Left || rc.Bottom <= rc.Top || rc.Left <= -32000 {
		return 0, 0, false
	}
	return int(rc.Left+rc.Right) / 2, int(rc.Top+rc.Bottom) / 2, true
}

// packPoint packs a POINT{LONG x; LONG y} into the single 64-bit register it
// is passed in by value on amd64: x in the low 32 bits, y in the high 32.
// Extracted so the packing — the one non-obvious bit, and the one that must
// survive negative coordinates (monitors left of / above the primary sit at
// negative virtual-screen coords) — is unit-testable without a live COM call.
// uint32(int32) round-trips the sign so Windows reads the LONG back correctly.
func packPoint(x, y int32) uintptr {
	return uintptr(uint32(x)) | uintptr(uint32(y))<<32
}

// elementFromPoint returns the UIA element at absolute screen coordinates
// (x,y), the bridge between "I saw something at this pixel in a screenshot"
// and an actual accessibility element. Caller must release the result.
func elementFromPoint(uia *ole.IUnknown, x, y int32) (*ole.IUnknown, error) {
	var elem *ole.IUnknown
	hr := vcall(uia, uiaElementFromPoint, packPoint(x, y), uintptr(unsafe.Pointer(&elem)))
	if failed(hr) || elem == nil {
		return nil, fmt.Errorf("UIA: ElementFromPoint(%d,%d) failed (hr=0x%x) — point may be off-screen or over no element", x, y, uint32(hr))
	}
	return elem, nil
}

// elemString reads a BSTR-returning property (Name / AutomationId).
func elemString(elem *ole.IUnknown, slot int) string {
	var bstr *uint16
	hr := vcall(elem, slot, uintptr(unsafe.Pointer(&bstr)))
	if failed(hr) || bstr == nil {
		return ""
	}
	s := ole.BstrToString(bstr)
	ole.SysFreeString((*int16)(unsafe.Pointer(bstr)))
	return s
}

// elemInt32 reads an int32-returning property (ControlType, IsEnabled).
func elemInt32(elem *ole.IUnknown, slot int) int32 {
	var v int32
	vcall(elem, slot, uintptr(unsafe.Pointer(&v)))
	return v
}

// elemSupportsPattern returns true if the element exposes the given pattern.
// The returned *IUnknown (if any) is released; callers that need the pattern
// should use getPattern instead.
func elemSupportsPattern(elem *ole.IUnknown, patternId int) bool {
	p := getPattern(elem, patternId)
	if p != nil {
		release(p)
		return true
	}
	return false
}

// getPattern returns the pattern interface for patternId, or nil if unsupported.
func getPattern(elem *ole.IUnknown, patternId int) *ole.IUnknown {
	var pat *ole.IUnknown
	hr := vcall(elem, elemGetCurrentPattern, uintptr(patternId), uintptr(unsafe.Pointer(&pat)))
	if failed(hr) || pat == nil {
		return nil
	}
	return pat
}

// ---- VARIANT property reads ----

// VARIANT type tags read back from GetCurrent/CachedPropertyValue.
const (
	vtI4   = 3
	vtR8   = 5
	vtBSTR = 8
	vtBool = 11
)

// elemPropVariant reads property propId through slot (elemGetCurrentPropertyVal
// or elemGetCachedPropertyVal). The caller must ole.VariantClear the result.
// A property the provider does not support comes back as VT_EMPTY or UIA's
// "not supported" VT_UNKNOWN sentinel, which the typed readers below reject.
func elemPropVariant(el *ole.IUnknown, slot, propId int) (ole.VARIANT, bool) {
	var v ole.VARIANT
	ole.VariantInit(&v)
	if hr := vcall(el, slot, uintptr(propId), uintptr(unsafe.Pointer(&v))); failed(hr) {
		return v, false
	}
	return v, true
}

// elemPropBool reads a VT_BOOL property; ok=false when absent/unsupported.
func elemPropBool(el *ole.IUnknown, slot, propId int) (val, ok bool) {
	v, got := elemPropVariant(el, slot, propId)
	defer ole.VariantClear(&v)
	if !got || v.VT != vtBool {
		return false, false
	}
	return int16(v.Val) != 0, true // VARIANT_BOOL in the low 16 bits; TRUE = -1
}

// elemPropInt reads a VT_I4 property (enums such as ToggleState).
func elemPropInt(el *ole.IUnknown, slot, propId int) (int32, bool) {
	v, got := elemPropVariant(el, slot, propId)
	defer ole.VariantClear(&v)
	if !got || v.VT != vtI4 {
		return 0, false
	}
	return int32(v.Val), true
}

// elemPropFloat reads a VT_R8 property (RangeValue value/min/max).
func elemPropFloat(el *ole.IUnknown, slot, propId int) (float64, bool) {
	v, got := elemPropVariant(el, slot, propId)
	defer ole.VariantClear(&v)
	if !got || v.VT != vtR8 {
		return 0, false
	}
	return math.Float64frombits(uint64(v.Val)), true
}

// elemPropString reads a VT_BSTR property (ValueValue).
func elemPropString(el *ole.IUnknown, slot, propId int) (string, bool) {
	v, got := elemPropVariant(el, slot, propId)
	defer ole.VariantClear(&v)
	if !got || v.VT != vtBSTR {
		return "", false
	}
	bstr := *(**uint16)(unsafe.Pointer(&v.Val))
	if bstr == nil {
		return "", true
	}
	return ole.BstrToString(bstr), true // VariantClear frees the BSTR
}

// ---- cache requests ----

// newCacheRequest creates an IUIAutomationCacheRequest for props with the given
// TreeScope. The tree filter is TrueCondition (the raw view, same elements a
// FindAll(TrueCondition) walk sees) instead of the default control view.
// Caller must release the result.
func newCacheRequest(uia *ole.IUnknown, props []int, scope int) (*ole.IUnknown, error) {
	var cr *ole.IUnknown
	if hr := vcall(uia, uiaCreateCacheRequest, uintptr(unsafe.Pointer(&cr))); failed(hr) || cr == nil {
		return nil, fmt.Errorf("UIA: CreateCacheRequest failed (hr=0x%x)", uint32(hr))
	}
	for _, p := range props {
		if hr := vcall(cr, cacheAddProperty, uintptr(p)); failed(hr) {
			release(cr)
			return nil, fmt.Errorf("UIA: CacheRequest.AddProperty(%d) failed (hr=0x%x)", p, uint32(hr))
		}
	}
	if scope != treeScopeElement {
		if hr := vcall(cr, cachePutTreeScope, uintptr(scope)); failed(hr) {
			release(cr)
			return nil, fmt.Errorf("UIA: CacheRequest.put_TreeScope failed (hr=0x%x)", uint32(hr))
		}
	}
	var cond *ole.IUnknown
	if hr := vcall(uia, uiaCreateTrueCondition, uintptr(unsafe.Pointer(&cond))); !failed(hr) && cond != nil {
		vcall(cr, cachePutTreeFilter, uintptr(unsafe.Pointer(cond)))
		release(cond)
	}
	return cr, nil
}

// nodeCacheProps is everything readNode needs, so a cached read is one cross-
// process call for the whole tree instead of ~10 per element.
var nodeCacheProps = []int{
	uiaNamePropertyId, uiaControlTypePropertyId, uiaAutomationIdPropertyId,
	uiaIsEnabledPropertyId, uiaBoundingRectanglePropertyId,
	uiaIsInvokeAvailablePropertyId, uiaIsValueAvailablePropertyId, uiaIsTextAvailablePropertyId,
	uiaIsToggleAvailablePropertyId, uiaIsExpandCollapseAvailablePropertyId,
	uiaIsSelectionItemAvailablePropertyId, uiaIsRangeValueAvailablePropertyId,
	uiaValueValuePropertyId, uiaToggleToggleStatePropertyId, uiaExpandCollapseStatePropertyId,
	uiaSelectionItemIsSelectedPropertyId, uiaRangeValueValuePropertyId,
}

// ---- snapshot ----

type uiaNode struct {
	name       string
	ctrlType   int32
	autoId     string
	enabled    bool
	invoke     bool
	value      bool
	text       bool   // exposes the Text pattern (Document/read-only text area)
	valueText  string // ValueValue, when value
	preview    string // short inlined content preview (see snapshotPreviewChars)
	depth      int    // containment depth among SHOWN elements (indentation)
	rect       rect   // screen bounding rectangle
	toggle     int32  // ToggleState, stateNone when no Toggle pattern
	expand     int32  // ExpandCollapseState, stateNone when no ExpandCollapse pattern
	selectable bool   // SelectionItem pattern
	selected   bool
	hasRange   bool // RangeValue pattern with a readable value
	rangeValue float64
}

// readNode reads an element's snapshot fields — from its cache (cached=true;
// the element must come from a request built with nodeCacheProps) or live.
func readNode(el *ole.IUnknown, cached bool) uiaNode {
	nameSlot, typeSlot, idSlot := elemGetCurrentName, elemGetCurrentControlType, elemGetCurrentAutomationId
	enSlot, rectSlot, propSlot := elemGetCurrentIsEnabled, elemGetCurrentBoundingRect, elemGetCurrentPropertyVal
	if cached {
		nameSlot, typeSlot, idSlot = elemGetCachedName, elemGetCachedControlType, elemGetCachedAutomationId
		enSlot, rectSlot, propSlot = elemGetCachedIsEnabled, elemGetCachedBoundingRect, elemGetCachedPropertyVal
	}
	nd := uiaNode{
		name:     elemString(el, nameSlot),
		ctrlType: elemInt32(el, typeSlot),
		autoId:   elemString(el, idSlot),
		enabled:  elemInt32(el, enSlot) != 0,
		toggle:   stateNone,
		expand:   stateNone,
	}
	vcall(el, rectSlot, uintptr(unsafe.Pointer(&nd.rect)))
	has := func(id int) bool { v, _ := elemPropBool(el, propSlot, id); return v }
	nd.invoke = has(uiaIsInvokeAvailablePropertyId)
	nd.value = has(uiaIsValueAvailablePropertyId)
	nd.text = has(uiaIsTextAvailablePropertyId)
	if has(uiaIsToggleAvailablePropertyId) {
		if s, ok := elemPropInt(el, propSlot, uiaToggleToggleStatePropertyId); ok {
			nd.toggle = s
		}
	}
	if has(uiaIsExpandCollapseAvailablePropertyId) {
		if s, ok := elemPropInt(el, propSlot, uiaExpandCollapseStatePropertyId); ok {
			nd.expand = s
		}
	}
	if has(uiaIsSelectionItemAvailablePropertyId) {
		nd.selectable = true
		nd.selected, _ = elemPropBool(el, propSlot, uiaSelectionItemIsSelectedPropertyId)
	}
	if has(uiaIsRangeValueAvailablePropertyId) {
		nd.rangeValue, nd.hasRange = elemPropFloat(el, propSlot, uiaRangeValueValuePropertyId)
	}
	if nd.value {
		nd.valueText, _ = elemPropString(el, propSlot, uiaValueValuePropertyId)
	}
	return nd
}

// anonymous reports a wholly anonymous, non-interactive, contentless node —
// skipped from snapshot output to save tokens (its children still show).
func (nd uiaNode) anonymous() bool {
	return strings.TrimSpace(nd.name) == "" && nd.autoId == "" && !nd.invoke && !nd.value && !nd.text &&
		nd.toggle == stateNone && !nd.hasRange
}

// snapshotPreviewChars bounds the per-element content preview inlined into a
// snapshot line. Short on purpose: enough to see WHAT a field/editor holds
// without a screenshot or a get_value round-trip, but small so it can't bloat the
// tree — the full content is one get_value/get_text call away.
const snapshotPreviewChars = 120

// onelinePreview flattens a value/text preview to a single trimmed line capped at
// max runes (with an ellipsis), so an inlined snapshot preview stays one grep-able
// line no matter how the source wraps.
func onelinePreview(s string, max int) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.Join(strings.Fields(s), " ") // collapse runs of whitespace
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// uiaSnapshotSparseThreshold is the element count below which uiaSnapshot
// suspects the target hasn't finished building its accessibility tree yet and
// retries once. Chromium/Electron apps (VS Code, Chrome) often expose an empty
// or near-empty tree on the very first UIA query after gaining focus — the
// query itself seems to be what triggers them to start building it — and a
// second query a moment later sees the real tree. Native Win32/WPF apps return
// hundreds of elements immediately, so this path essentially never fires for
// them and costs nothing.
const uiaSnapshotSparseThreshold = 10

// uiaSnapshotRetryDelay is how long uiaSnapshot waits before its one retry.
var uiaSnapshotRetryDelay = 250 * time.Millisecond

// snapshotFlattenCap bounds how many elements one snapshot holds references
// to; past it the tail is simply not listed (total says so).
const snapshotFlattenCap = 100000

// snapEl is one element of the flattened (document-order) tree; parent is the
// index of its parent in the same slice, -1 for the root / a flat list.
type snapEl struct {
	el     *ole.IUnknown
	parent int
}

// cachedTree fetches root's whole subtree with nodeCacheProps in ONE
// cross-process call (BuildUpdatedCache, TreeScope_Subtree) and flattens it
// depth-first, so the result is document order with parent links. hier=false
// means that failed and the result is FindAllBuildCache's flat list (same
// elements and order, no parents). Caller must release every el.
func cachedTree(uia, root *ole.IUnknown) (els []snapEl, hier bool, err error) {
	if cr, cerr := newCacheRequest(uia, nodeCacheProps, treeScopeSubtree); cerr == nil {
		var croot *ole.IUnknown
		hr := vcall(root, elemBuildUpdatedCache, uintptr(unsafe.Pointer(cr)), uintptr(unsafe.Pointer(&croot)))
		release(cr)
		if !failed(hr) && croot != nil {
			var walk func(el *ole.IUnknown, parent int)
			walk = func(el *ole.IUnknown, parent int) {
				idx := len(els)
				els = append(els, snapEl{el, parent})
				var arr *ole.IUnknown
				if hr := vcall(el, elemGetCachedChildren, uintptr(unsafe.Pointer(&arr))); failed(hr) || arr == nil {
					return // no children (GetCachedChildren gives NULL for a leaf)
				}
				defer release(arr)
				var n int32
				vcall(arr, arrGetLength, uintptr(unsafe.Pointer(&n)))
				for i := int32(0); i < n && len(els) < snapshotFlattenCap; i++ {
					var c *ole.IUnknown
					if hr := vcall(arr, arrGetElement, uintptr(i), uintptr(unsafe.Pointer(&c))); failed(hr) || c == nil {
						continue
					}
					walk(c, idx)
				}
			}
			walk(croot, -1)
			return els, true, nil
		}
	}
	// Fallback: the same raw-view elements as a flat cached list.
	arr, n, ferr := findAllCached(uia, root, 0, nodeCacheProps)
	if ferr != nil {
		return nil, false, ferr
	}
	defer release(arr)
	for i := int32(0); i < n && len(els) < snapshotFlattenCap; i++ {
		var c *ole.IUnknown
		if hr := vcall(arr, arrGetElement, uintptr(i), uintptr(unsafe.Pointer(&c))); failed(hr) || c == nil {
			continue
		}
		els = append(els, snapEl{c, -1})
	}
	return els, false, nil
}

func releaseSnapEls(els []snapEl) {
	for _, e := range els {
		release(e.el)
	}
}

// uiaSnapshot walks the foreground window's element subtree in document order
// starting at raw element index offset and returns a compact textual listing of
// up to maxElems shown elements / maxChars characters, ending with a
// continuation note (offset=N) when the tree was not exhausted. Lines are
// indented by containment (depth among shown elements) and carry state flags
// and the screen rectangle.
func uiaSnapshot(offset, maxElems, maxChars int) (string, error) {
	return uiaSnapshotIn(0, offset, maxElems, maxChars)
}

// uiaSnapshotIn is uiaSnapshot rooted at window hwnd (0 = foreground window).
func uiaSnapshotIn(hwnd uintptr, offset, maxElems, maxChars int) (string, error) {
	maxElems = clampLimit(maxElems, defaultSnapshotMaxElements, ceilingSnapshotMaxElements)
	maxChars = clampLimit(maxChars, defaultSnapshotMaxChars, ceilingSnapshotMaxChars)
	return uiaDo(func(uia *ole.IUnknown) (string, error) {
		root, err := uiaRoot(uia, hwnd)
		if err != nil {
			return "", err
		}
		defer release(root)

		els, hier, err := cachedTree(uia, root)
		if err != nil {
			return "", err
		}
		if len(els) < uiaSnapshotSparseThreshold {
			time.Sleep(uiaSnapshotRetryDelay)
			if els2, hier2, err2 := cachedTree(uia, root); err2 == nil && len(els2) > len(els) {
				releaseSnapEls(els)
				els, hier = els2, hier2
			} else {
				releaseSnapEls(els2) // retry didn't help; keep the first (possibly still-empty) result
			}
		}
		defer func() { releaseSnapEls(els) }()

		total := len(els)
		start := offset
		if start < 0 {
			start = 0
		}
		if start > total {
			start = total
		}
		// Bound one page's walk too: anonymous/contentless elements are skipped
		// from the output, so a page scans at most this many raw elements.
		scanLimit := maxElems * snapshotScanFactor

		// shown/depth for every element up to the page end: a line's indentation
		// counts its SHOWN ancestors, so skipped anonymous panes add none.
		shown := make([]bool, 0, total)
		depth := make([]int, 0, total)
		place := func(i int, nd uiaNode) {
			d := 0
			if p := els[i].parent; p >= 0 {
				d = depth[p]
				if shown[p] {
					d++
				}
			}
			shown = append(shown, !nd.anonymous())
			depth = append(depth, d)
		}
		for i := 0; i < start; i++ {
			place(i, readNode(els[i].el, true))
		}

		var lines []string
		chars := 0
		next := start
		reason := ""
		for i := start; i < total; i++ {
			if len(lines) >= maxElems {
				reason = fmt.Sprintf("max_elements=%d", maxElems)
				break
			}
			if i-start >= scanLimit {
				reason = "scan limit"
				break
			}
			nd := readNode(els[i].el, true)
			place(i, nd)
			if !shown[i] {
				next = i + 1
				continue
			}
			nd.depth = depth[i]
			// Inline a short content preview so one snapshot shows structure AND
			// content — the model can read a form's field values / an editor's text
			// without a screenshot or a get_value per field. Value first (edit/combo,
			// already cached), then Text (documents/read-only areas; one live call,
			// only for shown elements). Bounded hard; full content is a
			// get_value/get_text away.
			preview := nd.valueText
			if preview == "" && nd.text {
				if s, ok := elemText(els[i].el, snapshotPreviewChars*2); ok {
					preview = s
				}
			}
			nd.preview = onelinePreview(preview, snapshotPreviewChars)
			line := snapshotLine(nd)
			// Character budget: stop BEFORE the line that would overflow it (but
			// always emit at least one line so a page makes progress).
			if len(lines) > 0 && chars+len([]rune(line))+1 > maxChars {
				reason = fmt.Sprintf("max_chars=%d", maxChars)
				break
			}
			lines = append(lines, line)
			chars += len([]rune(line)) + 1
			next = i + 1
		}

		var b strings.Builder
		layout := "indented by containment"
		if !hier {
			layout = "flat — hierarchy unavailable"
		}
		fmt.Fprintf(&b, "UIA elements of %s (%d shown; scanned %d–%d of %d; document order, %s; @x,y,w×h = screen px):\n",
			uiaTargetLabel(hwnd), len(lines), start, next, total, layout)
		for _, l := range lines {
			b.WriteString(l)
			b.WriteString("\n")
		}
		b.WriteString(pageNote("elements", start, next, total, reason))
		return strings.TrimRight(b.String(), "\n"), nil
	})
}

// snapshotScanFactor bounds how many raw UIA elements one snapshot page walks,
// as a multiple of max_elements (most skipped elements are anonymous panes).
const snapshotScanFactor = 8

// snapshotLine renders one snapshot element line, indented by nd.depth.
func snapshotLine(nd uiaNode) string {
	d := nd.depth
	if d > snapshotIndentCap {
		d = snapshotIndentCap
	}
	return strings.Repeat("  ", d) + "- " + nodeDesc(nd)
}

// nodeDesc is an element's description shared by snapshot and element_at:
// type | "name" | id=… [capabilities] [state] @x,y,w×h = "preview".
func nodeDesc(nd uiaNode) string {
	caps := ""
	if nd.invoke {
		caps += " [invokable]"
	}
	if nd.value {
		caps += " [editable]"
	}
	if nd.text {
		caps += " [text]"
	}
	if !nd.enabled {
		caps += " [disabled]"
	}
	name := nd.name
	if name == "" {
		name = "(unnamed)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s | %q", controlTypeName(nd.ctrlType), name)
	if nd.autoId != "" {
		fmt.Fprintf(&b, " | id=%s", nd.autoId)
	}
	b.WriteString(caps)
	b.WriteString(stateFlags(nd))
	b.WriteString(rectSuffix(nd.rect))
	if nd.preview != "" {
		fmt.Fprintf(&b, " = %q", nd.preview)
	}
	return b.String()
}

// uiaElementAtPoint resolves the UIA element under absolute screen point
// (x,y) and describes it the same way a snapshot line does. This is the
// missing link between screenshot-based visual identification and reliable
// UIA-based action: spot something in a screenshot, get its accessible name
// here, then drive it with invoke/set_value/click_control instead of a raw
// coordinate click.
func uiaElementAtPoint(x, y int32) (string, error) {
	return uiaDo(func(uia *ole.IUnknown) (string, error) {
		el, err := elementFromPoint(uia, x, y)
		if err != nil {
			return "", err
		}
		defer release(el)

		nd := readNodeOne(uia, el)
		nd.preview = onelinePreview(nd.valueText, snapshotPreviewChars)
		var b strings.Builder
		b.WriteString(nodeDesc(nd))
		if nd.name != "" {
			fmt.Fprintf(&b, "\nUse invoke/set_value/set_state/click_control with name %q to act on it (prefer name over automationId if both are set).", nd.name)
		} else if nd.autoId != "" {
			fmt.Fprintf(&b, "\nNo accessible name, but has an automationId — invoke/set_value also match against automationId, so try %q there.", nd.autoId)
		} else {
			b.WriteString("\nNo accessible name or automationId — this element can't be targeted by invoke/set_value/click_control; fall back to win_controls for a coordinate, or click(x,y) as a last resort.")
		}
		return b.String(), nil
	})
}

// readNodeOne reads one element's fields with a single cached round trip,
// falling back to live per-property reads.
func readNodeOne(uia, el *ole.IUnknown) uiaNode {
	if cr, err := newCacheRequest(uia, nodeCacheProps, treeScopeElement); err == nil {
		defer release(cr)
		var cel *ole.IUnknown
		if hr := vcall(el, elemBuildUpdatedCache, uintptr(unsafe.Pointer(cr)), uintptr(unsafe.Pointer(&cel))); !failed(hr) && cel != nil {
			defer release(cel)
			return readNode(cel, true)
		}
	}
	return readNode(el, false)
}

// ---- find by selector ----

// propCondition creates a property condition for a VARIANT value. Caller must
// release the result.
func propCondition(uia *ole.IUnknown, propId int, v *ole.VARIANT) (*ole.IUnknown, error) {
	var cond *ole.IUnknown
	hr := vcall(uia, uiaCreatePropertyCondition,
		uintptr(propId),
		// VARIANT is passed by value (struct copy) across the ABI; on amd64 a
		// VARIANT larger than 8 bytes is passed by reference to a hidden copy,
		// so we pass its address.
		uintptr(unsafe.Pointer(v)),
		uintptr(unsafe.Pointer(&cond)))
	if failed(hr) || cond == nil {
		return nil, fmt.Errorf("CreatePropertyCondition failed (hr=0x%x)", uint32(hr))
	}
	return cond, nil
}

// findAllByProp finds every descendant whose property `propId` equals `value`
// and returns the resulting IUIAutomationElementArray plus its length. Caller
// must release the returned array (if non-nil).
func findAllByProp(uia *ole.IUnknown, root *ole.IUnknown, propId int, value string) (*ole.IUnknown, int32, error) {
	bstr := ole.SysAllocString(value)
	if bstr == nil {
		return nil, 0, fmt.Errorf("SysAllocString failed")
	}
	defer ole.SysFreeString(bstr)

	// VARIANT for a BSTR: vt = VT_BSTR (8), value = pointer to BSTR data.
	var v ole.VARIANT
	v.VT = vtBSTR
	*(*uintptr)(unsafe.Pointer(&v.Val)) = uintptr(unsafe.Pointer(bstr))
	cond, err := propCondition(uia, propId, &v)
	if err != nil {
		return nil, 0, err
	}
	defer release(cond)

	var arr *ole.IUnknown
	hr := vcall(root, elemFindAll, uintptr(treeScopeSubtree),
		uintptr(unsafe.Pointer(cond)), uintptr(unsafe.Pointer(&arr)))
	if failed(hr) || arr == nil {
		return nil, 0, fmt.Errorf("FindAll failed (hr=0x%x)", uint32(hr))
	}
	var l int32
	vcall(arr, arrGetLength, uintptr(unsafe.Pointer(&l)))
	return arr, l, nil
}

// findAllCached returns every element of root's subtree (only control type
// ctrlType when != 0) with props cached, in one call. Caller must release the
// array.
func findAllCached(uia, root *ole.IUnknown, ctrlType int32, props []int) (*ole.IUnknown, int32, error) {
	var cond *ole.IUnknown
	if ctrlType != 0 {
		var v ole.VARIANT
		v.VT = vtI4
		v.Val = int64(ctrlType)
		c, err := propCondition(uia, uiaControlTypePropertyId, &v)
		if err != nil {
			return nil, 0, err
		}
		cond = c
	} else if hr := vcall(uia, uiaCreateTrueCondition, uintptr(unsafe.Pointer(&cond))); failed(hr) || cond == nil {
		return nil, 0, fmt.Errorf("UIA: CreateTrueCondition failed (hr=0x%x)", uint32(hr))
	}
	defer release(cond)
	cr, err := newCacheRequest(uia, props, treeScopeElement)
	if err != nil {
		return nil, 0, err
	}
	defer release(cr)
	var arr *ole.IUnknown
	hr := vcall(root, elemFindAllBuildCache, uintptr(treeScopeSubtree),
		uintptr(unsafe.Pointer(cond)), uintptr(unsafe.Pointer(cr)), uintptr(unsafe.Pointer(&arr)))
	if failed(hr) || arr == nil {
		return nil, 0, fmt.Errorf("UIA: FindAllBuildCache failed (hr=0x%x)", uint32(hr))
	}
	var l int32
	vcall(arr, arrGetLength, uintptr(unsafe.Pointer(&l)))
	return arr, l, nil
}

// maxSelectorCandidates bounds how many matches one lookup holds (a contains
// match on "a" can hit most of a tree); nth beyond it is not reachable.
const maxSelectorCandidates = 500

// findCandidates returns every element matching sel in document order — Name
// matches, or AutomationId matches when no Name matched. Caller must release
// each element.
//
// exact uses UIA's own property condition (fast, provider-side match);
// contains/prefix fetch every name once (cached, narrowed by control type) and
// compare here, since UIA's substring flag is not available on older Windows.
func findCandidates(uia, root *ole.IUnknown, sel uiaSelector) ([]*ole.IUnknown, error) {
	if sel.Match == matchExact || sel.Match == "" {
		for _, prop := range []int{uiaNamePropertyId, uiaAutomationIdPropertyId} {
			arr, n, err := findAllByProp(uia, root, prop, sel.Name)
			if err != nil {
				return nil, err
			}
			var got []*ole.IUnknown
			for i := int32(0); i < n && len(got) < maxSelectorCandidates; i++ {
				var el *ole.IUnknown
				if hr := vcall(arr, arrGetElement, uintptr(i), uintptr(unsafe.Pointer(&el))); failed(hr) || el == nil {
					continue
				}
				if sel.ControlType != 0 && elemInt32(el, elemGetCurrentControlType) != sel.ControlType {
					release(el)
					continue
				}
				got = append(got, el)
			}
			release(arr)
			if len(got) > 0 {
				return got, nil
			}
		}
		return nil, nil
	}

	arr, n, err := findAllCached(uia, root, sel.ControlType, []int{uiaNamePropertyId, uiaAutomationIdPropertyId})
	if err != nil {
		return nil, err
	}
	defer release(arr)
	var byName, byID []*ole.IUnknown
	for i := int32(0); i < n && len(byName) < maxSelectorCandidates; i++ {
		var el *ole.IUnknown
		if hr := vcall(arr, arrGetElement, uintptr(i), uintptr(unsafe.Pointer(&el))); failed(hr) || el == nil {
			continue
		}
		switch {
		case sel.matches(elemString(el, elemGetCachedName)):
			byName = append(byName, el)
		case len(byName) == 0 && len(byID) < maxSelectorCandidates && sel.matches(elemString(el, elemGetCachedAutomationId)):
			byID = append(byID, el)
		default:
			release(el)
		}
	}
	if len(byName) > 0 {
		releaseAll(byID)
		return byName, nil
	}
	return byID, nil
}

func releaseAll(els []*ole.IUnknown) {
	for _, e := range els {
		release(e)
	}
}

// nameHints lists up to 5 names/automation ids under root closest to sel.Name
// — the "did you mean" part of a not-found error. Searches every control type
// (a wrong control_type is a common miss) and tags each hint with its type.
func nameHints(uia, root *ole.IUnknown, sel uiaSelector) []string {
	arr, n, err := findAllCached(uia, root, 0, []int{uiaNamePropertyId, uiaAutomationIdPropertyId, uiaControlTypePropertyId})
	if err != nil {
		return nil
	}
	defer release(arr)
	var pool []string
	typeOf := map[string]int32{}
	for i := int32(0); i < n; i++ {
		var el *ole.IUnknown
		if hr := vcall(arr, arrGetElement, uintptr(i), uintptr(unsafe.Pointer(&el))); failed(hr) || el == nil {
			continue
		}
		ct := elemInt32(el, elemGetCachedControlType)
		for _, s := range []string{elemString(el, elemGetCachedName), elemString(el, elemGetCachedAutomationId)} {
			if s = strings.TrimSpace(s); s != "" {
				if _, dup := typeOf[s]; !dup {
					typeOf[s] = ct
					pool = append(pool, s)
				}
			}
		}
		release(el)
	}
	hints := closestNames(sel.Name, pool, 5)
	for i, h := range hints {
		hints[i] = fmt.Sprintf("%s (%s)", h, controlTypeName(typeOf[h]))
	}
	return hints
}

// findElement resolves sel under root to one element. pred (may be nil) marks
// the candidates that support what the caller is about to do: without nth the
// first such candidate wins over the bare document-order first match. This
// matters because some apps (e.g. Outlook's compose window) give a field's
// static label the EXACT same caption as its edit control — "제목(U)" names
// both the "Subject:" label text and the subject edit box — so a plain first
// match can land on the inert label. note reports multiple matches ("matched
// 3; used 1st (use nth …)"); a miss lists the closest names. Caller must
// release the element.
func findElement(uia, root *ole.IUnknown, sel uiaSelector, pred func(*ole.IUnknown) bool) (*ole.IUnknown, string, error) {
	cands, err := findCandidates(uia, root, sel)
	if err != nil {
		return nil, "", err
	}
	if len(cands) == 0 {
		return nil, "", notFoundError(sel, nameHints(uia, root, sel))
	}
	return pickCandidate(cands, sel, pred)
}

// pickCandidate keeps the selected candidate and releases the rest.
func pickCandidate(cands []*ole.IUnknown, sel uiaSelector, pred func(*ole.IUnknown) bool) (*ole.IUnknown, string, error) {
	preferred := -1
	if pred != nil && sel.Nth == 0 && len(cands) > 1 {
		for i, c := range cands {
			if pred(c) {
				preferred = i
				break
			}
		}
	}
	idx, note, err := pickMatch(len(cands), sel.Nth, preferred)
	for i, c := range cands {
		if i != idx {
			release(c)
		}
	}
	if err != nil {
		return nil, "", fmt.Errorf("%s: %v", sel.describe(), err)
	}
	return cands[idx], note, nil
}

// exactSelector is the plain by-name selector of the pre-selector API.
func exactSelector(name string) uiaSelector {
	return uiaSelector{Name: name, Match: matchExact}
}

// ---- invoke ----

// invokeCapable reports whether el supports any pattern uiaInvoke can act
// through — used to disambiguate same-named elements in favor of the one
// uiaInvoke can actually activate (see findElement).
func invokeCapable(el *ole.IUnknown) bool {
	return elemSupportsPattern(el, uiaInvokePatternId) ||
		elemSupportsPattern(el, uiaSelectionItemPatternId) ||
		elemSupportsPattern(el, uiaTogglePatternId) ||
		elemSupportsPattern(el, uiaExpandCollapsePatternId)
}

// valueCapable reports whether el supports the Value pattern — used to
// disambiguate same-named elements in favor of the editable one (see
// findElement).
func valueCapable(el *ole.IUnknown) bool {
	return elemSupportsPattern(el, uiaValuePatternId)
}

// textCapable reports whether el exposes the UIA Text pattern — how Documents,
// read-only text areas and editors expose their content (they carry no Value
// pattern). Lets us read "what does this say" by handle instead of a screenshot.
func textCapable(el *ole.IUnknown) bool {
	return elemSupportsPattern(el, uiaTextPatternId)
}

// readableCapable prefers an element that actually carries readable text (a
// label and its edit box often share a caption; we want the one with content).
func readableCapable(el *ole.IUnknown) bool {
	return valueCapable(el) || textCapable(el)
}

// elemValue reads el's Value-pattern current text (the editable content of
// edit/combo controls). Returns ("", false) when el has no Value pattern.
func elemValue(el *ole.IUnknown) (string, bool) {
	p := getPattern(el, uiaValuePatternId)
	if p == nil {
		return "", false
	}
	defer release(p)
	var bstr *uint16
	if hr := vcall(p, valuePatternCurrentValue, uintptr(unsafe.Pointer(&bstr))); failed(hr) {
		return "", false
	}
	if bstr == nil {
		return "", true
	}
	s := ole.BstrToString(bstr)
	ole.SysFreeString((*int16)(unsafe.Pointer(bstr)))
	return s, true
}

// elemText reads el's content via the Text pattern's document range, truncated
// server-side to maxLen chars (pass a bounded value — never unbounded, so a huge
// document can't return megabytes). Returns ("", false) when el has no Text
// pattern, so callers can fall back to the Value pattern.
func elemText(el *ole.IUnknown, maxLen int) (string, bool) {
	tp := getPattern(el, uiaTextPatternId)
	if tp == nil {
		return "", false
	}
	defer release(tp)
	var rng *ole.IUnknown
	if hr := vcall(tp, textPatternGetDocumentRange, uintptr(unsafe.Pointer(&rng))); failed(hr) || rng == nil {
		return "", false
	}
	defer release(rng)
	if maxLen <= 0 {
		maxLen = 1
	}
	var bstr *uint16
	if hr := vcall(rng, textRangeGetText, uintptr(int32(maxLen)), uintptr(unsafe.Pointer(&bstr))); failed(hr) {
		return "", false
	}
	if bstr == nil {
		return "", true
	}
	s := ole.BstrToString(bstr)
	ole.SysFreeString((*int16)(unsafe.Pointer(bstr)))
	return s, true
}

// uiaInvoke finds an element by Name (or AutomationId) and activates it via the
// most appropriate pattern (Invoke → SelectionItem → Toggle → ExpandCollapse).
func uiaInvoke(name string) error {
	_, err := uiaInvokeIn(0, exactSelector(name))
	return err
}

// uiaInvokeIn is uiaInvoke for a selector, rooted at window hwnd. With hwnd != 0
// it runs in the background: lease only (no user-idle wait, no focus change),
// and the agent cursor marks the element afterwards. note is the selector's
// multiple-match note ("" when unambiguous).
func uiaInvokeIn(hwnd uintptr, sel uiaSelector) (note string, err error) {
	if err := beginUIAOp(hwnd); err != nil {
		return "", err
	}
	cx, cy, marked := 0, 0, false
	note, err = uiaDo(func(uia *ole.IUnknown) (string, error) {
		root, err := uiaRoot(uia, hwnd)
		if err != nil {
			return "", err
		}
		defer release(root)

		el, note, err := findElement(uia, root, sel, invokeCapable)
		if err != nil {
			return "", err
		}
		defer release(el)
		if hwnd != 0 {
			cx, cy, marked = elemCenter(el)
		}
		return note, activateElement(el, sel.Name)
	})
	if err == nil && marked {
		showAgentCursor(cx, cy)
	}
	return note, err
}

// activateElement drives el through the first pattern it supports
// (Invoke → SelectionItem → Toggle → ExpandCollapse). Runs on the STA thread.
func activateElement(el *ole.IUnknown, name string) error {
	_, err := func() (string, error) {
		if p := getPattern(el, uiaInvokePatternId); p != nil {
			defer release(p)
			if hr := vcall(p, invokePatternInvoke); failed(hr) {
				return "", fmt.Errorf("Invoke failed (hr=0x%x)", uint32(hr))
			}
			return "", nil
		}
		if p := getPattern(el, uiaSelectionItemPatternId); p != nil {
			defer release(p)
			if hr := vcall(p, selectionItemSelect); failed(hr) {
				return "", fmt.Errorf("Select failed (hr=0x%x)", uint32(hr))
			}
			return "", nil
		}
		if p := getPattern(el, uiaTogglePatternId); p != nil {
			defer release(p)
			if hr := vcall(p, togglePatternToggle); failed(hr) {
				return "", fmt.Errorf("Toggle failed (hr=0x%x)", uint32(hr))
			}
			return "", nil
		}
		if p := getPattern(el, uiaExpandCollapsePatternId); p != nil {
			defer release(p)
			if hr := vcall(p, expandCollapseExpand); failed(hr) {
				return "", fmt.Errorf("Expand failed (hr=0x%x)", uint32(hr))
			}
			return "", nil
		}
		return "", fmt.Errorf("element %q supports no invokable pattern (for a checkbox/expander/list item, set_state is more precise)", name)
	}()
	return err
}

// ---- set_value ----

// uiaSetValueIn finds an element by selector (rooted at window hwnd;
// background when != 0, see uiaInvokeIn) and sets its text via the Value
// pattern.
func uiaSetValueIn(hwnd uintptr, sel uiaSelector, text string) (note string, err error) {
	if err := beginUIAOp(hwnd); err != nil {
		return "", err
	}
	cx, cy, marked := 0, 0, false
	note, err = uiaDo(func(uia *ole.IUnknown) (string, error) {
		root, err := uiaRoot(uia, hwnd)
		if err != nil {
			return "", err
		}
		defer release(root)

		el, note, err := findElement(uia, root, sel, valueCapable)
		if err != nil {
			return "", err
		}
		defer release(el)
		if hwnd != 0 {
			cx, cy, marked = elemCenter(el)
		}

		p := getPattern(el, uiaValuePatternId)
		if p == nil {
			return "", fmt.Errorf("%s does not support the Value pattern", sel.describe())
		}
		defer release(p)

		bstr := ole.SysAllocString(text)
		if bstr == nil {
			return "", fmt.Errorf("SysAllocString failed")
		}
		defer ole.SysFreeString(bstr)

		if hr := vcall(p, valuePatternSetValue, uintptr(unsafe.Pointer(bstr))); failed(hr) {
			return "", fmt.Errorf("SetValue failed (hr=0x%x)", uint32(hr))
		}
		return note, nil
	})
	if err == nil && marked {
		showAgentCursor(cx, cy)
	}
	return note, err
}

// ---- get_value ----

// uiaGetValueIn finds an element by selector (rooted at window hwnd, 0 =
// foreground) and reads its current text via the UIA Value pattern — the
// read-side counterpart to uiaSetValueIn. Useful after set_value/type/invoke to
// confirm what a field actually holds now (e.g. autocomplete rewrote it, a
// calculated total field updated). A pure read, not synthetic input, so it
// does not go through beginSyntheticInput.
//
// The value comes back raw (the caller bounds it with clampFieldValue). For the
// Text-pattern fallback only maxFieldValueChars+1 units are fetched; totalKnown
// reports false when that fetch was cut, so the note can say "N자 이상".
func uiaGetValueIn(hwnd uintptr, sel uiaSelector) (value string, totalKnown bool, note string, err error) {
	totalKnown = true
	_, err = uiaDo(func(uia *ole.IUnknown) (string, error) {
		root, err := uiaRoot(uia, hwnd)
		if err != nil {
			return "", err
		}
		defer release(root)

		el, n, err := findElement(uia, root, sel, readableCapable)
		if err != nil {
			return "", err
		}
		defer release(el)
		note = n

		if s, ok := elemValue(el); ok {
			value = s
			return "", nil
		}
		// Documents / read-only text areas expose content via the Text pattern, not
		// Value — fall back so get_value reads them too instead of erroring out and
		// pushing the caller to a screenshot.
		if s, ok := elemText(el, maxFieldValueChars+1); ok {
			totalKnown = len(utf16.Encode([]rune(s))) <= maxFieldValueChars
			value = s
			return "", nil
		}
		return "", fmt.Errorf("%s supports neither the Value nor Text pattern (nothing to read; try get_text or snapshot)", sel.describe())
	})
	return value, totalKnown, note, err
}

// ---- get_text ----

// textFetchCeiling bounds how much text get_text pulls from the provider in one
// UIA GetText call (UTF-16 units). This is LOCAL memory only — what reaches the
// prompt is the offset/max_chars window cut by runeWindow — but it still keeps a
// pathological multi-megabyte document from stalling the read. Fetching the
// whole (bounded) document lets the truncation note report the real total length
// so the caller knows how far there is to go.
const textFetchCeiling = 1_000_000

// uiaGetText reads a control's textual content via the UIA Text pattern (with a
// Value-pattern fallback) so a worker can read documents, editors and read-only
// text panes BY HANDLE instead of screenshotting them. name is an element
// Name/AutomationId; an empty name targets the foreground window element itself
// (useful when the whole window is one text document). Only the window selected
// by offset/maxChars (runes; negative offset = tail) is returned, followed by a
// continuation note when the text was cut — see runeWindow. Returns "" when the
// element holds no text. A pure read — no synthetic input.
func uiaGetText(name string, offset, maxChars int) (string, error) {
	return uiaGetTextIn(0, exactSelector(name), offset, maxChars)
}

// uiaGetTextIn is uiaGetText for a selector, rooted at window hwnd (0 =
// foreground). An empty sel.Name reads the root itself.
func uiaGetTextIn(hwnd uintptr, sel uiaSelector, offset, maxChars int) (string, error) {
	return uiaDo(func(uia *ole.IUnknown) (string, error) {
		root, err := uiaRoot(uia, hwnd)
		if err != nil {
			return "", err
		}
		defer release(root)

		target, note := root, ""
		if strings.TrimSpace(sel.Name) != "" {
			found, n, ferr := findElement(uia, root, sel, readableCapable)
			if ferr != nil {
				return "", ferr
			}
			defer release(found)
			target, note = found, n
		}

		full, totalKnown, ok := "", true, false
		if s, got := elemText(target, textFetchCeiling); got {
			full, ok = s, true
			totalKnown = len(utf16.Encode([]rune(s))) < textFetchCeiling
		} else if s, got := elemValue(target); got {
			full, ok = s, true
		}
		if !ok {
			if strings.TrimSpace(sel.Name) == "" {
				return "", fmt.Errorf("the %s exposes no Text pattern at its root; name a specific element from snapshot, or use capture_window as a last resort", uiaTargetLabel(hwnd))
			}
			return "", fmt.Errorf("%s supports neither the Text nor Value pattern", sel.describe())
		}
		if strings.TrimSpace(full) == "" {
			return "", nil
		}
		body, tnote := runeWindow(full, offset, maxChars, totalKnown)
		if note != "" {
			tnote += "\n[" + note + "]"
		}
		return body + tnote, nil
	})
}

// ---- wait_for_control ----

// uiaWaitForControlPollEvery is how often uiaWaitForControl re-checks while
// waiting. A var (not const) so tests can shrink it.
var uiaWaitForControlPollEvery = 250 * time.Millisecond

// probeOpts says what one wait_for_control observation should also read.
type probeOpts struct {
	Value bool // read the element's value (until=value)
	Hints bool // on a miss, collect closest names (final poll only — it enumerates the tree)
}

// uiaControlProbe observes the element sel picks in window hwnd's UIA tree (0 =
// foreground). A package var (wrapping the real uiaDo-based lookup) so
// uiaWaitForControl's polling loop is unit-testable without a real UIA/COM
// worker thread — mirrors the findTopWindowProbe pattern already used for
// waitForWindow's tests.
var uiaControlProbe = func(hwnd uintptr, sel uiaSelector, opts probeOpts) (uiaProbeResult, error) {
	var res uiaProbeResult
	_, err := uiaDo(func(uia *ole.IUnknown) (string, error) {
		root, err := uiaRoot(uia, hwnd)
		if err != nil {
			return "", err
		}
		defer release(root)
		cands, err := findCandidates(uia, root, sel)
		if err != nil {
			return "", err
		}
		if len(cands) == 0 || sel.Nth > len(cands) {
			releaseAll(cands)
			if opts.Hints {
				res.Hints = nameHints(uia, root, sel)
			}
			return "", nil
		}
		var pred func(*ole.IUnknown) bool
		if opts.Value {
			pred = readableCapable
		}
		el, _, err := pickCandidate(cands, sel, pred)
		if err != nil {
			return "", err
		}
		defer release(el)
		res.Found = true
		res.Enabled = elemInt32(el, elemGetCurrentIsEnabled) != 0
		if opts.Value {
			if s, ok := elemValue(el); ok {
				res.Value, res.HasValue = s, true
			} else if s, ok := elemText(el, maxFieldValueChars); ok {
				res.Value, res.HasValue = s, true
			}
		}
		return "", nil
	})
	return res, err
}

// uiaWaitForControl polls the foreground window's UIA tree until an element
// matching name (Name or AutomationId) appears, or timeoutMs elapses —
// mirrors aglink-web's wait_for_element and aglink-screen's own
// wait_for_window, but for a specific control inside the foreground window
// rather than a whole top-level window. Replaces a manual snapshot-polling
// loop for dialogs/controls that render a moment after the action that
// triggers them (e.g. a "Save" dialog, a panel that appears after a click).
func uiaWaitForControl(name string, timeoutMs int) (string, error) {
	return uiaWaitForControlIn("", exactSelector(name), waitCond{Until: untilAppear}, timeoutMs)
}

// uiaWaitForControlIn is uiaWaitForControl for a selector and condition
// (appear / disappear / enabled / value), searching the window matching
// `window` (title substring/hwnd; "" = foreground). The window is re-resolved
// on every poll, so it may itself appear — or, for disappear, close — during
// the wait.
func uiaWaitForControlIn(window string, sel uiaSelector, cond waitCond, timeoutMs int) (string, error) {
	if timeoutMs <= 0 {
		timeoutMs = 8000
	}
	if cond.Until == "" {
		cond.Until = untilAppear
	}
	window = strings.TrimSpace(window)
	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	var last uiaProbeResult
	for {
		final := !time.Now().Before(deadline)
		hwnd, ok := uintptr(0), true
		if window != "" {
			hwnd, ok = findTopWindowProbe(window)
		}
		if ok {
			p, err := uiaControlProbe(hwnd, sel, probeOpts{Value: cond.Until == untilValue, Hints: final && cond.Until == untilAppear})
			if err == nil { // a failed probe (window closing mid-query) proves nothing either way
				last = p
				if cond.met(p) {
					return waitSuccess(sel, cond, p), nil
				}
			}
		} else if cond.Until == untilDisappear {
			return fmt.Sprintf("ok: window %q is gone, and %s with it", window, sel.describe()), nil
		}
		if final {
			switch {
			case !ok:
				return "", fmt.Errorf("timed out after %dms: no window matching %q", timeoutMs, window)
			case cond.Until == untilAppear:
				return "", fmt.Errorf("timed out after %dms: %v", timeoutMs, notFoundError(sel, last.Hints))
			}
			return "", fmt.Errorf("timed out after %dms waiting for %s %s (last: %s)", timeoutMs, sel.describe(), cond.describe(), cond.observed(last))
		}
		time.Sleep(uiaWaitForControlPollEvery)
	}
}

// waitSuccess is wait_for_control's ok line.
func waitSuccess(sel uiaSelector, cond waitCond, p uiaProbeResult) string {
	switch cond.Until {
	case untilDisappear:
		return fmt.Sprintf("ok: %s is gone", sel.describe())
	case untilEnabled:
		return fmt.Sprintf("ok: %s is enabled", sel.describe())
	case untilValue:
		return fmt.Sprintf("ok: %s value = %q", sel.describe(), onelinePreview(p.Value, 200))
	}
	if sel.ControlType == 0 && (sel.Match == matchExact || sel.Match == "") {
		return fmt.Sprintf("ok: found %q", sel.Name)
	}
	return "ok: found " + sel.describe()
}

// keep windows import referenced even if other helpers change.
var _ = windows.UTF16ToString
