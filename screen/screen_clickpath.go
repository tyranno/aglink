package main

import "strings"

// clickPath is how click_control acts on a matched Win32 control.
type clickPath int

const (
	// clickPathFocus: bring the window to front and send a real mouse click.
	clickPathFocus clickPath = iota
	// clickPathBMClick: post BM_CLICK to the button — no focus change, cursor untouched.
	clickPathBMClick
	// clickPathUIAInvoke: UIA Invoke/Toggle/Select on the control's element.
	clickPathUIAInvoke
)

func (p clickPath) String() string {
	switch p {
	case clickPathBMClick:
		return "BM_CLICK"
	case clickPathUIAInvoke:
		return "UIA invoke"
	default:
		return "focus+mouse click"
	}
}

// clickTarget is what chooseClickPath needs to know about a control.
type clickTarget struct {
	Class   string
	Style   uint32
	Enabled bool
}

// Button style types (low nibble of the window style, BS_TYPEMASK).
const (
	bsTypeMask = 0xF
	bsGroupBox = 0x7
)

// isButtonClass reports whether a Win32 class is a BUTTON (plain, or a
// framework wrapper such as WinForms' "WindowsForms10.BUTTON.app…").
func isButtonClass(class string) bool {
	return strings.Contains(strings.ToLower(class), "button")
}

// chooseClickPath picks how to click a control. background=false always keeps
// the old focus+click. Otherwise a button/checkbox/radio gets BM_CLICK, any
// other control a UIA invoke when its element supports one (uiaInvokable is
// only consulted then), and everything else falls back to focus+click. The
// reason explains a fallback; it is empty when a background path was chosen.
func chooseClickPath(background bool, t clickTarget, uiaInvokable func() bool) (clickPath, string) {
	if !background {
		return clickPathFocus, "background:false"
	}
	if !t.Enabled {
		return clickPathFocus, "control is disabled"
	}
	if isButtonClass(t.Class) {
		if t.Style&bsTypeMask == bsGroupBox {
			return clickPathFocus, "control is a group box"
		}
		return clickPathBMClick, ""
	}
	if uiaInvokable != nil && uiaInvokable() {
		return clickPathUIAInvoke, ""
	}
	return clickPathFocus, "control is not a button and exposes no UIA invoke"
}
