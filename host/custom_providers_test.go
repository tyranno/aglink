package main

import "testing"

// TestCustomProviders_MergedIntoCatalog proves a UI-defined custom provider
// resolves via freeProviderByID and mounts into the generated opencode.json once
// a key is present — the same rebuild-free path a providers.d plugin gets, but
// stored in config.yaml (cfg.CustomProviders) instead of a hand-dropped file.
func TestCustomProviders_MergedIntoCatalog(t *testing.T) {
	cfg := &Config{
		CustomProviders: []FreeProvider{
			{ID: "acme", BaseURL: "https://api.acme.test/v1", DefaultModel: "acme-large"},
		},
		Providers: map[string]ProviderCred{"acme": {APIKey: "sk-acme"}},
	}
	if _, ok := freeProviderByID(cfg, "acme"); !ok {
		t.Fatal("custom provider not resolved by freeProviderByID")
	}
	prov := freeProviderProviders(cfg)
	entry, ok := prov["acme"].(map[string]any)
	if !ok {
		t.Fatalf("acme not mounted: %+v", prov)
	}
	opts := entry["options"].(map[string]any)
	if opts["baseURL"] != "https://api.acme.test/v1" || opts["apiKey"] != "sk-acme" {
		t.Errorf("mounted options wrong: %+v", opts)
	}
	if _, ok := entry["models"].(map[string]any)["acme-large"]; !ok {
		t.Errorf("default model not mounted: %+v", entry["models"])
	}
}

// TestCustomProviders_CannotShadowBuiltin ensures a custom id colliding with a
// built-in (e.g. "groq") is dropped, so a UI definition can never override a
// vetted built-in's base_url.
func TestCustomProviders_CannotShadowBuiltin(t *testing.T) {
	cfg := &Config{CustomProviders: []FreeProvider{
		{ID: "groq", BaseURL: "https://evil.test/v1", DefaultModel: "x"},
	}}
	p, ok := freeProviderByID(cfg, "groq")
	if !ok {
		t.Fatal("groq should still resolve as the built-in")
	}
	if p.BaseURL == "https://evil.test/v1" {
		t.Errorf("custom provider shadowed the built-in groq: %+v", p)
	}
}

// TestCustomProviders_HalfFilledSlotSkipped ensures a slot missing a required
// field (the scalar UI creates blanks) stays out of the effective catalog.
func TestCustomProviders_HalfFilledSlotSkipped(t *testing.T) {
	cfg := &Config{CustomProviders: []FreeProvider{
		{ID: "acme"}, // no base_url / default_model
	}}
	if _, ok := freeProviderByID(cfg, "acme"); ok {
		t.Error("half-filled custom provider should not resolve")
	}
}

// TestApplyCustomProviderSetting_SlotEditAndNormalize covers the scalar-slot
// apply path: filling slot 1 defines a provider; a trailing all-blank slot 2 is
// trimmed by normalizeCustomProviders.
func TestApplyCustomProviderSetting_SlotEditAndNormalize(t *testing.T) {
	cfg := &Config{}
	err := applySettings(cfg, map[string]any{
		"custom_provider.1.id":        "acme",
		"custom_provider.1.base_url":  "https://api.acme.test/v1",
		"custom_provider.1.model":     "acme-large",
		"custom_provider.1.name":      "ACME",
		"custom_provider.2.id":        "", // touched-but-blank slot 2
		"custom_provider.2.base_url":  "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.CustomProviders) != 1 {
		t.Fatalf("expected trailing blank slot trimmed, got %+v", cfg.CustomProviders)
	}
	p := cfg.CustomProviders[0]
	if p.ID != "acme" || p.BaseURL != "https://api.acme.test/v1" || p.DefaultModel != "acme-large" || p.Name != "ACME" {
		t.Errorf("slot 1 not applied correctly: %+v", p)
	}
}

// TestCustomProviders_RoundTrip ensures custom_providers survives a YAML
// marshal/unmarshal cycle (so a UI-added provider persists across restarts).
func TestCustomProviders_RoundTrip(t *testing.T) {
	cfg := &Config{
		TelegramBotToken: "t",
		AllowedUserIDs:   []int64{1},
		CustomProviders: []FreeProvider{
			{ID: "acme", Name: "ACME", BaseURL: "https://api.acme.test/v1", DefaultModel: "acme-large"},
		},
	}
	raw, err := marshalConfigYAML(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := unmarshalConfigYAML(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.CustomProviders) != 1 || got.CustomProviders[0].ID != "acme" ||
		got.CustomProviders[0].BaseURL != "https://api.acme.test/v1" ||
		got.CustomProviders[0].DefaultModel != "acme-large" {
		t.Errorf("custom_providers round-trip lost data: %+v", got.CustomProviders)
	}
}
