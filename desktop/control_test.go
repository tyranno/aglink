package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestControlServiceConnectReadsLargeHistoryReplies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")

		data, err := json.Marshal(map[string]string{
			"history": strings.Repeat("x", 64*1024),
		})
		if err != nil {
			t.Errorf("marshal large reply: %v", err)
			return
		}

		writeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := wsjson.Write(writeCtx, conn, controlOut{
			Kind:  "reply",
			ReqID: "r-large",
			Data:  data,
		}); err != nil {
			t.Errorf("write large reply: %v", err)
			return
		}

		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer server.Close()

	service := &ControlService{
		addr:    strings.TrimPrefix(server.URL, "http://"),
		pending: map[string]chan json.RawMessage{},
	}
	replyCh := make(chan json.RawMessage, 1)
	service.pending["r-large"] = replyCh

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- service.connectOnce(ctx)
	}()

	select {
	case data := <-replyCh:
		if len(data) < 64*1024 {
			t.Fatalf("delivered reply was truncated: %d bytes", len(data))
		}
	case err := <-errCh:
		t.Fatalf("connectOnce returned before delivering large reply: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for large reply delivery")
	}
}

func setTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func TestSaveClipboardImageWritesAttachmentsFile(t *testing.T) {
	home := setTestHome(t)
	payload := []byte("clipboard image bytes")
	encoded := base64.StdEncoding.EncodeToString(payload)

	path, err := (&ControlService{}).SaveClipboardImage("data:image/png;base64," + encoded)
	if err != nil {
		t.Fatalf("SaveClipboardImage returned error: %v", err)
	}
	defer os.Remove(path)

	if filepath.Ext(path) != ".png" {
		t.Fatalf("extension = %q, want .png", filepath.Ext(path))
	}
	wantDir := filepath.Join(home, ".teleclaude", "attachments")
	if filepath.Dir(path) != wantDir {
		t.Fatalf("dir = %q, want %q", filepath.Dir(path), wantDir)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("saved bytes = %q, want %q", got, payload)
	}
}

func TestStageAttachmentCopiesIntoAttachmentsDir(t *testing.T) {
	home := setTestHome(t)
	src := filepath.Join(t.TempDir(), "picked.jpg")
	payload := []byte("picked file bytes")
	if err := os.WriteFile(src, payload, 0o600); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	path, err := (&ControlService{}).StageAttachment(src)
	if err != nil {
		t.Fatalf("StageAttachment returned error: %v", err)
	}
	defer os.Remove(path)

	wantDir := filepath.Join(home, ".teleclaude", "attachments")
	if filepath.Dir(path) != wantDir {
		t.Fatalf("dir = %q, want %q", filepath.Dir(path), wantDir)
	}
	if filepath.Ext(path) != ".jpg" {
		t.Fatalf("extension = %q, want .jpg", filepath.Ext(path))
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read staged file: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("staged bytes = %q, want %q", got, payload)
	}
}

func TestSaveClipboardImageRejectsNonImageDataURL(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("plain text"))

	if path, err := (&ControlService{}).SaveClipboardImage("data:text/plain;base64," + encoded); err == nil {
		_ = os.Remove(path)
		t.Fatal("SaveClipboardImage accepted a non-image data URL")
	}
}

func TestPreviewAttachmentImageReturnsDataURLForLocalImage(t *testing.T) {
	payload := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 1, 2, 3}
	path := filepath.Join(t.TempDir(), "picked.png")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatalf("write temp png: %v", err)
	}

	preview, err := (&ControlService{}).PreviewAttachmentImage(path)
	if err != nil {
		t.Fatalf("PreviewAttachmentImage returned error: %v", err)
	}
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(payload)
	if preview != want {
		t.Fatalf("preview = %q, want %q", preview, want)
	}
}

func TestPreviewAttachmentImageRejectsNonImages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(path, []byte("not an image"), 0o600); err != nil {
		t.Fatalf("write temp text: %v", err)
	}

	if preview, err := (&ControlService{}).PreviewAttachmentImage(path); err == nil {
		t.Fatalf("PreviewAttachmentImage accepted non-image file and returned %q", preview)
	}
}

func TestUploadAttachmentControlInIncludesTarget(t *testing.T) {
	msg := uploadAttachmentControlIn(`C:\tmp\a.png`, "설명", "web", "conv-7")
	if msg.Type != "upload_attachment" {
		t.Fatalf("type = %q, want upload_attachment", msg.Type)
	}
	if msg.Path != `C:\tmp\a.png` || msg.Caption != "설명" {
		t.Fatalf("path/caption = %q/%q", msg.Path, msg.Caption)
	}
	var target map[string]string
	if err := json.Unmarshal(msg.Target, &target); err != nil {
		t.Fatalf("target json: %v", err)
	}
	if target["kind"] != "web" || target["id"] != "conv-7" {
		t.Fatalf("target = %#v, want web conv-7", target)
	}
}
