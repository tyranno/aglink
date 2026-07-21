package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// VLLMServer is one OpenAI-compatible local inference endpoint (a vLLM instance).
// BaseURL is the OpenAI-compatible root, e.g. http://10.0.0.5:8000/v1. Model is
// the served model id (opencode references it as vllm/<model>). APIKey is often a
// throwaway/local token; vLLM can be started with or without auth.
type VLLMServer struct {
	Name    string `yaml:"name" json:"name"`
	BaseURL string `yaml:"base_url" json:"base_url"`
	Model   string `yaml:"model" json:"model"`
	APIKey  string `yaml:"api_key" json:"api_key"`
}

// vllmServerAt returns the i-th configured server, or a zero VLLMServer when the
// slot doesn't exist. Lets the scalar settings UI read "primary"/"secondary"
// slots without index-out-of-range checks scattered around.
func vllmServerAt(cfg *Config, i int) VLLMServer {
	if cfg == nil || i < 0 || i >= len(cfg.VLLMServers) {
		return VLLMServer{}
	}
	return cfg.VLLMServers[i]
}

// setVLLMField sets one field of the i-th server, growing the slice with empty
// slots as needed so the UI can populate a "secondary" slot before a "primary"
// one exists. Callers normalize afterward (normalizeVLLMServers) to drop the
// trailing empties this can create.
func setVLLMField(cfg *Config, i int, field, val string) {
	for len(cfg.VLLMServers) <= i {
		cfg.VLLMServers = append(cfg.VLLMServers, VLLMServer{})
	}
	switch field {
	case "url":
		cfg.VLLMServers[i].BaseURL = strings.TrimSpace(val)
	case "model":
		cfg.VLLMServers[i].Model = strings.TrimSpace(val)
	case "apikey":
		cfg.VLLMServers[i].APIKey = val
	case "name":
		cfg.VLLMServers[i].Name = strings.TrimSpace(val)
	}
}

// vllmServerEmpty reports whether a server slot carries no usable configuration
// (an all-blank slot the scalar UI created and the user never filled in).
func vllmServerEmpty(s VLLMServer) bool {
	return strings.TrimSpace(s.BaseURL) == "" && strings.TrimSpace(s.Model) == "" &&
		strings.TrimSpace(s.Name) == "" && s.APIKey == ""
}

// normalizeVLLMServers trims trailing all-blank slots so a cleared "secondary"
// slot doesn't linger and get rendered into the opencode provider config. Only
// trailing blanks are dropped — a blank slot between two filled ones is kept so
// slot indices the UI edits stay stable within one save.
func normalizeVLLMServers(cfg *Config) {
	for len(cfg.VLLMServers) > 0 && vllmServerEmpty(cfg.VLLMServers[len(cfg.VLLMServers)-1]) {
		cfg.VLLMServers = cfg.VLLMServers[:len(cfg.VLLMServers)-1]
	}
}

// renderVLLMOpencodeConfig produces an opencode.json exposing each vLLM server as
// an OpenAI-compatible provider. The first server is provider id "vllm", the rest
// "vllm-2", "vllm-3", … so a worker/manager model reference like "vllm/<model>"
// targets the primary and later servers stay addressable for failover/spillover.
// Output is deterministic (Go marshals map keys sorted) so callers can compare
// bytes and skip rewriting an unchanged file.
func renderVLLMOpencodeConfig(servers []VLLMServer) ([]byte, error) {
	providers := vllmProviders(servers)
	if len(providers) == 0 {
		return nil, fmt.Errorf("vLLM 서버(base_url)가 없습니다")
	}
	root := map[string]any{
		"$schema":  "https://opencode.ai/config.json",
		"provider": providers,
	}
	return json.MarshalIndent(root, "", "  ")
}

// vllmProviders builds the opencode provider entries for the configured vLLM
// servers: the first usable server is id "vllm", the rest "vllm-2", "vllm-3", …
// Servers with a blank base_url are skipped. Returns an empty map when none are
// usable so callers can merge it without a special-case.
func vllmProviders(servers []VLLMServer) map[string]any {
	providers := map[string]any{}
	i := 0
	for _, s := range servers {
		if strings.TrimSpace(s.BaseURL) == "" {
			continue
		}
		id := "vllm"
		if i > 0 {
			id = fmt.Sprintf("vllm-%d", i+1)
		}
		i++
		model := strings.TrimSpace(s.Model)
		if model == "" {
			model = "default"
		}
		opts := map[string]any{"baseURL": strings.TrimSpace(s.BaseURL)}
		// vLLM accepts any bearer when started without auth; opencode's
		// openai-compatible provider still wants a non-empty apiKey to send one.
		if key := strings.TrimSpace(s.APIKey); key != "" {
			opts["apiKey"] = key
		} else {
			opts["apiKey"] = "vllm-local"
		}
		name := strings.TrimSpace(s.Name)
		if name == "" {
			name = id
		}
		providers[id] = map[string]any{
			"npm":     "@ai-sdk/openai-compatible",
			"name":    name,
			"options": opts,
			"models":  map[string]any{model: map[string]any{}},
		}
	}
	return providers
}

// renderOpencodeProviderConfig produces the generated opencode.json exposing all
// mounted OpenAI-compatible providers: local vLLM servers (ids vllm, vllm-2, …)
// plus every free-catalog provider that has an API key (ids groq, cerebras, …).
// A worker/manager model reference like "groq/llama-3.3-70b-versatile" or
// "vllm/<model>" then targets one of them. Returns an error when nothing is
// mounted so resolveOpencodeConfigPath can fall back to opencode's own search.
func renderOpencodeProviderConfig(cfg *Config) ([]byte, error) {
	providers := vllmProviders(cfg.VLLMServers)
	for id, p := range freeProviderProviders(cfg) {
		providers[id] = p
	}
	if len(providers) == 0 {
		return nil, fmt.Errorf("mount된 provider가 없습니다 (vLLM 서버 또는 무료 API 키)")
	}
	root := map[string]any{
		"$schema":  "https://opencode.ai/config.json",
		"provider": providers,
	}
	return json.MarshalIndent(root, "", "  ")
}

// resolveOpencodeConfigPath returns the opencode.json path the opencode runner
// should use: the explicit OpencodeConfigPath when set; else, when vLLM servers
// are configured, a generated config under the data dir that exposes them as an
// OpenAI-compatible provider (zero manual opencode.json editing); else "" so
// opencode falls back to its own default search — i.e. a machine with neither
// setting behaves exactly as before. The generated file is only rewritten when
// its bytes change, so per-turn calls don't thrash the disk.
func resolveOpencodeConfigPath(cfg *Config) string {
	if p := strings.TrimSpace(cfg.OpencodeConfigPath); p != "" {
		return p
	}
	if len(cfg.VLLMServers) == 0 && !anyProviderCred(cfg) {
		return ""
	}
	data, err := renderOpencodeProviderConfig(cfg)
	if err != nil {
		return ""
	}
	dir, err := dataDir()
	if err != nil {
		return ""
	}
	path := filepath.Join(dir, "opencode-vllm.json")
	if existing, rerr := os.ReadFile(path); rerr == nil && bytes.Equal(existing, data) {
		return path
	}
	if werr := os.WriteFile(path, data, 0o600); werr != nil {
		return ""
	}
	return path
}
