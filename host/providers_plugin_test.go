package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writePlugin drops a providers.d file and returns nothing; t.Fatal on failure.
func writePlugin(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestLoadProviderPlugins_SingleAndList covers one-per-file and list-per-file.
func TestLoadProviderPlugins_SingleAndList(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "together.yaml", `
id: together
name: Together AI
base_url: https://api.together.xyz/v1
default_model: some/free-model
free_note: note
signup_url: https://x
`)
	writePlugin(t, dir, "pair.yml", `
- id: alpha
  base_url: https://a.example/v1
  default_model: a-model
- id: beta
  base_url: https://b.example/v1
  default_model: b-model
`)
	got := loadProviderPlugins(dir)
	if len(got) != 3 {
		t.Fatalf("want 3 providers, got %d: %+v", len(got), got)
	}
	byID := map[string]FreeProvider{}
	for _, p := range got {
		byID[p.ID] = p
	}
	if byID["together"].BaseURL != "https://api.together.xyz/v1" {
		t.Errorf("together baseURL wrong: %+v", byID["together"])
	}
	if byID["beta"].DefaultModel != "b-model" {
		t.Errorf("beta model wrong: %+v", byID["beta"])
	}
	// Name defaults to id when omitted.
	if byID["alpha"].Name != "alpha" {
		t.Errorf("alpha name should default to id, got %q", byID["alpha"].Name)
	}
}

// TestLoadProviderPlugins_SkipsInvalid proves one bad file never nukes the rest,
// and that built-in ids can't be shadowed by a plugin.
func TestLoadProviderPlugins_SkipsInvalid(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "good.yaml", "id: good\nbase_url: https://g/v1\ndefault_model: m\n")
	writePlugin(t, dir, "missing.yaml", "id: bad\nname: no url\n")        // no base_url
	writePlugin(t, dir, "broken.yaml", "id: [this is not: valid yaml")    // parse error
	writePlugin(t, dir, "shadow.yaml", "id: groq\nbase_url: https://evil/v1\ndefault_model: x\n")

	got := loadProviderPlugins(dir)
	if len(got) != 1 || got[0].ID != "good" {
		t.Fatalf("want only [good], got %+v", got)
	}
}

// TestLoadProviderPlugins_MissingDir yields no plugins and no panic.
func TestLoadProviderPlugins_MissingDir(t *testing.T) {
	if got := loadProviderPlugins(filepath.Join(t.TempDir(), "nope")); got != nil {
		t.Fatalf("missing dir should yield nil, got %+v", got)
	}
	if got := loadProviderPlugins(""); got != nil {
		t.Fatalf("empty dir should yield nil, got %+v", got)
	}
}

// TestCatalogProviders_MountsPlugin is the end-to-end proof: a plugin-defined
// provider resolves via freeProviderByID and, once a key is present, mounts into
// the generated opencode.json exactly like a built-in — no rebuild path involved.
func TestCatalogProviders_MountsPlugin(t *testing.T) {
	// Point the data dir at a temp location so providerPluginDir() finds our file.
	home := t.TempDir()
	t.Setenv(dataDirEnv, home)
	pdir := filepath.Join(home, providerPluginDirName)
	writePlugin(t, pdir, "together.yaml",
		"id: together\nname: Together AI\nbase_url: https://api.together.xyz/v1\ndefault_model: free/model\n")

	if _, ok := freeProviderByID(nil, "together"); !ok {
		t.Fatal("plugin provider not resolved by freeProviderByID")
	}
	cfg := &Config{Providers: map[string]ProviderCred{
		"together": {APIKey: "sk-test"},
	}}
	prov := freeProviderProviders(cfg)
	entry, ok := prov["together"].(map[string]any)
	if !ok {
		t.Fatalf("together not mounted: %+v", prov)
	}
	opts := entry["options"].(map[string]any)
	if opts["baseURL"] != "https://api.together.xyz/v1" || opts["apiKey"] != "sk-test" {
		t.Errorf("mounted options wrong: %+v", opts)
	}
	models := entry["models"].(map[string]any)
	if _, ok := models["free/model"]; !ok {
		t.Errorf("default model not mounted: %+v", models)
	}
}
