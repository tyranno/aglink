//go:build windows

package main

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
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
	uiaNamePropertyId         = 30005
	uiaAutomationIdPropertyId = 30011

	// Control pattern IDs.
	uiaInvokePatternId         = 10000
	uiaValuePatternId          = 10002
	uiaTogglePatternId         = 10015
	uiaSelectionItemPatternId  = 10010
	uiaExpandCollapsePatternId = 10005

	// TreeScope flags.
	treeScopeElement     = 1
	treeScopeChildren    = 2
	treeScopeDescendants = 4
	treeScopeSubtree     = 7
)

// ---- IUIAutomation vtable slot indices (incl. IUnknown 0,1,2) ----
const (
	uiaGetRootElement          = 5 // GetRootElement(out **IUIAutomationElement)
	uiaElementFromHandle       = 6 // ElementFromHandle(HWND, out **IUIAutomationElement)
	uiaGetFocusedElement       = 8 // GetFocusedElement(out **IUIAutomationElement)
	uiaCreateTrueCondition     = 21
	uiaCreatePropertyCondition = 23 // CreatePropertyCondition(PROPERTYID, VARIANT, out **IUIAutomationCondition)
)

// ---- IUIAutomationElement vtable slot indices (incl. IUnknown 0,1,2) ----
const (
	elemSetFocus               = 3
	elemFindFirst              = 5  // FindFirst(scope, *cond, out **elem)
	elemFindAll                = 6  // FindAll(scope, *cond, out **IUIAutomationElementArray)
	elemGetCurrentPattern      = 16 // GetCurrentPattern(patternId, out **IUnknown)
	elemGetCurrentControlType  = 21 // -> *int32 (CONTROLTYPEID)
	elemGetCurrentName         = 23 // -> *BSTR
	elemGetCurrentIsEnabled    = 28 // -> *BOOL(int32)
	elemGetCurrentAutomationId = 29 // -> *BSTR
)

// ---- IUIAutomationElementArray vtable slot indices ----
const (
	arrGetLength  = 3 // get_Length(out *int32)
	arrGetElement = 4 // GetElement(int32 index, out **elem)
)

// ---- Pattern vtable slots (all inherit IUnknown 0,1,2) ----
const (
	invokePatternInvoke  = 3 // Invoke()
	valuePatternSetValue = 3 // SetValue(BSTR)
	togglePatternToggle  = 3 // Toggle()
	selectionItemSelect  = 3 // Select()
	expandCollapseExpand = 3 // Expand()
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

// ---- snapshot ----

type uiaNode struct {
	name     string
	ctrlType int32
	autoId   string
	enabled  bool
	invoke   bool
	value    bool
	depth    int
}

// uiaSnapshot walks the foreground window's element subtree (children-first,
// breadth-limited) and returns a compact textual listing capped at maxElems.
func uiaSnapshot(maxElems int) (string, error) {
	if maxElems <= 0 {
		maxElems = 200
	}
	return uiaDo(func(uia *ole.IUnknown) (string, error) {
		root, err := foregroundElement(uia)
		if err != nil {
			return "", err
		}
		defer release(root)

		// TrueCondition matches every element.
		var cond *ole.IUnknown
		hr := vcall(uia, uiaCreateTrueCondition, uintptr(unsafe.Pointer(&cond)))
		if failed(hr) || cond == nil {
			return "", fmt.Errorf("UIA: CreateTrueCondition failed (hr=0x%x)", uint32(hr))
		}
		defer release(cond)

		// FindAll over the subtree (descendants + self).
		var arr *ole.IUnknown
		hr = vcall(root, elemFindAll, uintptr(treeScopeSubtree),
			uintptr(unsafe.Pointer(cond)), uintptr(unsafe.Pointer(&arr)))
		if failed(hr) || arr == nil {
			return "", fmt.Errorf("UIA: FindAll failed (hr=0x%x)", uint32(hr))
		}
		defer release(arr)

		var length int32
		vcall(arr, arrGetLength, uintptr(unsafe.Pointer(&length)))

		truncated := false
		n := int(length)
		if n > maxElems {
			n = maxElems
			truncated = true
		}

		var nodes []uiaNode
		for i := 0; i < n; i++ {
			var el *ole.IUnknown
			hr := vcall(arr, arrGetElement, uintptr(int32(i)), uintptr(unsafe.Pointer(&el)))
			if failed(hr) || el == nil {
				continue
			}
			name := elemString(el, elemGetCurrentName)
			ct := elemInt32(el, elemGetCurrentControlType)
			autoId := elemString(el, elemGetCurrentAutomationId)
			enabled := elemInt32(el, elemGetCurrentIsEnabled) != 0
			canInvoke := elemSupportsPattern(el, uiaInvokePatternId)
			canValue := elemSupportsPattern(el, uiaValuePatternId)
			release(el)

			// Skip wholly anonymous, non-interactive nodes to save tokens.
			if strings.TrimSpace(name) == "" && autoId == "" && !canInvoke && !canValue {
				continue
			}
			nodes = append(nodes, uiaNode{
				name:     name,
				ctrlType: ct,
				autoId:   autoId,
				enabled:  enabled,
				invoke:   canInvoke,
				value:    canValue,
			})
		}

		// Stable, readable order: by control type then name.
		sort.SliceStable(nodes, func(i, j int) bool {
			if nodes[i].ctrlType != nodes[j].ctrlType {
				return nodes[i].ctrlType < nodes[j].ctrlType
			}
			return nodes[i].name < nodes[j].name
		})

		var b strings.Builder
		fmt.Fprintf(&b, "UIA elements of foreground window (%d shown of %d):\n", len(nodes), length)
		for _, nd := range nodes {
			caps := ""
			if nd.invoke {
				caps += " [invokable]"
			}
			if nd.value {
				caps += " [editable]"
			}
			if !nd.enabled {
				caps += " [disabled]"
			}
			name := nd.name
			if name == "" {
				name = "(unnamed)"
			}
			fmt.Fprintf(&b, "- %s | %q", controlTypeName(nd.ctrlType), name)
			if nd.autoId != "" {
				fmt.Fprintf(&b, " | id=%s", nd.autoId)
			}
			b.WriteString(caps)
			b.WriteString("\n")
		}
		if truncated {
			fmt.Fprintf(&b, "(truncated to %d elements; increase 'max' to see more)\n", maxElems)
		}
		return strings.TrimRight(b.String(), "\n"), nil
	})
}

// ---- find by name / automationId ----

// findFirstByProp finds the first descendant whose property `propId` equals
// `value` (a string). Caller must release the returned element.
func findFirstByProp(uia *ole.IUnknown, root *ole.IUnknown, propId int, value string) (*ole.IUnknown, error) {
	bstr := ole.SysAllocString(value)
	if bstr == nil {
		return nil, fmt.Errorf("SysAllocString failed")
	}
	defer ole.SysFreeString(bstr)

	// VARIANT for a BSTR: vt = VT_BSTR (8), value = pointer to BSTR data.
	var v ole.VARIANT
	v.VT = 8 // VT_BSTR
	// Store the BSTR pointer into the VARIANT's value slot.
	*(*uintptr)(unsafe.Pointer(&v.Val)) = uintptr(unsafe.Pointer(bstr))

	var cond *ole.IUnknown
	hr := vcall(uia, uiaCreatePropertyCondition,
		uintptr(propId),
		// VARIANT is passed by value (struct copy) across the ABI; on amd64 a
		// 16-byte VARIANT is passed by reference to a hidden copy. go-ole's
		// VARIANT is 16 bytes (VT + reserved + Val), matching the Win32 layout
		// for the BSTR case, so we pass its address.
		uintptr(unsafe.Pointer(&v)),
		uintptr(unsafe.Pointer(&cond)))
	if failed(hr) || cond == nil {
		return nil, fmt.Errorf("CreatePropertyCondition failed (hr=0x%x)", uint32(hr))
	}
	defer release(cond)

	var found *ole.IUnknown
	hr = vcall(root, elemFindFirst, uintptr(treeScopeSubtree),
		uintptr(unsafe.Pointer(cond)), uintptr(unsafe.Pointer(&found)))
	if failed(hr) {
		return nil, fmt.Errorf("FindFirst failed (hr=0x%x)", uint32(hr))
	}
	return found, nil // may be nil if not found
}

// findByName tries Name first, then AutomationId.
func findByName(uia *ole.IUnknown, root *ole.IUnknown, name string) (*ole.IUnknown, error) {
	el, err := findFirstByProp(uia, root, uiaNamePropertyId, name)
	if err == nil && el != nil {
		return el, nil
	}
	if el != nil {
		release(el)
	}
	return findFirstByProp(uia, root, uiaAutomationIdPropertyId, name)
}

// ---- invoke ----

// uiaInvoke finds an element by Name (or AutomationId) and activates it via the
// most appropriate pattern (Invoke → SelectionItem → Toggle → ExpandCollapse).
func uiaInvoke(name string) error {
	ensureControlNotice()
	_, err := uiaDo(func(uia *ole.IUnknown) (string, error) {
		root, err := foregroundElement(uia)
		if err != nil {
			return "", err
		}
		defer release(root)

		el, err := findByName(uia, root, name)
		if err != nil {
			return "", err
		}
		if el == nil {
			return "", fmt.Errorf("no element named %q (try snapshot)", name)
		}
		defer release(el)

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
		return "", fmt.Errorf("element %q supports no invokable pattern", name)
	})
	return err
}

// ---- set_value ----

// uiaSetValue finds an element by Name (or AutomationId) and sets its text via
// the Value pattern.
func uiaSetValue(name, text string) error {
	ensureControlNotice()
	_, err := uiaDo(func(uia *ole.IUnknown) (string, error) {
		root, err := foregroundElement(uia)
		if err != nil {
			return "", err
		}
		defer release(root)

		el, err := findByName(uia, root, name)
		if err != nil {
			return "", err
		}
		if el == nil {
			return "", fmt.Errorf("no element named %q (try snapshot)", name)
		}
		defer release(el)

		p := getPattern(el, uiaValuePatternId)
		if p == nil {
			return "", fmt.Errorf("element %q does not support the Value pattern", name)
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
		return "", nil
	})
	return err
}

// keep windows import referenced even if other helpers change.
var _ = windows.UTF16ToString
