package main

import (
	"path/filepath"
	"testing"
)

func TestBuildConversationsResponse_ListsWebConvs(t *testing.T) {
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	_ = st.Load()
	c, _ := st.NewWebConv("웹1")
	_ = c
	resp := buildConversationsResponse(st)
	if len(resp.WebConvs) != 1 || resp.WebConvs[0].Title != "웹1" {
		t.Errorf("response should list web convs, got %+v", resp.WebConvs)
	}
}

func TestValidateDir(t *testing.T) {
	dir := t.TempDir()
	if err := validateDir(dir); err != nil {
		t.Errorf("existing dir should validate: %v", err)
	}
	if err := validateDir(filepath.Join(dir, "nope")); err == nil {
		t.Error("missing dir should fail validation")
	}
}
