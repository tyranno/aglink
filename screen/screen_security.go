package main

import (
	"bufio"
	"strings"
)

// Passing Windows security prompts.
//
// When aglink runs a program, Windows can interpose a confirmation window that a
// human would normally click through: the "Open File - Security Warning" dialog
// on a downloaded .exe, SmartScreen's "Windows protected your PC", or an app's
// own "really do this?" popup. pass_security_prompts recognizes such a window
// and clicks its affirmative button so an unattended session is not stuck
// waiting at it.
//
// It only ever acts on a window it RECOGNIZES — a built-in spec below, or one the
// user registered — and never on the UAC consent window, which Windows isolates
// on the secure desktop where no synthetic input reaches it (that one is
// reported, not faked). Nothing here weakens a security decision the user has
// not already made: the user chose to run aglink elevated and, for app popups,
// listed the exact window+button by hand.

// securityPrompt describes one window that pass_security_prompts may click
// through: any Titles substring identifies it, and the first of Buttons found as
// an affirmative control is clicked. uia marks a modern (XAML) dialog whose
// buttons are UI-Automation elements rather than Win32 Button controls.
type securityPrompt struct {
	Name    string
	Titles  []string
	Buttons []string
	UIA     bool
}

// builtinSecurityPrompts are the Windows-shipped confirmation windows aglink
// knows how to pass. App-specific popups are not here; the user registers those
// (see parseUserPrompts) so a short token never clicks an unintended button.
var builtinSecurityPrompts = []securityPrompt{
	{
		// "파일 열기 - 보안 경고" / "Open File - Security Warning": the run
		// confirmation on an exe that carries a downloaded-from-the-internet mark.
		// Verified live: a Win32 dialog whose affirmative button is a Button
		// control labelled "실행(&R)".
		Name:    "open-file-security-warning",
		Titles:  []string{"보안 경고", "Security Warning"},
		Buttons: []string{"실행", "Run"},
	},
	{
		// SmartScreen "Windows의 PC 보호" / "Windows protected your PC". A modern
		// dialog: its visible button is "실행 안 함"/"Don't run"; the affirmative
		// path is "추가 정보"/"More info" first, then "실행"/"Run anyway". Handled
		// via UI Automation; see passSmartScreen. NOT verified live.
		Name:    "smartscreen",
		Titles:  []string{"PC 보호", "protected your PC", "Windows Defender SmartScreen"},
		Buttons: []string{"실행", "Run anyway"},
		UIA:     true,
	},
}

// uacTitles identify the Windows User Account Control consent window. It runs on
// the secure desktop, so no tool — elevated or not — can click it; aglink reports
// it instead of pretending. (It usually cannot even be enumerated; this is a
// best-effort guard for the rare configuration where it appears as an ordinary
// window.)
var uacTitles = []string{"사용자 계정 컨트롤", "User Account Control"}

// userSecurityPromptsFile is read from the aglink data dir (~/.aglink). Each
// non-empty, non-comment line registers one app popup as `title | btn1 + btn2`:
// a window-title substring, then one or more affirmative button labels to try.
//
//	# 사내 인증 모듈이 띄우는 허용 창
//	INISAFE | 허용 + 예
const userSecurityPromptsFile = "aglink-screen-prompts"

// parseUserPrompts turns the file's text into specs. A line is
// `title | label[ + label...]`; blank lines and `#` comments are skipped. A line
// with no `|`, an empty title, or no buttons is ignored (not an error) so a
// stray line never aborts a sweep.
func parseUserPrompts(content string) []securityPrompt {
	var out []securityPrompt
	sc := bufio.NewScanner(strings.NewReader(content))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		bar := strings.IndexByte(line, '|')
		if bar < 0 {
			continue
		}
		title := strings.TrimSpace(line[:bar])
		if title == "" {
			continue
		}
		var buttons []string
		for _, b := range strings.Split(line[bar+1:], "+") {
			if b = strings.TrimSpace(b); b != "" {
				buttons = append(buttons, b)
			}
		}
		if len(buttons) == 0 {
			continue
		}
		out = append(out, securityPrompt{Name: "user:" + title, Titles: []string{title}, Buttons: buttons})
	}
	return out
}

// matchSecurityPrompt returns the first spec whose any Title is a case-insensitive
// substring of title.
func matchSecurityPrompt(title string, specs []securityPrompt) (securityPrompt, bool) {
	lt := strings.ToLower(strings.TrimSpace(title))
	if lt == "" {
		return securityPrompt{}, false
	}
	for _, s := range specs {
		for _, t := range s.Titles {
			if t = strings.ToLower(strings.TrimSpace(t)); t != "" && strings.Contains(lt, t) {
				return s, true
			}
		}
	}
	return securityPrompt{}, false
}

// isUACTitle reports whether title looks like the UAC consent window.
func isUACTitle(title string) bool {
	lt := strings.ToLower(strings.TrimSpace(title))
	for _, t := range uacTitles {
		if strings.Contains(lt, strings.ToLower(t)) {
			return true
		}
	}
	return false
}
