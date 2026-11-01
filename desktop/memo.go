package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Voice memos and to-dos ("메모해 줘, 내일 견적서 확인", "할 일 추가: 보고서"):
// kept in <data dir>/memos.json rather than the WebView's storage, so they
// survive a cleared or rebuilt WebView profile.
const (
	memoFileName = "memos.json"
	maxMemos     = 200
	maxMemoChars = 300
)

type memo struct {
	ID   int       `json:"id"`
	Text string    `json:"text"`
	Todo bool      `json:"todo,omitempty"`
	At   time.Time `json:"at"`
}

var memoMu sync.Mutex

func memoPath() (string, error) {
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, memoFileName), nil
}

// loadMemos reads the list, oldest first; a missing file is an empty list.
func loadMemos() ([]memo, error) {
	p, err := memoPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return []memo{}, nil
	}
	if err != nil {
		return nil, err
	}
	var list []memo
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// saveMemos writes the list through a temporary file, so a crash mid-write
// leaves the old list rather than half of one.
func saveMemos(list []memo) error {
	p, err := memoPath()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func memosJSON(list []memo) string {
	b, _ := json.Marshal(list)
	return string(b)
}

// MemoList returns the memos as JSON, oldest first.
func (v *VoiceService) MemoList() (string, error) {
	memoMu.Lock()
	defer memoMu.Unlock()
	list, err := loadMemos()
	if err != nil {
		return "", err
	}
	return memosJSON(list), nil
}

// MemoAdd keeps a memo (todo: a thing to do) and returns the list as JSON.
// Past maxMemos the oldest go.
func (v *VoiceService) MemoAdd(text string, todo bool) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("빈 메모예요")
	}
	if r := []rune(text); len(r) > maxMemoChars {
		text = string(r[:maxMemoChars])
	}
	memoMu.Lock()
	defer memoMu.Unlock()
	list, err := loadMemos()
	if err != nil {
		return "", err
	}
	id := 1
	for _, m := range list {
		id = max(id, m.ID+1)
	}
	list = append(list, memo{ID: id, Text: text, Todo: todo, At: time.Now()})
	if over := len(list) - maxMemos; over > 0 {
		list = list[over:]
	}
	if err := saveMemos(list); err != nil {
		return "", err
	}
	return memosJSON(list), nil
}

// MemoDelete removes the memos with these ids (none: all of them) and returns
// the list left as JSON.
func (v *VoiceService) MemoDelete(ids []int) (string, error) {
	memoMu.Lock()
	defer memoMu.Unlock()
	list, err := loadMemos()
	if err != nil {
		return "", err
	}
	kept := []memo{}
	if len(ids) > 0 {
		drop := map[int]bool{}
		for _, id := range ids {
			drop[id] = true
		}
		for _, m := range list {
			if !drop[m.ID] {
				kept = append(kept, m)
			}
		}
	}
	if err := saveMemos(kept); err != nil {
		return "", err
	}
	return memosJSON(kept), nil
}
