package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindSSHHost(t *testing.T) {
	hosts := []SSHHost{{Name: "gpu1"}, {Name: "Web"}}
	if _, ok := findSSHHost(hosts, "GPU1"); !ok {
		t.Error("case-insensitive lookup failed")
	}
	if _, ok := findSSHHost(hosts, "missing"); ok {
		t.Error("missing host should not be found")
	}
}

func TestSSHClientConfig_EmptyHostFails(t *testing.T) {
	if _, err := sshClientConfig(SSHHost{Name: "x"}); err == nil {
		t.Fatal("expected error when host address empty")
	}
}

func TestSSHClientConfig_NoAuthFails(t *testing.T) {
	if _, err := sshClientConfig(SSHHost{Name: "x", Host: "10.0.0.5"}); err == nil {
		t.Fatal("expected error when neither key_file nor password is set")
	}
}

func TestSSHClientConfig_PasswordAuth(t *testing.T) {
	cfg, err := sshClientConfig(SSHHost{Name: "gpu1", Host: "10.0.0.5", User: "lab", Password: "secret"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg.User != "lab" || len(cfg.Auth) != 1 {
		t.Fatalf("unexpected client config: %+v", cfg)
	}
}

func TestSSHClientConfig_MissingKeyFileFails(t *testing.T) {
	if _, err := sshClientConfig(SSHHost{Name: "x", Host: "h", KeyFile: filepath.Join(t.TempDir(), "nope")}); err == nil {
		t.Fatal("expected error when key file is missing")
	}
}

func TestSSHClientConfig_KeyAuthPrefersKeyOverPassword(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_ed25519")
	// A minimal ed25519 PEM key generated once for this test; any invalid key
	// content is fine here since we only assert key auth is attempted (and
	// fails to parse) rather than falling back to password auth.
	if err := os.WriteFile(keyPath, []byte("not-a-real-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := sshClientConfig(SSHHost{Name: "x", Host: "h", KeyFile: keyPath, Password: "secret"})
	if err == nil || !strings.Contains(err.Error(), "키 파일") {
		t.Fatalf("expected key-parse error (key must win over password), got %v", err)
	}
}

func TestRunSSHGatedByEnabled(t *testing.T) {
	cfg := &Config{SSHEnabled: false, SSHHosts: []SSHHost{{Name: "gpu1", Host: "h"}}}
	if _, err := runSSH(context.Background(), cfg, "gpu1", "ls"); err == nil {
		t.Fatal("runSSH must refuse when ssh.enabled is false")
	}
}

func TestRunSSHUnregisteredHostFails(t *testing.T) {
	cfg := &Config{SSHEnabled: true, SSHHosts: []SSHHost{{Name: "gpu1", Host: "h"}}}
	if _, err := runSSH(context.Background(), cfg, "missing", "ls"); err == nil {
		t.Fatal("runSSH must refuse an unregistered host name")
	}
}

func TestUploadSSHFileGatedByEnabled(t *testing.T) {
	cfg := &Config{SSHEnabled: false, SSHHosts: []SSHHost{{Name: "gpu1", Host: "h"}}}
	if _, err := uploadSSHFile(context.Background(), cfg, "gpu1", "local", "remote"); err == nil {
		t.Fatal("uploadSSHFile must refuse when ssh.enabled is false")
	}
}

func TestDownloadSSHFileGatedByEnabled(t *testing.T) {
	cfg := &Config{SSHEnabled: false, SSHHosts: []SSHHost{{Name: "gpu1", Host: "h"}}}
	if _, err := downloadSSHFile(context.Background(), cfg, "gpu1", "remote", "local"); err == nil {
		t.Fatal("downloadSSHFile must refuse when ssh.enabled is false")
	}
}
