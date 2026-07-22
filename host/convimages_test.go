package main

import (
	"encoding/base64"
	"testing"
)

func TestConvImages_SaveLoadAndSafety(t *testing.T) {
	t.Setenv("AGLINK_HOME", t.TempDir())
	png := []byte("\x89PNG\r\n\x1a\nFAKE")
	refs := saveConvImages("conv-1", [][]byte{png, png})
	if len(refs) != 2 {
		t.Fatalf("saved refs = %d, want 2", len(refs))
	}
	got, ok := loadImageRef(refs[0])
	if !ok || string(got) != string(png) {
		t.Errorf("loadImageRef roundtrip failed: ok=%v got=%q", ok, got)
	}
	if _, ok := loadImageRef("conv-1/../../escape.png"); ok {
		t.Error("path-traversal ref must be rejected")
	}
	if _, ok := loadImageRef("conv-1/missing.png"); ok {
		t.Error("missing ref must return ok=false")
	}
	if saveConvImages("c", nil) != nil {
		t.Error("no images → nil refs")
	}
}

func TestConvImages_PruneKeepsNewest(t *testing.T) {
	t.Setenv("AGLINK_HOME", t.TempDir())
	imgs := make([][]byte, maxImagesPerConv+5)
	for i := range imgs {
		imgs[i] = []byte{byte(i)}
	}
	refs := saveConvImages("c", imgs)
	live := 0
	for _, r := range refs {
		if _, ok := loadImageRef(r); ok {
			live++
		}
	}
	if live == 0 || live > maxImagesPerConv {
		t.Errorf("live images = %d, want in (0, %d]", live, maxImagesPerConv)
	}
}

// A turn's persisted image refs are replayed into /api/history as base64 image
// entries, so images survive a desktop restart.
func TestBuildHistoryResponse_IncludesPersistedImages(t *testing.T) {
	t.Setenv("AGLINK_HOME", t.TempDir())
	refs := saveConvImages("tg", [][]byte{[]byte("IMG")})
	if len(refs) != 1 {
		t.Fatalf("save refs = %d, want 1", len(refs))
	}
	st := histStore(t)
	tc := st.TelegramConversation()
	tc.History = []ConversationTurn{{Prompt: "q", Response: "a", Images: refs}}
	_ = st.UpdateTelegramConversation(tc)

	resp := buildHistoryResponse(st, Target{Kind: "telegram"})
	if len(resp.Turns) != 3 { // user(q) + assistant(a) + image
		t.Fatalf("turns = %d, want 3 (%+v)", len(resp.Turns), resp.Turns)
	}
	img := resp.Turns[2]
	if img.Image == "" {
		t.Fatal("expected an image turn with base64 payload")
	}
	dec, err := base64.StdEncoding.DecodeString(img.Image)
	if err != nil || string(dec) != "IMG" {
		t.Errorf("image payload = %q err=%v, want IMG", dec, err)
	}
}
