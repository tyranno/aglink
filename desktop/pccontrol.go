package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// PC control by voice ("메모장 열어 줘", "소리 줄여 줘", "화면 꺼 줘", "90분 뒤에
// 컴퓨터 꺼 줘"): the frontend matches the sentence and calls these. The
// Windows parts (launching, Core Audio, monitor power) are in
// pccontrol_windows.go.
const (
	minShutdownSec = 60        // a spoken shutdown never happens at once
	maxShutdownSec = 24 * 3600 // ...nor further off than a day
	screenOffDelay = 3 * time.Second
)

// launchNameRe is what may be handed to the shell as a program name: a plain
// name like "chrome" or "winword.exe" — no path, drive, URL or arguments.
var launchNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9 ._-]{0,39}$`)

// launchDenied are names that are never started by voice, whatever was heard.
var launchDenied = map[string]bool{
	"cmd": true, "powershell": true, "pwsh": true, "shutdown": true, "format": true, "del": true,
	"rd": true, "rmdir": true, "reg": true, "regedit": true, "diskpart": true, "bcdedit": true,
	"cipher": true, "vssadmin": true, "wmic": true, "taskkill": true, "net": true, "sc": true,
	"rundll32": true, "mshta": true, "wscript": true, "cscript": true, "msiexec": true,
}

// launchAllowed tells a program name that may be started.
func launchAllowed(name string) bool {
	if strings.HasPrefix(name, "ms-settings:") {
		return true
	}
	if !launchNameRe.MatchString(name) || strings.Contains(name, "..") {
		return false
	}
	base := strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(name), ".exe"), ".com"))
	return !launchDenied[strings.TrimSpace(base)]
}

// PCOpen starts a program by name ("notepad.exe", "chrome", "ms-settings:")
// the way Run (Win+R) does, so App Paths entries like chrome are found.
func (v *VoiceService) PCOpen(name string) error {
	name = strings.TrimSpace(name)
	if !launchAllowed(name) {
		return fmt.Errorf("열 수 없는 이름이에요: %q", name)
	}
	return shellOpen(name)
}

// PCVolume changes the speaker volume: op is "up"/"down" (by value, in
// percent), "set" (to value), "mute" or "unmute". It reports the result as
// JSON {"level":0..100,"muted":bool}.
func (v *VoiceService) PCVolume(op string, value int) (string, error) {
	level, muted, err := volumeGet()
	if err != nil {
		return "", err
	}
	switch op {
	case "up":
		level, muted = level+value, false
	case "down":
		level -= value
	case "set":
		level, muted = value, false
	case "mute":
		muted = true
	case "unmute":
		muted = false
	default:
		return "", fmt.Errorf("unknown volume op %q", op)
	}
	level = min(max(level, 0), 100)
	if err := volumeSet(level, muted); err != nil {
		return "", err
	}
	b, _ := json.Marshal(map[string]any{"level": level, "muted": muted})
	return string(b), nil
}

// PCScreenOff turns the monitor off after a moment, so the spoken answer is
// not cut and a key press made while saying it does not wake it again.
func (v *VoiceService) PCScreenOff() error {
	go func() {
		time.Sleep(screenOffDelay)
		monitorOff()
	}()
	return nil
}

// PCShutdown has Windows shut down in sec seconds (shutdown /s /t). Anything
// under a minute is refused: a misheard sentence must leave time to cancel.
func (v *VoiceService) PCShutdown(sec int) error {
	if sec < minShutdownSec || sec > maxShutdownSec {
		return fmt.Errorf("종료 시간은 1분에서 24시간 사이여야 해요 (%d초)", sec)
	}
	return runShutdown("/s", "/t", fmt.Sprint(sec))
}

// errNoShutdown: "컴퓨터 끄기 취소" with no shutdown pending.
var errNoShutdown = errors.New("no shutdown pending")

// PCShutdownCancel calls off a pending shutdown (shutdown /a). It answers
// "none" when there was none to call off.
func (v *VoiceService) PCShutdownCancel() (string, error) {
	err := runShutdown("/a")
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1116 { // ERROR_NO_SHUTDOWN_IN_PROGRESS
		return "none", nil
	}
	if err != nil {
		return "", err
	}
	return "ok", nil
}

func runShutdown(args ...string) error {
	cmd := exec.Command("shutdown.exe", args...)
	hideConsole(cmd)
	return cmd.Run()
}
