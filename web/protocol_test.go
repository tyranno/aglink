package main

import (
	"encoding/json"
	"testing"
)

func TestRequestRoundTrip(t *testing.T) {
	in := Request{ID: 7, Method: "navigate", Params: map[string]any{"url": "https://example.com"}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Request
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ID != in.ID || out.Method != in.Method {
		t.Fatalf("id/method mismatch: got %+v", out)
	}
	if out.Params["url"] != "https://example.com" {
		t.Fatalf("params mismatch: got %+v", out.Params)
	}
}

func TestReplyOmitsEmptyFields(t *testing.T) {
	b, err := json.Marshal(Reply{ID: 1, OK: true, Text: "hi"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	if want := `{"id":1,"ok":true,"text":"hi"}`; s != want {
		t.Fatalf("unexpected reply json: got %s want %s", s, want)
	}
}
