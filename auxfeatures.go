package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Unified 3-state model for teleclaude's aglink helper features (aglink-chat
// relay, aglink-screen, aglink-web). All three use the same vocabulary so the UI
// can render them with one rule.
const (
	auxRunning = "running" // 🟢 actively running
	auxIdle    = "idle"    // ⚪ available but not in use — normal, NOT an error
	auxAbsent  = "absent"  // 🔴 not installed / unavailable
)

// auxFeature is one aglink helper feature in the unified list.
type auxFeature struct {
	Name    string `json:"name"`
	Label   string `json:"label"`
	State   string `json:"state"`
	Detail  string `json:"detail,omitempty"`
	Version string `json:"version,omitempty"`
}

// buildAuxFeatures describes aglink-chat, aglink-screen and aglink-web under one
// state model. relayClients is the current control-API client count;
// chatControlEnabled/chatControlAddr come from config. aglink-chat is never
// "absent": with no relay attached it is idle (the embedded web server alone is
// enough), not an error.
func buildAuxFeatures(relayClients int, chatControlEnabled bool, chatControlAddr string) []auxFeature {
	feats := make([]auxFeature, 0, 1+len(pluginNames))

	chat := auxFeature{Name: "aglink-chat", Label: "aglink-chat 릴레이"}
	switch {
	case relayClients > 0:
		chat.State = auxRunning
		chat.Detail = fmt.Sprintf("릴레이 %d개 접속 · 제어 API %s", relayClients, chatControlAddr)
	case chatControlEnabled:
		chat.State = auxIdle
		chat.Detail = "미접속 (내장 웹으로 충분) · 제어 API " + chatControlAddr
	default:
		chat.State = auxIdle
		chat.Detail = "미사용 (제어 API 꺼짐)"
	}
	feats = append(feats, chat)

	runStatuses, runKnown := pluginRunStatuses(pluginNames)
	var srcDir, parent string
	if exe, err := os.Executable(); err == nil {
		srcDir = filepath.Dir(exe)
		parent = filepath.Dir(srcDir)
	}
	for _, name := range pluginNames {
		f := auxFeature{Name: name, Label: name}

		installed := false
		if parent != "" {
			if fi, e := os.Stat(filepath.Join(parent, name)); e == nil && fi.IsDir() {
				installed = true
				f.Version = gitShortCommit(filepath.Join(parent, name))
			}
		}
		binary := false
		if srcDir != "" {
			if _, e := os.Stat(filepath.Join(srcDir, name+exeSuffix)); e == nil {
				binary = true
			}
		}
		running, runDetail := false, ""
		if runKnown {
			if pr, ok := runStatuses[name]; ok {
				running = pr.running
				runDetail = pr.detail()
			}
		}

		switch {
		case !installed && !binary:
			f.State = auxAbsent
			f.Detail = "설치 안 됨"
		case running:
			f.State = auxRunning
			f.Detail = joinDetail(runDetail, buildLabel(binary), sourceLabel(installed))
		default:
			f.State = auxIdle
			runLabel := "미실행"
			if !runKnown {
				runLabel = "실행 상태 확인 불가"
			}
			f.Detail = joinDetail(runLabel, buildLabel(binary), sourceLabel(installed))
		}
		feats = append(feats, f)
	}
	return feats
}

func buildLabel(binary bool) string {
	if binary {
		return "빌드 있음"
	}
	return "빌드 없음"
}

func sourceLabel(installed bool) string {
	if installed {
		return ""
	}
	return "소스 없음"
}

// joinDetail joins non-empty parts with " · ".
func joinDetail(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, " · ")
}
