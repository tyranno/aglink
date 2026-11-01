package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Voice conversation for VS Code: the aglink-vscode extension borrows this
// app's voice — the wake word, Whisper, the enrolled-voice check and the
// Supertonic reading — for Claude Code running in a VS Code window.
//
//   - The extension holds GET /vscode/events open (server-sent events) and
//     turns voice on or off with POST /vscode/attach.
//   - What the user says goes to the extension as a "prompt" event; it puts the
//     words in Claude Code's input box and calls POST /vscode/paste, which
//     brings the window forward and presses Ctrl+V, Enter there.
//   - Claude Code's Stop hook posts the finished turn to POST /vscode/stop; its
//     last answer is read out like a desktop reply and the mic opens again.
//
// Only 127.0.0.1 is served, on a fixed port so the hook needs no lookup.
const vscodeVoicePort = 48230

type vscodeClient struct {
	folder string
	title  string
	pid    int
	events chan map[string]any
}

var vscodeVoice struct {
	mu       sync.Mutex
	clients  map[*vscodeClient]bool
	attached *vscodeClient
}

func setupVSCodeVoice(app *application.App) {
	vscodeVoice.clients = map[*vscodeClient]bool{}
	app.Event.On("vscode:prompt", func(e *application.CustomEvent) {
		m, _ := e.Data.(map[string]any)
		text, _ := m["text"].(string)
		vscodeVoice.mu.Lock()
		c := vscodeVoice.attached
		vscodeVoice.mu.Unlock()
		if c == nil {
			app.Event.Emit("vscode:sent", map[string]any{"ok": false, "error": "VS Code 가 연결되어 있지 않아요"})
			return
		}
		push(c, map[string]any{"type": "prompt", "text": text})
	})
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", vscodeVoicePort))
	if err != nil {
		log.Printf("[vscode] voice bridge off: %v", err)
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /vscode/events", func(w http.ResponseWriter, r *http.Request) { vscodeEvents(app, w, r) })
	mux.HandleFunc("POST /vscode/attach", func(w http.ResponseWriter, r *http.Request) { vscodeAttach(app, w, r) })
	mux.HandleFunc("POST /vscode/paste", func(w http.ResponseWriter, r *http.Request) { vscodePaste(app, w, r) })
	mux.HandleFunc("POST /vscode/stop", func(w http.ResponseWriter, r *http.Request) { vscodeStop(app, w, r) })
	go func() { _ = http.Serve(l, mux) }()
	log.Printf("[vscode] voice bridge on 127.0.0.1:%d", vscodeVoicePort)
}

func push(c *vscodeClient, ev map[string]any) {
	select {
	case c.events <- ev:
	default: // a stalled window misses it rather than holding up the voice
	}
}

func vscodeEvents(app *application.App, w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no streaming", http.StatusInternalServerError)
		return
	}
	q := r.URL.Query()
	c := &vscodeClient{folder: q.Get("folder"), title: q.Get("title"), events: make(chan map[string]any, 8)}
	fmt.Sscan(q.Get("pid"), &c.pid)
	vscodeVoice.mu.Lock()
	vscodeVoice.clients[c] = true
	vscodeVoice.mu.Unlock()
	defer func() {
		vscodeVoice.mu.Lock()
		delete(vscodeVoice.clients, c)
		was := vscodeVoice.attached == c
		if was {
			vscodeVoice.attached = nil
		}
		vscodeVoice.mu.Unlock()
		if was {
			app.Event.Emit("vscode:attach", map[string]any{"on": false})
		}
	}()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	fmt.Fprint(w, ": hi\n\n")
	fl.Flush()
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			fmt.Fprint(w, ": ping\n\n")
		case ev := <-c.events:
			b, _ := json.Marshal(ev)
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		fl.Flush()
	}
}

// clientFor finds the event stream of the window that sent a request: the same
// VS Code process and folder.
func clientFor(pid int, folder string) *vscodeClient {
	vscodeVoice.mu.Lock()
	defer vscodeVoice.mu.Unlock()
	for c := range vscodeVoice.clients {
		if c.pid == pid && c.folder == folder {
			return c
		}
	}
	return nil
}

type vscodeReq struct {
	On     bool   `json:"on"`
	Folder string `json:"folder"`
	Title  string `json:"title"`
	PID    int    `json:"pid"`
}

func readReq(r *http.Request) (vscodeReq, error) {
	var q vscodeReq
	err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&q)
	return q, err
}

func vscodeAttach(app *application.App, w http.ResponseWriter, r *http.Request) {
	q, err := readReq(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	c := clientFor(q.PID, q.Folder)
	if q.On && c == nil {
		http.Error(w, "event stream not open", http.StatusConflict)
		return
	}
	vscodeVoice.mu.Lock()
	prev := vscodeVoice.attached
	if q.On {
		vscodeVoice.attached = c
	} else if prev == c || c == nil {
		vscodeVoice.attached = nil
	}
	vscodeVoice.mu.Unlock()
	// Another window had the voice: it is told it has lost it.
	if q.On && prev != nil && prev != c {
		push(prev, map[string]any{"type": "attached", "on": false})
	}
	app.Event.Emit("vscode:attach", map[string]any{"on": q.On, "folder": q.Folder, "title": q.Title})
	log.Printf("[vscode] voice %v for %s", map[bool]string{true: "on", false: "off"}[q.On], q.Title)
	w.WriteHeader(http.StatusNoContent)
}

func vscodePaste(app *application.App, w http.ResponseWriter, r *http.Request) {
	q, err := readReq(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	err = pasteAndSend(q.PID, q.Title)
	res := map[string]any{"ok": err == nil}
	if err != nil {
		res["error"] = err.Error()
		log.Printf("[vscode] paste failed: %v", err)
	}
	app.Event.Emit("vscode:sent", res)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

// stopHook is the part of Claude Code's Stop hook input used here.
type stopHook struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	Last      string `json:"last_assistant_message"`
}

func vscodeStop(app *application.App, w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
	var h stopHook
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&h); err != nil || strings.TrimSpace(h.Last) == "" {
		return
	}
	vscodeVoice.mu.Lock()
	c := vscodeVoice.attached
	vscodeVoice.mu.Unlock()
	// Every Claude Code turn on this PC runs the hook; only the attached
	// window's folder is read out (the page also drops turns nobody asked for).
	if c == nil || !sameFolder(h.Cwd, c.folder) {
		return
	}
	app.Event.Emit("vscode:reply", map[string]any{"text": h.Last, "session": h.SessionID})
}

// sameFolder reports whether cwd is folder or inside it.
func sameFolder(cwd, folder string) bool {
	if folder == "" {
		return true
	}
	a := strings.ToLower(filepath.Clean(cwd))
	b := strings.ToLower(filepath.Clean(folder))
	return a == b || strings.HasPrefix(a, b+string(filepath.Separator))
}

var errNoVSCodeWindow = errors.New("VS Code 창을 찾지 못했어요")
