package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderVLLMOpencodeConfig_Primary(t *testing.T) {
	b, err := renderVLLMOpencodeConfig([]VLLMServer{
		{Name: "gpu1", BaseURL: "http://10.0.0.5:8000/v1", Model: "qwen2.5-coder"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var root struct {
		Provider map[string]struct {
			NPM     string                 `json:"npm"`
			Options map[string]any         `json:"options"`
			Models  map[string]interface{} `json:"models"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, b)
	}
	p, ok := root.Provider["vllm"]
	if !ok {
		t.Fatalf("primary server must be provider id 'vllm': %s", b)
	}
	if p.Options["baseURL"] != "http://10.0.0.5:8000/v1" {
		t.Errorf("baseURL wrong: %v", p.Options["baseURL"])
	}
	if _, ok := p.Models["qwen2.5-coder"]; !ok {
		t.Errorf("model not present: %v", p.Models)
	}
	if p.Options["apiKey"] == "" || p.Options["apiKey"] == nil {
		t.Errorf("apiKey should default to a non-empty placeholder for vLLM: %v", p.Options["apiKey"])
	}
}

func TestRenderVLLMOpencodeConfig_SecondBecomesVllm2(t *testing.T) {
	b, err := renderVLLMOpencodeConfig([]VLLMServer{
		{BaseURL: "http://a:8000/v1", Model: "m1"},
		{BaseURL: "http://b:8000/v1", Model: "m2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"vllm"`) || !strings.Contains(s, `"vllm-2"`) {
		t.Errorf("expected vllm and vllm-2 providers: %s", s)
	}
}

func TestRenderVLLMOpencodeConfig_SkipsEmptyBaseURL(t *testing.T) {
	if _, err := renderVLLMOpencodeConfig([]VLLMServer{{Model: "m"}}); err == nil {
		t.Fatal("a server with no base_url is not usable")
	}
}

func TestNormalizeVLLMServers_TrimsTrailingEmpties(t *testing.T) {
	cfg := &Config{VLLMServers: []VLLMServer{
		{BaseURL: "http://a"},
		{}, // blank trailing slot the scalar UI created
	}}
	normalizeVLLMServers(cfg)
	if len(cfg.VLLMServers) != 1 {
		t.Fatalf("trailing blank slot not trimmed: %+v", cfg.VLLMServers)
	}
}

func TestSetVLLMFieldGrows(t *testing.T) {
	cfg := &Config{}
	setVLLMField(cfg, 1, "url", "http://b") // secondary before primary exists
	if len(cfg.VLLMServers) != 2 {
		t.Fatalf("slice should grow to index+1: %+v", cfg.VLLMServers)
	}
	if cfg.VLLMServers[1].BaseURL != "http://b" {
		t.Errorf("secondary url not set: %+v", cfg.VLLMServers)
	}
}

func TestResolveOpencodeConfigPath_ExplicitWins(t *testing.T) {
	cfg := &Config{OpencodeConfigPath: `C:\custom\opencode.json`, VLLMServers: []VLLMServer{{BaseURL: "http://a"}}}
	if got := resolveOpencodeConfigPath(cfg); got != `C:\custom\opencode.json` {
		t.Errorf("explicit path must win, got %q", got)
	}
}

func TestResolveOpencodeConfigPath_NoneReturnsEmpty(t *testing.T) {
	if got := resolveOpencodeConfigPath(&Config{}); got != "" {
		t.Errorf("no config and no vllm should return empty, got %q", got)
	}
}
