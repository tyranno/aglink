package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// wsFrame is teleclaude's browser-facing frame envelope (relayed to the UI as a
// Wails "frame" event).
type wsFrame struct {
	Type    string          `json:"type"` // text | image | typing | done | user
	Text    string          `json:"text,omitempty"`
	Caption string          `json:"caption,omitempty"`
	Data    string          `json:"data,omitempty"` // base64 PNG
	Target  json.RawMessage `json:"target,omitempty"`
}

// controlOut is what teleclaude's control API sends us.
type controlOut struct {
	Kind  string          `json:"kind"` // frame | reply
	Frame *wsFrame        `json:"frame,omitempty"`
	ReqID string          `json:"reqID,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// controlIn is what we send to teleclaude's control API.
type controlIn struct {
	Type    string          `json:"type"`
	ReqID   string          `json:"reqID,omitempty"`
	Text    string          `json:"text,omitempty"`
	Caption string          `json:"caption,omitempty"`
	Origin  string          `json:"origin,omitempty"`
	Path    string          `json:"path,omitempty"`
	ID      string          `json:"id,omitempty"`
	Title   string          `json:"title,omitempty"`
	Backend string          `json:"backend,omitempty"`
	Body    string          `json:"body,omitempty"`
	Target  json.RawMessage `json:"target,omitempty"`
}

// ControlService is the Wails-bound service the Svelte frontend calls. It keeps a
// persistent client connection to teleclaude's control API (the same relay role
// aglink-chat plays) and forwards live frames to the UI as Wails events:
//   - "frame"          → a wsFrame (assistant text/image, typing, done, echo)
//   - "control:status" → bool (connected?)
type ControlService struct {
	addr, token string

	writeMu   sync.Mutex
	connMu    sync.RWMutex
	conn      *websocket.Conn
	connected atomic.Bool

	pendMu  sync.Mutex
	pending map[string]chan json.RawMessage
	seq     atomic.Uint64
}

func NewControlService() *ControlService {
	tok := ""
	if b, err := os.ReadFile(controlTokenPath()); err == nil {
		tok = strings.TrimSpace(string(b))
	}
	return &ControlService{
		addr:    "127.0.0.1:17170",
		token:   tok,
		pending: map[string]chan json.RawMessage{},
	}
}

func controlTokenPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".teleclaude", "chat_control.token")
}

// ServiceStartup (Wails lifecycle hook) launches the control-client loop.
func (c *ControlService) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	go c.run(ctx)
	return nil
}

func (c *ControlService) run(ctx context.Context) {
	backoff := 500 * time.Millisecond
	for ctx.Err() == nil {
		err := c.connectOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		c.connected.Store(false)
		c.emit("control:status", false)
		log.Printf("[control] disconnected (%v); reconnecting in %s", err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 10*time.Second)
	}
}

func (c *ControlService) connectOnce(ctx context.Context) error {
	u := "ws://" + c.addr + "/control?token=" + url.QueryEscape(c.token)
	dctx, dcancel := context.WithTimeout(ctx, 10*time.Second)
	conn, _, err := websocket.Dial(dctx, u, nil)
	dcancel()
	if err != nil {
		return err
	}
	// Full stored-history replies can exceed coder/websocket's 32KiB default
	// read limit, especially the shared Telegram stream. Match aglink-chat's
	// control client so desktop can render the same persisted conversation data.
	conn.SetReadLimit(8 << 20)
	c.setConn(conn)
	defer c.setConn(nil)
	defer conn.Close(websocket.StatusNormalClosure, "")
	c.connected.Store(true)
	c.emit("control:status", true)
	log.Printf("[control] connected to %s", c.addr)

	for {
		var o controlOut
		if rerr := wsjson.Read(ctx, conn, &o); rerr != nil {
			return rerr
		}
		switch o.Kind {
		case "frame":
			if o.Frame != nil {
				c.emit("frame", o.Frame)
			}
		case "reply":
			c.deliverReply(o.ReqID, o.Data)
		}
	}
}

func (c *ControlService) emit(name string, data any) {
	if app := application.Get(); app != nil {
		app.Event.Emit(name, data)
	}
}

func (c *ControlService) setConn(conn *websocket.Conn) {
	c.connMu.Lock()
	c.conn = conn
	c.connMu.Unlock()
}
func (c *ControlService) getConn() *websocket.Conn {
	c.connMu.RLock()
	defer c.connMu.RUnlock()
	return c.conn
}

func (c *ControlService) send(m controlIn) error {
	conn := c.getConn()
	if conn == nil {
		return errors.New("control API not connected")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	wctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return wsjson.Write(wctx, conn, m)
}

// request sends with a fresh reqID and waits for the matching reply.
func (c *ControlService) request(m controlIn) (json.RawMessage, error) {
	id := fmt.Sprintf("r%d", c.seq.Add(1))
	m.ReqID = id
	rc := make(chan json.RawMessage, 1)
	c.pendMu.Lock()
	c.pending[id] = rc
	c.pendMu.Unlock()
	defer func() {
		c.pendMu.Lock()
		delete(c.pending, id)
		c.pendMu.Unlock()
	}()
	if err := c.send(m); err != nil {
		return nil, err
	}
	select {
	case data := <-rc:
		return data, nil
	case <-time.After(10 * time.Second):
		return nil, errors.New("control request timed out")
	}
}

func (c *ControlService) deliverReply(id string, data json.RawMessage) {
	c.pendMu.Lock()
	rc := c.pending[id]
	c.pendMu.Unlock()
	if rc != nil {
		select {
		case rc <- data:
		default:
		}
	}
}

func targetJSON(kind, project, id string) json.RawMessage {
	m := map[string]string{"kind": kind}
	if project != "" {
		m["project"] = project
	}
	if id != "" {
		m["id"] = id
	}
	b, _ := json.Marshal(m)
	return b
}

func uploadAttachmentControlIn(path, caption, kind, id string) controlIn {
	return controlIn{
		Type:    "upload_attachment",
		Path:    path,
		Caption: caption,
		Origin:  "web",
		Target:  targetJSON(kind, "", id),
	}
}

func clipboardImageExtension(mimeType string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/png":
		return ".png", true
	case "image/jpeg", "image/jpg":
		return ".jpg", true
	case "image/gif":
		return ".gif", true
	case "image/webp":
		return ".webp", true
	case "image/bmp":
		return ".bmp", true
	case "image/svg+xml":
		return ".svg", true
	default:
		return "", false
	}
}

func attachmentImageMimeType(path string) (string, bool) {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(path))) {
	case ".png":
		return "image/png", true
	case ".jpg", ".jpeg":
		return "image/jpeg", true
	case ".gif":
		return "image/gif", true
	case ".webp":
		return "image/webp", true
	case ".bmp":
		return "image/bmp", true
	case ".svg":
		return "image/svg+xml", true
	default:
		return "", false
	}
}

func attachmentsStagingDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, ".teleclaude", "attachments"), nil
}

func writeStagedAttachmentBytes(data []byte, ext string) (string, error) {
	if len(data) == 0 {
		return "", errors.New("attachment is empty")
	}
	dir, err := attachmentsStagingDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create attachments dir: %w", err)
	}
	if ext == "" {
		ext = ".bin"
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	file, err := os.CreateTemp(dir, "aglink-desktop-*"+ext)
	if err != nil {
		return "", fmt.Errorf("create staged attachment: %w", err)
	}

	path := file.Name()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write staged attachment: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close staged attachment: %w", err)
	}
	return path, nil
}

func decodeClipboardImageDataURL(dataURL string) ([]byte, string, error) {
	dataURL = strings.TrimSpace(dataURL)
	if !strings.HasPrefix(dataURL, "data:") {
		return nil, "", errors.New("clipboard image must be a data URL")
	}

	header, encoded, ok := strings.Cut(strings.TrimPrefix(dataURL, "data:"), ",")
	if !ok {
		return nil, "", errors.New("clipboard image data URL is missing data")
	}
	parts := strings.Split(header, ";")
	mimeType := strings.ToLower(strings.TrimSpace(parts[0]))
	if !strings.HasPrefix(mimeType, "image/") {
		return nil, "", fmt.Errorf("clipboard data is not an image: %s", mimeType)
	}
	ext, ok := clipboardImageExtension(mimeType)
	if !ok {
		return nil, "", fmt.Errorf("unsupported clipboard image type: %s", mimeType)
	}

	base64Encoded := false
	for _, part := range parts[1:] {
		if strings.EqualFold(strings.TrimSpace(part), "base64") {
			base64Encoded = true
			break
		}
	}
	if !base64Encoded {
		return nil, "", errors.New("clipboard image data URL must be base64 encoded")
	}

	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, "", fmt.Errorf("decode clipboard image: %w", err)
	}
	if len(data) == 0 {
		return nil, "", errors.New("clipboard image is empty")
	}
	return data, ext, nil
}

// --- Bound methods (callable from Svelte) ---------------------------------

// Connected reports whether the control API connection is currently up.
func (c *ControlService) Connected() bool { return c.connected.Load() }

// ListConversations returns the /api/conversations payload as a JSON string.
func (c *ControlService) ListConversations() (string, error) {
	data, err := c.request(controlIn{Type: "list_conversations"})
	return string(data), err
}

// GetActiveWorkers returns the running-worker payload as a JSON string.
func (c *ControlService) GetActiveWorkers() (string, error) {
	data, err := c.request(controlIn{Type: "get_active_workers"})
	return string(data), err
}

// GetHistory returns a conversation's stored turns as a JSON string. kind is
// "telegram" or "web"; id is the web conversation id (ignored for telegram).
func (c *ControlService) GetHistory(kind, id string) (string, error) {
	if kind == "" {
		kind = "telegram"
	}
	data, err := c.request(controlIn{Type: "get_history", Target: targetJSON(kind, "", id)})
	return string(data), err
}

// SendText dispatches a message to a conversation (kind "telegram"|"web", id for
// web). A leading "!" is routed as a command.
func (c *ControlService) SendText(text, kind, id string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	m := controlIn{Text: text, Origin: "web", Target: targetJSON(kind, "", id)}
	if strings.HasPrefix(text, "!") {
		m.Type = "handle_command"
	} else {
		m.Type = "send_text"
	}
	return c.send(m)
}

// WebNew creates a new top-level web conversation.
func (c *ControlService) WebNew(title string) error {
	return c.send(controlIn{Type: "web_new", Title: title, Origin: "web"})
}

// WebSetDir sets a web conversation's working directory.
func (c *ControlService) WebSetDir(id, path string) error {
	return c.send(controlIn{Type: "web_setdir", ID: id, Path: path, Origin: "web"})
}

// WebRename renames a web conversation.
func (c *ControlService) WebRename(id, title string) error {
	return c.send(controlIn{Type: "web_rename", ID: id, Title: title, Origin: "web"})
}

// WebDelete deletes a web conversation.
func (c *ControlService) WebDelete(id string) error {
	return c.send(controlIn{Type: "web_delete", ID: id, Origin: "web"})
}

// SetChannelBackend sets a per-channel backend override. backend may be
// "default" (inherit), "claude", or "codex".
func (c *ControlService) SetChannelBackend(kind, id, backend string) (string, error) {
	if kind == "" {
		kind = "telegram"
	}
	data, err := c.request(controlIn{
		Type:    "set_channel_backend",
		Origin:  "web",
		Target:  targetJSON(kind, "", id),
		Backend: backend,
	})
	return string(data), err
}

// PickFolder opens a native OS folder picker and returns the chosen absolute
// path ("" if cancelled) — the key desktop-app win over the browser, which can't
// read real filesystem paths.
func (c *ControlService) PickFolder() (string, error) {
	return application.Get().Dialog.OpenFile().
		CanChooseDirectories(true).
		CanChooseFiles(false).
		CanCreateDirectories(true).
		PromptForSingleSelection()
}

// GetVersion returns the teleclaude version payload as a JSON string.
func (c *ControlService) GetVersion() (string, error) {
	data, err := c.request(controlIn{Type: "get_version"})
	return string(data), err
}

// GetAux returns aglink helper-feature status as a JSON string.
func (c *ControlService) GetAux() (string, error) {
	data, err := c.request(controlIn{Type: "get_aux"})
	return string(data), err
}

// GetConfig returns the raw config payload wrapper as a JSON string.
func (c *ControlService) GetConfig() (string, error) {
	data, err := c.request(controlIn{Type: "get_config"})
	return string(data), err
}

// SetConfig writes raw config text and returns the control reply as a JSON string.
func (c *ControlService) SetConfig(body string) (string, error) {
	data, err := c.request(controlIn{Type: "set_config", Body: body})
	return string(data), err
}

// GetSettings returns the structured settings schema as a JSON string.
func (c *ControlService) GetSettings() (string, error) {
	data, err := c.request(controlIn{Type: "get_settings"})
	return string(data), err
}

// SetSettings updates structured settings and returns the control reply as a JSON string.
func (c *ControlService) SetSettings(body string) (string, error) {
	data, err := c.request(controlIn{Type: "set_settings", Body: body})
	return string(data), err
}

// PickFile opens a native OS file picker and returns the chosen absolute path.
func (c *ControlService) PickFile() (string, error) {
	return application.Get().Dialog.OpenFile().
		CanChooseFiles(true).
		CanChooseDirectories(false).
		PromptForSingleSelection()
}

// SaveClipboardImage stores a pasted clipboard image data URL under teleclaude's
// attachments directory so the control API will accept it for ingestion.
func (c *ControlService) SaveClipboardImage(dataURL string) (string, error) {
	data, ext, err := decodeClipboardImageDataURL(dataURL)
	if err != nil {
		return "", err
	}
	return writeStagedAttachmentBytes(data, ext)
}

// StageAttachment copies a native picked file into teleclaude's attachments
// directory before UploadAttachment asks teleclaude to ingest it.
func (c *ControlService) StageAttachment(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("attachment path is empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read attachment: %w", err)
	}
	return writeStagedAttachmentBytes(data, filepath.Ext(path))
}

// PreviewAttachmentImage returns a browser-renderable data URL for a local image
// chosen through the native file picker.
func (c *ControlService) PreviewAttachmentImage(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("attachment path is empty")
	}
	mimeType, ok := attachmentImageMimeType(path)
	if !ok {
		return "", fmt.Errorf("attachment is not a previewable image: %s", filepath.Ext(path))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read attachment preview: %w", err)
	}
	if len(data) == 0 {
		return "", errors.New("attachment image is empty")
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// UploadAttachment relays a local file path through teleclaude's attachment pipeline.
func (c *ControlService) UploadAttachment(path, caption, kind, id string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	return c.send(uploadAttachmentControlIn(path, caption, kind, id))
}
