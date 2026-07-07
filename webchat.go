package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// loadOrCreateWebToken returns cfgToken if set, otherwise reads (or creates and
// persists) ~/.teleclaude/web_chat.token with 0600 perms.
func loadOrCreateWebToken(cfgToken string) (string, error) {
	return loadOrCreateToken(cfgToken, "web_chat.token")
}

// loadOrCreateToken returns cfgToken if set, otherwise reads (or creates and
// persists) ~/.teleclaude/<filename> with 0600 perms. Used for both the web_chat
// and chat_control shared tokens.
func loadOrCreateToken(cfgToken, filename string) (string, error) {
	if cfgToken != "" {
		return cfgToken, nil
	}
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, filename)
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
	cfgPath     string             // config.yaml path (edit target; fixed, never client-supplied)
	holder      *ConfigHolder      // current effective config (secret restore / status)
	control     *chatControlServer // chat_control server, for aglink-chat connection status; nil when disabled
}

// wsFrame is the JSON envelope sent to browsers.
type wsFrame struct {
	Type    string `json:"type"` // "text" | "image" | "typing" | "done"
	Text    string `json:"text,omitempty"`
	Caption string `json:"caption,omitempty"`
	Data    string `json:"data,omitempty"` // base64 PNG for images
}

// inMsg is a message from the browser.
type inMsg struct {
	Type   string  `json:"type"` // "send" | "web_new" | "web_setdir" | "web_rename" | "web_delete"
	Text   string  `json:"text"`
	Target *Target `json:"target,omitempty"`
	ID     string  `json:"id,omitempty"`
	Path   string  `json:"path,omitempty"`
	Title  string  `json:"title,omitempty"`
}

type webConversationTopic struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Summary      string `json:"summary,omitempty"`
	Started      bool   `json:"started"`
	Backend      string `json:"backend,omitempty"`
	Active       bool   `json:"active"`
	LastActivity string `json:"lastActivity,omitempty"`
	// Channel is the conversation series' origin: "web" for chats explicitly
	// created from the web UI (shown only in the web sidebar), "telegram" for
	// everything else (legacy/shared/telegram). The web UI shows web + the active
	// conversation by default and tucks the rest behind a "telegram" toggle.
	Channel string `json:"channel"`
}

// conversationChannel maps a conversation's Origin to a display channel. Empty
// Origin (legacy/auto-created/telegram) is treated as "telegram"; only an
// explicit "web" origin counts as web.
func conversationChannel(c *Conversation) string {
	if c != nil && c.Origin == OriginWeb {
		return OriginWeb
	}
	return OriginTelegram
}

type webProjectTopics struct {
	Name          string                 `json:"name"`
	Path          string                 `json:"path"`
	Conversations []webConversationTopic `json:"conversations"`
}

type webTelegramEntry struct {
	Title   string `json:"title"`
	ID      string `json:"id"`
	Active  bool   `json:"active"`
	Backend string `json:"backend,omitempty"`
	Project string `json:"project,omitempty"` // current working-dir project
}

type webConversationsResponse struct {
	Active   ActiveRef          `json:"active"`
	Telegram *webTelegramEntry  `json:"telegram,omitempty"`
	Projects []webProjectTopics `json:"projects"`
	WebConvs []webWebConv       `json:"webConvs"`
}

// webWebConv is a top-level web conversation entry (Task 2's store.WebConvs),
// distinct from webProjectTopics/webConversationTopic which describe
// project-scoped (Telegram-origin) conversation trees.
type webWebConv struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	WorkDir string `json:"workDir,omitempty"`
	Active  bool   `json:"active"`
	Backend string `json:"backend,omitempty"`
}

type webConversationGroup struct {
	root     *Conversation
	selected *Conversation
	active   bool
	started  bool
	last     time.Time
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

func (w *webChannel) Send(_ int64, text string) error {
	w.push(wsFrame{Type: "text", Text: text})
	return nil
}
func (w *webChannel) SendPhoto(_ int64, png []byte, caption string) error {
	w.push(wsFrame{Type: "image", Caption: caption, Data: base64.StdEncoding.EncodeToString(png)})
	return nil
}
func (w *webChannel) Typing(_ int64) { w.push(wsFrame{Type: "typing"}) }
func (w *webChannel) Done(_ int64)   { w.push(wsFrame{Type: "done"}) }

// EchoUser shows input that came from Telegram as a user bubble on the web side.
// web-origin is a no-op — the sending tab already rendered it locally, and other
// tabs don't mirror each other's typed input.
func (w *webChannel) EchoUser(_ int64, text, origin string) {
	if origin == OriginTelegram {
		w.push(wsFrame{Type: "user", Text: text})
	}
}

// inject feeds a browser message into the same pipeline Telegram uses.
// Non-command text is subject to the same per-user rate limit as the
// Telegram path (design §3.3); commands are never rate-limited. tgt, when
// non-nil, routes the send to an explicit target (telegram stream or a web
// topic) via dispatchTargeted rather than the LLM-routed dispatchText path.
func (s *webServer) inject(text string, tgt *Target) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if strings.HasPrefix(text, "!") {
		s.bot.handleCommand(s.ownerChatID, text, OriginWeb)
		return
	}
	if s.bot.rateLimiter != nil && !s.bot.rateLimiter.Allow(s.ownerChatID) {
		_ = s.bot.Send(s.ownerChatID, "⚠️ 요청이 너무 많습니다. 잠시 후 다시 시도해 주세요.")
		return
	}
	s.bot.dispatchTargeted(s.ownerChatID, text, tgt)
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
		switch m.Type {
		case "send":
			go s.inject(m.Text, m.Target)
		case "web_new":
			go s.bot.webNew(s.ownerChatID, m.Title)
		case "web_setdir":
			go s.bot.webSetDir(s.ownerChatID, m.ID, m.Path)
		case "web_rename":
			go s.bot.webRename(s.ownerChatID, m.ID, m.Title)
		case "web_delete":
			go s.bot.webDelete(s.ownerChatID, m.ID)
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
	// Inject the current valid token so the page always authenticates its WS/API
	// calls, regardless of the URL's ?token=, stale localStorage, or which loopback
	// host was opened. Safe: the endpoint is loopback-only and same-origin — a
	// cross-origin page cannot read this response (CORS), and any local process
	// could already read the token file. json.Marshal escapes it for the <script>.
	tokJSON, _ := json.Marshal(s.token)
	inject := []byte("<script>window.__TC_TOKEN__=" + string(tokJSON) + ";</script></head>")
	b = bytes.Replace(b, []byte("</head>"), inject, 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, must-revalidate")
	_, _ = w.Write(b)
}

func (s *webServer) handleConversations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !s.authOK(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.bot == nil || s.bot.store == nil {
		http.Error(w, "conversation store unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(buildConversationsResponse(s.bot.store))
}

func (s *webServer) handleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !s.authOK(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.bot == nil || s.bot.store == nil {
		http.Error(w, "conversation store unavailable", http.StatusInternalServerError)
		return
	}
	tgt := Target{Kind: r.URL.Query().Get("kind"), Project: r.URL.Query().Get("project"), ID: r.URL.Query().Get("id")}
	if tgt.Kind == "" {
		tgt.Kind = "telegram"
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(buildHistoryResponse(s.bot.store, tgt))
}

// buildConversationsResponse assembles the /api/conversations payload (project +
// grouped-topic list with per-topic channel tags). Shared by the embedded web
// server and the chat-control API so both report identical data.
func buildConversationsResponse(store StoreRepo) webConversationsResponse {
	active := store.GetActive()
	projects := store.ListProjects()
	names := make([]string, 0, len(projects))
	for name := range projects {
		names = append(names, name)
	}
	sort.Strings(names)

	resp := webConversationsResponse{Active: active, Projects: make([]webProjectTopics, 0, len(names))}
	for _, name := range names {
		p := projects[name]
		item := webProjectTopics{Name: name, Path: p.Path}
		item.Conversations = webTopicsForProject(name, p.Conversations, active)
		resp.Projects = append(resp.Projects, item)
	}
	if tc := store.TelegramConversation(); tc != nil {
		resp.Telegram = &webTelegramEntry{
			Title:   tc.Title,
			ID:      tc.ID,
			Backend: tc.Backend,
			Project: store.TelegramActiveProject(),
		}
	}

	webConvs := store.ListWebConvs()
	ids := make([]string, 0, len(webConvs))
	for id := range webConvs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return webConvs[ids[i]].LastActivity.After(webConvs[ids[j]].LastActivity) })
	resp.WebConvs = make([]webWebConv, 0, len(ids))
	for _, id := range ids {
		c := webConvs[id]
		resp.WebConvs = append(resp.WebConvs, webWebConv{
			ID: c.ID, Title: c.Title, WorkDir: c.WorkDir,
			Active:  active.Project == "" && active.ConversationID == c.ID,
			Backend: c.Backend,
		})
	}
	return resp
}

type historyTurn struct {
	Role string `json:"role"` // "user" | "assistant"
	Text string `json:"text"`
}
type historyResponse struct {
	Turns []historyTurn `json:"turns"`
}

// buildHistoryResponse expands a conversation's stored turns into a flat
// user/assistant sequence for the web log. Shared by /api/history and the
// chat-control get_history request.
func buildHistoryResponse(store StoreRepo, tgt Target) historyResponse {
	resp := historyResponse{Turns: []historyTurn{}}
	// Take a copy of the history under the store lock instead of reaching into
	// a live *Conversation — a worker may concurrently be appending to (and
	// reslicing) the same conversation's History slice, especially for the
	// shared global telegram conversation.
	turns := store.HistorySnapshot(tgt)
	for _, turn := range turns {
		if turn.Prompt != "" {
			resp.Turns = append(resp.Turns, historyTurn{Role: "user", Text: turn.Prompt})
		}
		if turn.Response != "" {
			resp.Turns = append(resp.Turns, historyTurn{Role: "assistant", Text: turn.Response})
		}
	}
	return resp
}

func webTopicsForProject(project string, convs map[string]*Conversation, active ActiveRef) []webConversationTopic {
	groups := map[string]*webConversationGroup{}
	for _, id := range sortedConvIDsByActivity(convs) {
		c := convs[id]
		rootID := webConversationRootID(convs, c)
		root := convs[rootID]
		if root == nil {
			root = c
		}
		g := groups[rootID]
		if g == nil {
			g = &webConversationGroup{root: root, selected: c, last: c.LastActivity}
			groups[rootID] = g
		}
		if c.LastActivity.After(g.last) {
			g.last = c.LastActivity
		}
		g.started = g.started || c.Started

		isActive := project == active.Project && c.ID == active.ConversationID
		if isActive {
			g.active = true
			g.selected = c
			continue
		}
		if !g.active && c.LastActivity.After(g.selected.LastActivity) {
			g.selected = c
		}
	}

	ordered := make([]*webConversationGroup, 0, len(groups))
	for _, g := range groups {
		ordered = append(ordered, g)
	}
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].last.After(ordered[j].last)
	})

	topics := make([]webConversationTopic, 0, len(ordered))
	for _, g := range ordered {
		c := g.selected
		if c == nil {
			continue
		}
		title := c.Title
		if g.root != nil && g.root.Title != "" {
			title = g.root.Title
		}
		lastActivity := ""
		if !g.last.IsZero() {
			lastActivity = g.last.UTC().Format(time.RFC3339)
		}
		topics = append(topics, webConversationTopic{
			ID:           c.ID,
			Title:        title,
			Summary:      c.Summary,
			Started:      g.started,
			Backend:      c.Backend,
			Active:       g.active,
			LastActivity: lastActivity,
			Channel:      conversationChannel(g.root),
		})
	}
	return topics
}

func webConversationRootID(convs map[string]*Conversation, c *Conversation) string {
	if c == nil {
		return ""
	}
	rootID := c.ID
	seen := map[string]bool{}
	cur := c
	for cur != nil && cur.ParentID != "" {
		if seen[cur.ID] {
			break
		}
		seen[cur.ID] = true
		parent, ok := convs[cur.ParentID]
		if !ok {
			break
		}
		rootID = parent.ID
		cur = parent
	}
	return rootID
}

// resolveWebOwner picks the chatID web actions run as: the explicit config value,
// else the first allowed user ID; ok=false when neither is set (web chat disabled).
func resolveWebOwner(ownerCfg int64, allowed []int64) (int64, bool) {
	if ownerCfg != 0 {
		return ownerCfg, true
	}
	if len(allowed) > 0 {
		return allowed[0], true
	}
	return 0, false
}

func (s *webServer) Start() {
	staticSub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Printf("[webchat] embed error: %v — web chat disabled", err)
		return
	}
	// noStore forces the browser to always refetch web assets. go:embed + FileServer
	// serve weak cache validators, so without this a normal refresh keeps a stale
	// app.js after a rebuild — the browser ends up running old client code against a
	// new server. Assets are tiny and loopback-only, so no-store costs nothing.
	noStore := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store, must-revalidate")
			h.ServeHTTP(w, r)
		})
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/api/upload", s.handleUpload)
	mux.HandleFunc("/api/conversations", s.handleConversations)
	mux.HandleFunc("/api/history", s.handleHistory)
	mux.HandleFunc("/api/capabilities", s.handleCapabilities)
	mux.HandleFunc("/api/version", s.handleVersion)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/aux", s.handleAux)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.Handle("/static/", noStore(http.StripPrefix("/static/", http.FileServer(http.FS(staticSub)))))
	mux.HandleFunc("/", s.handleIndex)

	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		log.Printf("[webchat] listen %s failed: %v — web chat disabled", s.addr, err)
		return
	}
	log.Printf("[webchat] http://%s/?token=%s", s.addr, s.token)
	srv := &http.Server{Handler: mux}

	// Also serve the IPv6 loopback (::1) so a browser that resolves "localhost"
	// to IPv6 — common on Windows/Chrome — can connect. Best-effort: IPv4 still
	// works if this bind fails (e.g. IPv6 disabled).
	if v6 := ipv6LoopbackAddr(s.addr); v6 != "" {
		if ln6, err6 := net.Listen("tcp", v6); err6 != nil {
			log.Printf("[webchat] IPv6 loopback %s not bound: %v (IPv4 still served)", v6, err6)
		} else {
			log.Printf("[webchat] also http://%s/", v6)
			go func() { _ = srv.Serve(ln6) }()
		}
	}

	if serr := srv.Serve(ln); serr != nil {
		log.Printf("[webchat] server stopped: %v", serr)
	}
}

// ipv6LoopbackAddr returns the "[::1]:port" form of an IPv4-loopback / localhost
// listen address like "127.0.0.1:1717", or "" if addr is not one we should
// mirror onto IPv6.
func ipv6LoopbackAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	if host == "127.0.0.1" || strings.EqualFold(host, "localhost") {
		return net.JoinHostPort("::1", port)
	}
	return ""
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
	// Parts larger than the in-memory threshold above spill to temp files that
	// Go does not clean up automatically; remove them once the handler returns.
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()
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
	s.bot.ingestAttachment(s.ownerChatID, savePath, r.FormValue("caption"), OriginWeb)
	w.WriteHeader(http.StatusNoContent)
}

// versionPayload builds the running-vs-latest-source version report, shared by
// the embedded web server and the control-API relay so both report identical
// data. "latest" is computed live from the binary's own source dir (git commit
// count of HEAD), so the UI can flag when the running build is behind the tree.
// backend, when non-empty, is included as the active runner backend.
func versionPayload(backend string) map[string]any {
	p := map[string]any{
		"version":     runningVersion(),
		"commit":      buildCommit,
		"buildTime":   buildTime,
		"commitCount": atoiOr(buildCommitCount, 0),
	}
	if backend != "" {
		p["backend"] = backend
	}
	if exe, err := os.Executable(); err == nil {
		srcDir := filepath.Dir(exe)
		if latest := gitCommitCount(srcDir); latest != "" {
			p["latestCommitCount"] = atoiOr(latest, 0)
			p["latestVersion"] = formatVersion(latest)
			p["latestCommit"] = gitShortCommit(srcDir)
			// updateAvailable only when the running build is itself stamped
			// (a dev build has no meaningful count to compare).
			if buildCommitCount != "" {
				p["updateAvailable"] = atoiOr(latest, 0) > atoiOr(buildCommitCount, 0)
			}
		}
	}
	return p
}

// readMaskedConfig reads cfgPath and masks its secret values for display.
func readMaskedConfig(cfgPath string, cfg *Config) ([]byte, error) {
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, err
	}
	return maskConfigSecrets(raw, cfg), nil
}

// writeValidatedConfig restores masked secrets in body, validates it, and — only
// if valid — writes it to cfgPath (the fixed path; never client-supplied). A
// non-nil error means nothing was written.
func writeValidatedConfig(cfgPath string, cfg *Config, body []byte) error {
	restored := restoreConfigSecrets(body, cfg)
	if _, verr := unmarshalConfigYAML(restored); verr != nil {
		return verr
	}
	return os.WriteFile(cfgPath, restored, 0o600)
}

func (s *webServer) backendName() string {
	if s.bot != nil && s.bot.manager != nil {
		return s.bot.manager.Backend()
	}
	return ""
}

// handleCapabilities tells the shared app.js this is the admin-capable embedded
// server (aglink-chat does not register this route, so its 404 hides admin UI).
// It carries the full version payload so the badge can render the update flag
// without a second request.
func (s *webServer) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !s.authOK(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	p := versionPayload("")
	p["admin"] = true
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(p)
}

// handleVersion reports the running binary's version + build stamp, the latest
// source version, and the active backend.
func (s *webServer) handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !s.authOK(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(versionPayload(s.backendName()))
}

// handleStatus reports the web/control listen addresses and whether an
// aglink-chat control client is currently connected.
func (s *webServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !s.authOK(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	clients := 0
	if s.control != nil {
		clients = int(s.control.connCount.Load())
	}
	resp := map[string]any{"aglinkClients": clients, "aglinkConnected": clients > 0}
	if s.holder != nil {
		cfg := s.holder.Get()
		resp["webChatAddr"] = cfg.WebChatAddr
		resp["chatControlEnabled"] = cfg.ChatControl
		resp["chatControlAddr"] = cfg.ChatControlAddr
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleAux reports teleclaude's aglink helper features (aglink-chat relay,
// aglink-screen, aglink-web) under one unified 3-state model (running/idle/
// absent) so the UI renders all three the same way.
func (s *webServer) handleAux(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !s.authOK(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	clients := 0
	if s.control != nil {
		clients = int(s.control.connCount.Load())
	}
	enabled, addr := false, ""
	if s.holder != nil {
		cfg := s.holder.Get()
		enabled = cfg.ChatControl
		addr = cfg.ChatControlAddr
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"features": buildAuxFeatures(clients, enabled, addr)})
}

// handleConfig serves (GET, secret-masked) and saves (PUT, validated) the
// config.yaml file. Writes go only to s.cfgPath — the client never supplies a
// path. A saved file is picked up by the fsnotify hot-reload watcher.
func (s *webServer) handleConfig(w http.ResponseWriter, r *http.Request) {
	if !s.authOK(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.cfgPath == "" || s.holder == nil {
		http.Error(w, "config editing unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		masked, err := readMaskedConfig(s.cfgPath, s.holder.Get())
		if err != nil {
			http.Error(w, "read failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(masked)
	case http.MethodPut:
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MiB cap
		if err != nil {
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}
		if werr := writeValidatedConfig(s.cfgPath, s.holder.Get(), body); werr != nil {
			http.Error(w, werr.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent) // fsnotify (confighot.go) hot-reloads
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
