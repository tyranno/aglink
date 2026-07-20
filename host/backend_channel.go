package main

import (
	"fmt"
	"strings"
)

// normalizeChannelBackendOverride returns the stored per-channel backend
// override. Empty means "inherit the current default backend".
func normalizeChannelBackendOverride(name string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "default", "inherit":
		return "", true
	case "claude", "codex":
		return strings.ToLower(strings.TrimSpace(name)), true
	default:
		return "", false
	}
}

func (m *Manager) effectiveBackend(override string) string {
	if backend, ok := normalizeChannelBackendOverride(override); ok && backend != "" {
		return backend
	}
	if backend := m.Backend(); backend != "" {
		return backend
	}
	return "claude"
}

func (m *Manager) requireBackendAvailable(backend string) error {
	if backend == "" {
		return nil
	}
	if m.clientForBackend(backend) == nil {
		return fmt.Errorf("%s backend is not available", strings.ToUpper(backend))
	}
	return nil
}
