package main

import (
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Provider plugins: a fully rebuild-free way to add a brand-new OpenAI-compatible
// backend to the free-remote catalog. The built-in freeProviderCatalog (Go code)
// still ships the vetted defaults, but any extra backend can be added at runtime
// by dropping a small YAML file into <dataDir>/providers.d — no recompile, no
// binary swap. Keys are still supplied the normal way (settings UI /
// providers.<id>.api_key), because the settings section is data-driven from the
// effective catalog, so a plugin-defined provider automatically grows its own
// key/model rows in the UI.
//
// Example <dataDir>/providers.d/together.yaml:
//
//	id: together
//	name: Together AI
//	base_url: https://api.together.xyz/v1
//	default_model: meta-llama/Llama-3.3-70B-Instruct-Turbo-Free
//	free_note: 무료 모델 일부 제공.
//	signup_url: https://api.together.ai/settings/api-keys
//
// A file may hold a single provider (mapping) or a list of providers (sequence).

// providerPluginDirName is the drop-in directory under the data dir.
const providerPluginDirName = "providers.d"

// providerPluginDir returns the drop-in directory path (created lazily elsewhere;
// missing dir just means "no plugins"). Empty string when the data dir is
// unavailable, which callers treat as "built-ins only".
func providerPluginDir() string {
	dir, err := dataDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, providerPluginDirName)
}

// loadProviderPlugins reads every *.yaml / *.yml in dir and returns the valid
// provider definitions in filename order. Invalid entries (missing required
// fields, unparsable YAML, or an id that shadows a built-in) are skipped with a
// log line rather than failing the whole load — one bad plugin never breaks the
// others or the built-ins. A missing dir yields an empty slice and no error.
func loadProviderPlugins(dir string) []FreeProvider {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // dir absent or unreadable → no plugins
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext == ".yaml" || ext == ".yml" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	builtin := map[string]bool{}
	for _, p := range freeProviderCatalog {
		builtin[p.ID] = true
	}

	var out []FreeProvider
	seen := map[string]bool{}
	for _, name := range names {
		path := filepath.Join(dir, name)
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			log.Printf("provider 플러그인 읽기 실패 (%s): %v", name, rerr)
			continue
		}
		defs, perr := parseProviderPlugin(b)
		if perr != nil {
			log.Printf("provider 플러그인 파싱 실패 (%s): %v", name, perr)
			continue
		}
		for _, d := range defs {
			d.ID = strings.TrimSpace(d.ID)
			d.BaseURL = strings.TrimSpace(d.BaseURL)
			d.DefaultModel = strings.TrimSpace(d.DefaultModel)
			if d.ID == "" || d.BaseURL == "" || d.DefaultModel == "" {
				log.Printf("provider 플러그인 무시 (%s): id·base_url·default_model 필수", name)
				continue
			}
			if builtin[d.ID] {
				log.Printf("provider 플러그인 무시 (%s): id %q는 내장 프로바이더와 충돌", name, d.ID)
				continue
			}
			if seen[d.ID] {
				log.Printf("provider 플러그인 무시 (%s): id %q 중복", name, d.ID)
				continue
			}
			if strings.TrimSpace(d.Name) == "" {
				d.Name = d.ID
			}
			seen[d.ID] = true
			out = append(out, d)
		}
	}
	return out
}

// parseProviderPlugin decodes a plugin file as either a single provider mapping
// or a sequence of them, so one file can define one or many backends.
func parseProviderPlugin(b []byte) ([]FreeProvider, error) {
	// Try a sequence first; fall back to a single mapping.
	var list []FreeProvider
	if err := yaml.Unmarshal(b, &list); err == nil && len(list) > 0 && list[0].ID != "" {
		return list, nil
	}
	var one FreeProvider
	if err := yaml.Unmarshal(b, &one); err != nil {
		return nil, err
	}
	return []FreeProvider{one}, nil
}

// catalogProviders returns the effective catalog: the built-in list, then valid
// drop-in plugin providers (providers.d/*.yaml), then the user's UI-defined
// custom providers (cfg.CustomProviders). This is the single source of truth
// every UI/render path consults, so any of the three surfaces everywhere
// (settings key rows, id whitelist, opencode.json mount) with no other wiring.
// Ids that shadow an earlier entry (a built-in, a plugin, or a duplicate) are
// dropped so a custom/plugin definition can never override a vetted built-in.
// The plugin dir is re-read on each call so a freshly dropped file is picked up
// without a restart; the read is a handful of tiny files and callers are not hot
// loops. cfg may be nil (built-ins + plugins only).
func catalogProviders(cfg *Config) []FreeProvider {
	var custom []FreeProvider
	if cfg != nil {
		custom = cfg.CustomProviders
	}
	plugins := loadProviderPlugins(providerPluginDir())
	if len(plugins) == 0 && len(custom) == 0 {
		return freeProviderCatalog
	}
	out := make([]FreeProvider, 0, len(freeProviderCatalog)+len(plugins)+len(custom))
	out = append(out, freeProviderCatalog...)
	seen := map[string]bool{}
	for _, p := range out {
		seen[p.ID] = true
	}
	appendValid := func(defs []FreeProvider) {
		for _, d := range defs {
			d.ID = strings.TrimSpace(d.ID)
			d.BaseURL = strings.TrimSpace(d.BaseURL)
			d.DefaultModel = strings.TrimSpace(d.DefaultModel)
			// A half-filled custom slot (the scalar UI creates blanks) or an
			// id that shadows an earlier entry is silently skipped — it just
			// stays out of the effective catalog, never overriding a built-in.
			if d.ID == "" || d.BaseURL == "" || d.DefaultModel == "" || seen[d.ID] {
				continue
			}
			if strings.TrimSpace(d.Name) == "" {
				d.Name = d.ID
			}
			seen[d.ID] = true
			out = append(out, d)
		}
	}
	appendValid(plugins) // loadProviderPlugins already validated these; the seen check just guards against a plugin colliding with a built-in id
	appendValid(custom)
	return out
}
