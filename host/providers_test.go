package main

import (
	"encoding/json"
	"testing"
)

// A catalog provider mounts only once it has a key, and lands under its catalog
// id (not vllm) so a worker model "groq/<model>" can target it.
func TestFreeProvider_MountsWithKeyUnderCatalogID(t *testing.T) {
	cfg := &Config{}
	setProviderCredField(cfg, "groq", "key", "gsk_test")
	b, err := renderOpencodeProviderConfig(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var root struct {
		Provider map[string]struct {
			Options map[string]any            `json:"options"`
			Models  map[string]json.RawMessage `json:"models"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, b)
	}
	p, ok := root.Provider["groq"]
	if !ok {
		t.Fatalf("groq provider missing: %s", b)
	}
	if p.Options["baseURL"] != "https://api.groq.com/openai/v1" {
		t.Errorf("baseURL wrong: %v", p.Options["baseURL"])
	}
	if p.Options["apiKey"] != "gsk_test" {
		t.Errorf("apiKey not passed through: %v", p.Options["apiKey"])
	}
	if _, ok := p.Models["llama-3.3-70b-versatile"]; !ok {
		t.Errorf("default model not applied: %v", p.Models)
	}
}

// Gemini must render as the native @ai-sdk/google provider (not the generic
// openai-compatible shim) and without a baseURL: its 2.5 thinking models require
// a thought_signature round-trip on tool calls that only the native provider
// preserves. A plain openai-compatible endpoint gets a baseURL as before.
func TestGemini_UsesNativeGoogleProvider(t *testing.T) {
	cfg := &Config{}
	setProviderCredField(cfg, "gemini", "key", "AQ.test")
	setProviderCredField(cfg, "groq", "key", "gsk_test")
	b, err := renderOpencodeProviderConfig(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var root struct {
		Provider map[string]struct {
			NPM     string         `json:"npm"`
			Options map[string]any `json:"options"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, b)
	}
	gem := root.Provider["gemini"]
	if gem.NPM != "@ai-sdk/google" {
		t.Errorf("gemini npm = %q, want @ai-sdk/google", gem.NPM)
	}
	if _, ok := gem.Options["baseURL"]; ok {
		t.Errorf("native gemini must not carry a baseURL: %v", gem.Options)
	}
	if gem.Options["apiKey"] != "AQ.test" {
		t.Errorf("gemini apiKey not passed: %v", gem.Options)
	}
	// A plain openai-compatible provider is unaffected: still shim + baseURL.
	groq := root.Provider["groq"]
	if groq.NPM != "@ai-sdk/openai-compatible" {
		t.Errorf("groq npm = %q, want @ai-sdk/openai-compatible", groq.NPM)
	}
	if groq.Options["baseURL"] != "https://api.groq.com/openai/v1" {
		t.Errorf("groq baseURL wrong: %v", groq.Options)
	}
}

// No key → nothing mounted → generated config not needed (fall back to opencode).
func TestFreeProvider_KeylessIsInert(t *testing.T) {
	cfg := &Config{Providers: map[string]ProviderCred{"groq": {Model: "x"}}}
	if anyProviderCred(cfg) {
		t.Fatal("a keyless cred must not count as mounted")
	}
	if got := resolveOpencodeConfigPath(cfg); got != "" {
		t.Errorf("keyless provider should not trigger a generated config, got %q", got)
	}
}

// Clearing the key removes the entry entirely; unknown ids are rejected.
func TestSetProviderCredField_ClearAndUnknown(t *testing.T) {
	cfg := &Config{}
	setProviderCredField(cfg, "cerebras", "key", "k")
	setProviderCredField(cfg, "cerebras", "model", "llama-3.3-70b")
	if cfg.Providers["cerebras"].Model != "llama-3.3-70b" {
		t.Fatalf("model override lost: %+v", cfg.Providers)
	}
	setProviderCredField(cfg, "cerebras", "key", "")
	if _, ok := cfg.Providers["cerebras"]; ok {
		t.Errorf("clearing the key should drop the entry: %+v", cfg.Providers)
	}
	applyProviderSetting(cfg, "provider.bogus.key", "nope")
	if _, ok := cfg.Providers["bogus"]; ok {
		t.Errorf("unknown catalog id must be rejected: %+v", cfg.Providers)
	}
}

// vLLM and free providers coexist in one generated config.
func TestRenderOpencode_VLLMAndFreeCoexist(t *testing.T) {
	cfg := &Config{
		VLLMServers: []VLLMServer{{BaseURL: "http://a:8000/v1", Model: "m"}},
		Providers:   map[string]ProviderCred{"gemini": {APIKey: "AIza"}},
	}
	b, err := renderOpencodeProviderConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Provider map[string]json.RawMessage `json:"provider"`
	}
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatal(err)
	}
	if _, ok := root.Provider["vllm"]; !ok {
		t.Errorf("vllm provider missing: %s", b)
	}
	if _, ok := root.Provider["gemini"]; !ok {
		t.Errorf("gemini provider missing: %s", b)
	}
}
