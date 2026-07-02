package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// loadOrCreateWebToken returns cfgToken if set, otherwise reads (or creates and
// persists) ~/.teleclaude/web_chat.token with 0600 perms.
func loadOrCreateWebToken(cfgToken string) (string, error) {
	if cfgToken != "" {
		return cfgToken, nil
	}
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, "web_chat.token")
	if b, rerr := os.ReadFile(p); rerr == nil {
		if tok := strings.TrimSpace(string(b)); tok != "" {
			return tok, nil
		}
	}
	buf := make([]byte, 24)
	if _, rerr := rand.Read(buf); rerr != nil {
		return "", rerr
	}
	tok := hex.EncodeToString(buf)
	if werr := os.WriteFile(p, []byte(tok), 0o600); werr != nil {
		return "", werr
	}
	return tok, nil
}

// tokenOK checks the request token (query ?token= or "Authorization: Bearer <t>")
// against want using a constant-time comparison.
func tokenOK(r *http.Request, want string) bool {
	got := r.URL.Query().Get("token")
	if got == "" {
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			got = strings.TrimPrefix(h, "Bearer ")
		}
	}
	if got == "" || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// originOK guards against cross-site requests: allow when there is no Origin
// header (non-browser / same-origin) or when the Origin host is loopback.
func originOK(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "127.0.0.1" || host == "localhost"
}

//go:embed web
var webFS embed.FS

type webServer struct {
	addr        string
	token       string
	ownerChatID int64
	hub         *Hub
	bot         *Bot
}

// wsFrame is the JSON envelope sent to browsers.
type wsFrame struct {
	Type    string `json:"type"`              // "text" | "image" | "typing"
	Text    string `json:"text,omitempty"`
	Caption string `json:"caption,omitempty"`
	Data    string `json:"data,omitempty"` // base64 PNG for images
}

// inMsg is a message from the browser.
type inMsg struct {
	Type string `json:"type"` // "send"
	Text string `json:"text"`
}

// webChannel is one browser connection as a ChannelSender. Frames go through a
// buffered channel drained by a writer goroutine; if the buffer fills (slow
// client) the connection is closed rather than blocking the Hub.
type webChannel struct {
	send      chan wsFrame
	closeOnce sync.Once
	cancel    context.CancelFunc
}

func (w *webChannel) push(f wsFrame) {
	select {
	case w.send <- f:
	default:
		w.close()
	}
}
func (w *webChannel) close() { w.closeOnce.Do(func() { w.cancel() }) }

func (w *webChannel) Send(_ int64, text string) error { w.push(wsFrame{Type: "text", Text: text}); return nil }
func (w *webChannel) SendPhoto(_ int64, png []byte, caption string) error {
	w.push(wsFrame{Type: "image", Caption: caption, Data: base64.StdEncoding.EncodeToString(png)})
	return nil
}
func (w *webChannel) Typing(_ int64) { w.push(wsFrame{Type: "typing"}) }

// inject feeds a browser message into the same pipeline Telegram uses.
func (s *webServer) inject(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if strings.HasPrefix(text, "!") {
		s.bot.handleCommand(s.ownerChatID, text)
	} else {
		s.bot.dispatchText(s.ownerChatID, text)
	}
}

func (s *webServer) authOK(r *http.Request) bool { return originOK(r) && tokenOK(r, s.token) }

func (s *webServer) handleWS(w http.ResponseWriter, r *http.Request) {
	if !s.authOK(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"127.0.0.1:*", "localhost:*"},
	})
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch := &webChannel{send: make(chan wsFrame, 64), cancel: cancel}
	s.hub.Register(s.ownerChatID, ch)
	defer s.hub.Unregister(s.ownerChatID, ch)
	defer c.Close(websocket.StatusNormalClosure, "")

	// Writer goroutine.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case f := <-ch.send:
				wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
				werr := wsjson.Write(wctx, c, f)
				wcancel()
				if werr != nil {
					cancel()
					return
				}
			}
		}
	}()

	// Reader loop (blocks until disconnect).
	for {
		var m inMsg
		if rerr := wsjson.Read(ctx, c, &m); rerr != nil {
			break
		}
		if m.Type == "send" {
			go s.inject(m.Text)
		}
	}
	cancel()
}

func (s *webServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	b, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "ui missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

func (s *webServer) Start() {
	staticSub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Printf("[webchat] embed error: %v — web chat disabled", err)
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/api/upload", s.handleUpload)
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))
	mux.HandleFunc("/", s.handleIndex)

	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		log.Printf("[webchat] listen %s failed: %v — web chat disabled", s.addr, err)
		return
	}
	log.Printf("[webchat] http://%s/?token=%s", s.addr, s.token)
	srv := &http.Server{Handler: mux}
	if serr := srv.Serve(ln); serr != nil {
		log.Printf("[webchat] server stopped: %v", serr)
	}
}

// handleUpload saves an uploaded multipart file under ~/.teleclaude/attachments
// and feeds it into the shared ingestAttachment pipeline.
func (s *webServer) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.authOK(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "no file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	home, err := os.UserHomeDir()
	if err != nil {
		http.Error(w, "no home", http.StatusInternalServerError)
		return
	}
	dir := filepath.Join(home, ".teleclaude", "attachments")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		http.Error(w, "mkdir failed", http.StatusInternalServerError)
		return
	}
	ext := filepath.Ext(hdr.Filename)
	savePath := filepath.Join(dir, fmt.Sprintf("%d%s", time.Now().UnixMilli(), ext))
	out, err := os.Create(savePath)
	if err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	defer out.Close()
	if _, err := io.Copy(out, file); err != nil {
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	s.bot.ingestAttachment(s.ownerChatID, savePath, r.FormValue("caption"))
	w.WriteHeader(http.StatusNoContent)
}
