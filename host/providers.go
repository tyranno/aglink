package main

import (
	"sort"
	"strings"
)

// FreeProvider is one entry in the built-in catalog of free, OpenAI-compatible
// remote endpoints a user can mount into the opencode backend by pasting an API
// key. BaseURL and DefaultModel are pinned in code so the user only supplies the
// key; the provider is referenced from a worker/manager model as "<ID>/<model>"
// (e.g. groq/llama-3.3-70b-versatile), exactly like a vLLM server. Everything
// here is an OpenAI-compatible chat endpoint, so opencode reaches it through the
// same @ai-sdk/openai-compatible provider used for vLLM.
// yaml/json tags mirror the drop-in plugin file shape (see providers_plugin.go):
// a user adds a brand-new backend by dropping a small YAML file into
// <dataDir>/providers.d, no rebuild required.
type FreeProvider struct {
	ID           string `yaml:"id" json:"id"`                       // stable opencode provider id + config key, e.g. "groq"
	Name         string `yaml:"name" json:"name"`                   // human label shown in the settings UI
	BaseURL      string `yaml:"base_url" json:"base_url"`           // OpenAI-compatible root, ends without a trailing model path
	DefaultModel string `yaml:"default_model" json:"default_model"` // recommended free model id (user-overridable)
	FreeNote     string `yaml:"free_note" json:"free_note"`         // one-line free-tier summary for the settings hint
	SignupURL    string `yaml:"signup_url" json:"signup_url"`       // where to get a free key
}

// freeProviderCatalog is the ordered list surfaced in the settings UI. Order is
// the recommended try-order (best free throughput first). BaseURLs are the
// vendors' OpenAI-compatible endpoints; DefaultModels are current free-tier
// picks and are user-overridable because vendor model ids drift over time.
var freeProviderCatalog = []FreeProvider{
	{
		ID:           "groq",
		Name:         "Groq",
		BaseURL:      "https://api.groq.com/openai/v1",
		DefaultModel: "llama-3.3-70b-versatile",
		FreeNote:     "무료 티어 ~1000 요청/일. 매우 빠름(LPU).",
		SignupURL:    "https://console.groq.com/keys",
	},
	{
		ID:           "cerebras",
		Name:         "Cerebras",
		BaseURL:      "https://api.cerebras.ai/v1",
		DefaultModel: "llama-3.3-70b",
		FreeNote:     "무료 티어 ~1M 토큰/일. 초고속 추론.",
		SignupURL:    "https://cloud.cerebras.ai",
	},
	{
		ID:           "gemini",
		Name:         "Google AI Studio (Gemini)",
		BaseURL:      "https://generativelanguage.googleapis.com/v1beta/openai/",
		DefaultModel: "gemini-2.5-flash",
		FreeNote:     "무료 티어 Flash ~1500 요청/일. 카드 없이 발급.",
		SignupURL:    "https://aistudio.google.com/apikey",
	},
	{
		ID:           "openrouter",
		Name:         "OpenRouter (무료 모델)",
		BaseURL:      "https://openrouter.ai/api/v1",
		DefaultModel: "deepseek/deepseek-chat-v3-0324:free",
		FreeNote:     "':free' 접미 모델은 무료(일일 한도). 여러 오픈모델 중계.",
		SignupURL:    "https://openrouter.ai/keys",
	},
}

// freeProviderByID returns the effective catalog entry for id (ok=false when
// unknown). The effective catalog is the built-in list plus any drop-in plugin
// providers plus the user's UI-defined custom providers (cfg.CustomProviders),
// so an id defined by a providers.d/*.yaml file or added from the settings UI
// resolves here with no rebuild. cfg may be nil (built-ins + plugins only).
func freeProviderByID(cfg *Config, id string) (FreeProvider, bool) {
	for _, p := range catalogProviders(cfg) {
		if p.ID == id {
			return p, true
		}
	}
	return FreeProvider{}, false
}

// ProviderCred is the per-user secret + optional model override for one catalog
// provider. A provider is "mounted" (rendered into opencode.json) only when its
// APIKey is non-empty, so an unconfigured catalog entry stays fully inert — a
// machine that never pastes a key behaves exactly as before.
type ProviderCred struct {
	APIKey string `yaml:"api_key" json:"api_key"`
	Model  string `yaml:"model,omitempty" json:"model,omitempty"`
}

// providerCred reads the stored cred for a catalog id (zero value when unset),
// so the scalar settings UI can render a key/model row without nil checks.
func providerCred(cfg *Config, id string) ProviderCred {
	if cfg == nil || cfg.Providers == nil {
		return ProviderCred{}
	}
	return cfg.Providers[id]
}

// setProviderCredField sets one field of a catalog provider's cred, allocating
// the map on first use. Clearing the api_key ("key" field set blank) removes the
// whole entry so the provider stops mounting. Setting only the model is kept
// even with no key yet — a dangling override is inert (freeProviderProviders
// skips key-less creds) and this avoids dropping the model when key+model are
// saved together under Go's random map-update order.
func setProviderCredField(cfg *Config, id, field, val string) {
	val = strings.TrimSpace(val)
	if field == "key" && val == "" {
		delete(cfg.Providers, id)
		return
	}
	if cfg.Providers == nil {
		cfg.Providers = map[string]ProviderCred{}
	}
	cur := cfg.Providers[id]
	switch field {
	case "key":
		cur.APIKey = val
	case "model":
		cur.Model = val
	}
	cfg.Providers[id] = cur
}

// freeProviderProviders builds the opencode provider entries for every catalog
// provider that has an API key, keyed by the catalog id (groq/cerebras/…). Only
// catalog ids are emitted, so a stray/unknown map entry can't inject an
// arbitrary provider. Returned map is empty when nothing is mounted.
func freeProviderProviders(cfg *Config) map[string]any {
	out := map[string]any{}
	if cfg == nil {
		return out
	}
	// Deterministic order (map marshaling sorts keys anyway; sort the ids we
	// visit so behavior is stable regardless of Go map iteration order).
	ids := make([]string, 0, len(cfg.Providers))
	for id := range cfg.Providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		cred := cfg.Providers[id]
		if strings.TrimSpace(cred.APIKey) == "" {
			continue
		}
		cat, ok := freeProviderByID(cfg, id)
		if !ok {
			continue // unknown id — never emit
		}
		model := strings.TrimSpace(cred.Model)
		if model == "" {
			model = cat.DefaultModel
		}
		out[id] = map[string]any{
			"npm":  "@ai-sdk/openai-compatible",
			"name": cat.Name,
			"options": map[string]any{
				"baseURL": cat.BaseURL,
				"apiKey":  strings.TrimSpace(cred.APIKey),
			},
			"models": map[string]any{model: map[string]any{}},
		}
	}
	return out
}

// anyProviderCred reports whether at least one catalog provider is mounted (has
// a key). Used to decide whether a generated opencode.json is needed.
func anyProviderCred(cfg *Config) bool {
	return len(freeProviderProviders(cfg)) > 0
}

// --- UI-defined custom providers (scalar-slot editing) --------------------
//
// The structured settings form is scalar-only, so custom providers are edited
// as fixed "slots" (custom_provider.1.*, custom_provider.2.*, …) exactly like
// vLLM's primary/secondary servers. A slot defines a new OpenAI-compatible
// backend (id/base_url/default_model/name); the API key for it is then entered
// in the auto-generated "무료 원격 프로바이더" key row that appears once the
// definition is saved (catalogProviders merges the slot in).

// customProviderAt returns the i-th custom provider, or a zero FreeProvider when
// the slot doesn't exist, so the scalar settings UI can read a slot without
// index checks.
func customProviderAt(cfg *Config, i int) FreeProvider {
	if cfg == nil || i < 0 || i >= len(cfg.CustomProviders) {
		return FreeProvider{}
	}
	return cfg.CustomProviders[i]
}

// setCustomProviderField sets one field of the i-th custom provider, growing the
// slice with empty slots as needed (the UI may fill slot 2 before slot 1).
// Callers normalize afterward (normalizeCustomProviders) to drop the trailing
// empties this can create.
func setCustomProviderField(cfg *Config, i int, field, val string) {
	for len(cfg.CustomProviders) <= i {
		cfg.CustomProviders = append(cfg.CustomProviders, FreeProvider{})
	}
	val = strings.TrimSpace(val)
	switch field {
	case "id":
		cfg.CustomProviders[i].ID = val
	case "base_url":
		cfg.CustomProviders[i].BaseURL = val
	case "model": // maps to DefaultModel (the recommended model id)
		cfg.CustomProviders[i].DefaultModel = val
	case "name":
		cfg.CustomProviders[i].Name = val
	}
}

// customProviderEmpty reports whether a slot carries no usable definition (an
// all-blank slot the scalar UI created and the user never filled in).
func customProviderEmpty(p FreeProvider) bool {
	return strings.TrimSpace(p.ID) == "" && strings.TrimSpace(p.BaseURL) == "" &&
		strings.TrimSpace(p.DefaultModel) == "" && strings.TrimSpace(p.Name) == ""
}

// normalizeCustomProviders trims trailing all-blank slots so a cleared slot
// doesn't linger. Only trailing blanks are dropped — a blank slot between two
// filled ones is kept so slot indices stay stable within one save.
func normalizeCustomProviders(cfg *Config) {
	for len(cfg.CustomProviders) > 0 && customProviderEmpty(cfg.CustomProviders[len(cfg.CustomProviders)-1]) {
		cfg.CustomProviders = cfg.CustomProviders[:len(cfg.CustomProviders)-1]
	}
}
