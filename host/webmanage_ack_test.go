package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// web_setdir / web_delete are acknowledged like web_rename: handleInbound runs
// them synchronously and pushes exactly one {"ok":bool,"error":string} reply
// for the request's reqID, so the browser can change its screen only after the
// store write landed (and keep it, showing the error, when it did not).

// failingDeleteStore is a real store whose DeleteWebConv save fails.
type failingDeleteStore struct{ *fileStore }

func (failingDeleteStore) DeleteWebConv(string) error { return errors.New("disk full") }

type ackReply struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

func webAckServer(st StoreRepo) (*chatControlServer, *remoteChatChannel) {
	b := &Bot{store: st, out: NewHub(), cancels: map[int]*cancelEntry{}}
	s := &chatControlServer{ownerChatID: 7, bot: b, hub: b.out}
	ch := &remoteChatChannel{send: make(chan controlOut, 4), cancel: func() {}}
	return s, ch
}

func singleAck(t *testing.T, ch *remoteChatChannel, reqID string) ackReply {
	t.Helper()
	outs := drainControlOut(ch.send)
	if len(outs) != 1 || outs[0].Kind != "reply" || outs[0].ReqID != reqID {
		t.Fatalf("expected exactly one reply for %s, got %+v", reqID, outs)
	}
	var r ackReply
	if err := json.Unmarshal(outs[0].Data, &r); err != nil {
		t.Fatalf("reply data %s: %v", outs[0].Data, err)
	}
	return r
}

func TestChatControl_WebSetDir_OK(t *testing.T) {
	st := newTestStore(t)
	c, _ := st.NewWebConv("w")
	s, ch := webAckServer(st)
	dir := t.TempDir()

	s.handleInbound(ch, controlIn{Type: "web_setdir", ReqID: "r1", ID: c.ID, Path: dir})

	if r := singleAck(t, ch, "r1"); !r.OK || r.Error != "" {
		t.Fatalf("valid folder: want ok reply, got %+v", r)
	}
	if got, _ := st.GetWebConv(c.ID); got.WorkDir != dir {
		t.Errorf("workDir = %q, want %q — not persisted by the time the reply was sent", got.WorkDir, dir)
	}
}

func TestChatControl_WebSetDir_BadPathReplyError(t *testing.T) {
	st := newTestStore(t)
	c, _ := st.NewWebConv("w")
	keep := t.TempDir()
	c.WorkDir = keep
	if err := st.UpdateWebConv(c); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "plain.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"missing": filepath.Join(t.TempDir(), "does-not-exist"),
		"not dir": file,
		"empty":   "",
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			s, ch := webAckServer(st)
			s.handleInbound(ch, controlIn{Type: "web_setdir", ReqID: "r2", ID: c.ID, Path: path})
			r := singleAck(t, ch, "r2")
			if r.OK || r.Error == "" {
				t.Fatalf("bad folder %q: want error reply, got %+v", path, r)
			}
			if got, _ := st.GetWebConv(c.ID); got.WorkDir != keep {
				t.Errorf("workDir changed to %q on a rejected path", got.WorkDir)
			}
		})
	}
}

func TestChatControl_WebSetDir_UnknownConvReplyError(t *testing.T) {
	s, ch := webAckServer(newTestStore(t))
	s.handleInbound(ch, controlIn{Type: "web_setdir", ReqID: "r3", ID: "nope", Path: t.TempDir()})
	if r := singleAck(t, ch, "r3"); r.OK || r.Error == "" {
		t.Fatalf("unknown conversation: want error reply, got %+v", r)
	}
}

func TestChatControl_WebDelete_OK(t *testing.T) {
	st := newTestStore(t)
	c, _ := st.NewWebConv("w")
	s, ch := webAckServer(st)

	s.handleInbound(ch, controlIn{Type: "web_delete", ReqID: "r4", ID: c.ID})

	if r := singleAck(t, ch, "r4"); !r.OK {
		t.Fatalf("delete: want ok reply, got %+v", r)
	}
	if _, ok := st.GetWebConv(c.ID); ok {
		t.Error("conversation still present when the ok reply was sent")
	}
}

func TestChatControl_WebDelete_SaveFailureReplyError(t *testing.T) {
	st := newTestStore(t)
	c, _ := st.NewWebConv("w")
	s, ch := webAckServer(failingDeleteStore{st})

	s.handleInbound(ch, controlIn{Type: "web_delete", ReqID: "r5", ID: c.ID})

	r := singleAck(t, ch, "r5")
	if r.OK || !strings.Contains(r.Error, "disk full") {
		t.Fatalf("failed delete: want error reply carrying the cause, got %+v", r)
	}
}

func TestChatControl_WebDelete_UnknownConvReplyError(t *testing.T) {
	s, ch := webAckServer(newTestStore(t))
	s.handleInbound(ch, controlIn{Type: "web_delete", ReqID: "r6", ID: "nope"})
	if r := singleAck(t, ch, "r6"); r.OK || r.Error == "" {
		t.Fatalf("unknown conversation: want error reply, got %+v", r)
	}
}
