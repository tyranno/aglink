package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Meeting mode ("지금부터 듣고 있어"): nothing is recorded as audio, but
// everything recognised while it is on is written down as it is heard, so
// what was said is still there afterwards even though the sound itself
// never touches disk.
const meetingDirName = "aglink 회의"

type meetingState struct {
	mu   sync.Mutex
	txt  *os.File
	path string
}

var meeting meetingState

// meetingDir is Documents\aglink 회의, where the user finds it in Explorer.
func meetingDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "Documents", meetingDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// MeetingStart opens a new text file for a meeting and returns its path. A
// file left open from an earlier meeting is closed first.
func (v *VoiceService) MeetingStart() (string, error) {
	meeting.mu.Lock()
	defer meeting.mu.Unlock()
	meeting.closeLocked()
	dir, err := meetingDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "회의 "+time.Now().Format("2006-01-02 15-04-05")+".txt")
	txt, err := os.Create(path)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(txt, "회의 %s\n\n", time.Now().Format("2006-01-02 15:04:05"))
	meeting.txt, meeting.path = txt, path
	return path, nil
}

// MeetingNote writes a recognised line to the meeting's text file, stamped
// with the time into the meeting where it was said (atSec).
func (v *VoiceService) MeetingNote(atSec float64, text string) error {
	meeting.mu.Lock()
	defer meeting.mu.Unlock()
	if meeting.txt == nil {
		return errors.New("회의 모드가 아닙니다")
	}
	_, err := fmt.Fprintf(meeting.txt, "[%s] %s\n", clockOf(int(atSec)), strings.TrimSpace(text))
	return err
}

// MeetingStop closes the meeting's text file and reports it as JSON {"path"}.
func (v *VoiceService) MeetingStop() (string, error) {
	meeting.mu.Lock()
	defer meeting.mu.Unlock()
	if meeting.txt == nil {
		return "", errors.New("회의 모드가 아닙니다")
	}
	path := meeting.path
	meeting.closeLocked()
	b, _ := json.Marshal(map[string]any{"path": path})
	return string(b), nil
}

func (m *meetingState) closeLocked() {
	if m.txt != nil {
		m.txt.Close()
		m.txt = nil
	}
	m.path = ""
}

// OpenMeetingFolder shows the meeting notes folder in Explorer.
func (v *VoiceService) OpenMeetingFolder() error {
	dir, err := meetingDir()
	if err != nil {
		return err
	}
	return exec.Command("explorer.exe", dir).Start()
}

// maxMeetingListPreview is how much of a meeting's first lines the list shows.
const maxMeetingListPreview = 120

// MeetingList reports the saved meetings, newest first, as JSON
// [{path, name, started, lines, bytes, preview, live}]. live marks the one
// being written now.
func (v *VoiceService) MeetingList() (string, error) {
	dir, err := meetingDir()
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	meeting.mu.Lock()
	livePath := meeting.path
	meeting.mu.Unlock()
	type item struct {
		Path    string `json:"path"`
		Name    string `json:"name"`
		Started string `json:"started"`
		Lines   int    `json:"lines"`
		Bytes   int64  `json:"bytes"`
		Preview string `json:"preview"`
		Live    bool   `json:"live"`
		// From the AI's write-up next to it (" 정리.md"), when there is one.
		Title    string `json:"title"`
		Category string `json:"category"`
		Gist     string `json:"gist"`
		Summary  string `json:"summary"`
	}
	out := []item{}
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".txt") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		p := filepath.Join(dir, e.Name())
		it := item{Path: p, Name: strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())), Bytes: info.Size(), Live: p == livePath}
		if t, ok := meetingStartOf(e.Name()); ok {
			it.Started = t.Format("2006-01-02 15:04:05")
		} else {
			it.Started = info.ModTime().Format("2006-01-02 15:04:05")
		}
		if b, err := os.ReadFile(p); err == nil {
			var said []string
			for _, ln := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(ln, "[") {
					said = append(said, ln)
				}
			}
			it.Lines = len(said)
			if len(said) > 0 {
				prev := []rune(strings.Join(said[:min(len(said), 3)], " "))
				if len(prev) > maxMeetingListPreview {
					prev = append(prev[:maxMeetingListPreview], '…')
				}
				it.Preview = string(prev)
			}
		}
		if b, err := os.ReadFile(meetingSummaryPath(p)); err == nil {
			it.Summary = meetingSummaryPath(p)
			it.Title, it.Category, it.Gist = parseMeetingSummary(string(b))
		}
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started > out[j].Started })
	b, _ := json.Marshal(out)
	return string(b), nil
}

// meetingStartOf reads the start time from a name like "회의 2026-11-06 15-04-05.txt".
func meetingStartOf(name string) (time.Time, bool) {
	s := strings.TrimSuffix(strings.TrimPrefix(name, "회의 "), filepath.Ext(name))
	t, err := time.ParseInLocation("2006-01-02 15-04-05", s, time.Local)
	return t, err == nil
}

// meetingFile checks that p is a text file directly in the meetings folder.
func meetingFile(p string) (string, error) {
	dir, err := meetingDir()
	if err != nil {
		return "", err
	}
	p = filepath.Clean(p)
	if !strings.EqualFold(filepath.Dir(p), filepath.Clean(dir)) || !strings.EqualFold(filepath.Ext(p), ".txt") {
		return "", errors.New("회의 폴더의 글 파일이 아니에요")
	}
	return p, nil
}

// MeetingRead returns the text of a saved meeting.
func (v *VoiceService) MeetingRead(path string) (string, error) {
	p, err := meetingFile(path)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// MeetingDelete removes a saved meeting; the one being written cannot be.
func (v *VoiceService) MeetingDelete(path string) error {
	p, err := meetingFile(path)
	if err != nil {
		return err
	}
	meeting.mu.Lock()
	live := p == meeting.path
	meeting.mu.Unlock()
	if live {
		return errors.New("지금 듣고 있는 회의는 지울 수 없어요")
	}
	if err := os.Remove(p); err != nil {
		return err
	}
	_ = os.Remove(meetingSummaryPath(p))
	return nil
}

// meetingSummaryPath is where the AI's write-up of the meeting txtPath goes.
func meetingSummaryPath(txtPath string) string {
	return strings.TrimSuffix(txtPath, filepath.Ext(txtPath)) + " 정리.md"
}

// parseMeetingSummary reads the title ("# …"), the category ("분류: …") and
// the first line under the summary heading from a meeting write-up.
func parseMeetingSummary(md string) (title, category, gist string) {
	inSummary := false
	for _, ln := range strings.Split(md, "\n") {
		ln = strings.TrimSpace(ln)
		plain := strings.ReplaceAll(ln, "*", "")
		switch {
		case title == "" && strings.HasPrefix(ln, "# "):
			title = strings.TrimSpace(strings.TrimPrefix(ln, "# "))
		case category == "" && strings.HasPrefix(plain, "분류:"):
			category = strings.TrimSpace(strings.TrimPrefix(plain, "분류:"))
		case strings.HasPrefix(ln, "## "):
			inSummary = strings.Contains(ln, "요약")
		case inSummary && gist == "" && ln != "":
			gist = strings.TrimSpace(strings.TrimLeft(plain, "-0123456789. "))
		}
	}
	return
}

// MeetingSaveSummary writes the AI's write-up of the meeting txtPath next to
// it and returns the title found in it.
func (v *VoiceService) MeetingSaveSummary(txtPath, md string) (string, error) {
	p, err := meetingFile(txtPath)
	if err != nil {
		return "", err
	}
	md = strings.TrimSpace(md)
	if err := os.WriteFile(meetingSummaryPath(p), []byte(md+"\n"), 0o644); err != nil {
		return "", err
	}
	title, _, _ := parseMeetingSummary(md)
	return title, nil
}
