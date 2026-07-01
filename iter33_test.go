package main

import (
	"encoding/base64"
	"testing"
)

// ---- extractToolResultImages ----

func TestExtractToolResultImages_DecodesImageAndCaption(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte("PNGBYTES"))
	line := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"x",` +
		`"content":[{"type":"text","text":"Window Foo origin (10,20)"},` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + data + `"}}]}]}}`
	imgs := extractToolResultImages(line)
	if len(imgs) != 1 {
		t.Fatalf("want 1 image, got %d", len(imgs))
	}
	if string(imgs[0].png) != "PNGBYTES" {
		t.Errorf("png = %q, want PNGBYTES", imgs[0].png)
	}
	if imgs[0].caption == "" {
		t.Error("caption should be recovered from the sibling text block")
	}
}

func TestExtractToolResultImages_NonToolResultLine(t *testing.T) {
	line := `{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`
	if imgs := extractToolResultImages(line); imgs != nil {
		t.Errorf("assistant line should yield no images, got %d", len(imgs))
	}
}

func TestExtractToolResultImages_StringContentNoImage(t *testing.T) {
	// tool_result whose content is a plain string (a text-only tool result).
	line := `{"type":"user","message":{"content":[{"type":"tool_result","content":"ok: done"}]}}`
	if imgs := extractToolResultImages(line); imgs != nil {
		t.Errorf("string tool_result should yield no images, got %d", len(imgs))
	}
}

func TestExtractToolResultImages_ResultEnvelopeIgnored(t *testing.T) {
	line := `{"type":"result","subtype":"success","is_error":false,"result":"all done"}`
	if imgs := extractToolResultImages(line); imgs != nil {
		t.Errorf("result envelope should yield no images, got %d", len(imgs))
	}
}
