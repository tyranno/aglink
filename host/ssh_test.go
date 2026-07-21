package main

import (
	"strings"
	"testing"
)

func TestBuildSSHArgv_KeyAuth(t *testing.T) {
	h := SSHHost{Name: "gpu1", Host: "10.0.0.5", Port: 2222, User: "lab", KeyFile: `C:\keys\id`}
	argv, err := buildSSHArgv(h, "ssh", "sshpass", "nvidia-smi")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if argv[0] != "ssh" {
		t.Fatalf("key auth must not use sshpass, got %v", argv)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"-p 2222", "-i C:\\keys\\id", "BatchMode=yes", "lab@10.0.0.5", "nvidia-smi"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv missing %q: %v", want, joined)
		}
	}
}

func TestBuildSSHArgv_PasswordUsesSshpass(t *testing.T) {
	h := SSHHost{Name: "gpu1", Host: "10.0.0.5", User: "lab", Password: "secret"}
	argv, err := buildSSHArgv(h, "ssh", `C:\cygwin\bin\sshpass.exe`, "uptime")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if argv[0] != `C:\cygwin\bin\sshpass.exe` || argv[1] != "-p" || argv[2] != "secret" {
		t.Fatalf("password auth must prefix sshpass -p <pw>, got %v", argv[:3])
	}
	// default port applied
	if !strings.Contains(strings.Join(argv, " "), "-p 22") {
		t.Errorf("default port 22 not applied: %v", argv)
	}
}

func TestBuildSSHArgv_PasswordWithoutSshpassFails(t *testing.T) {
	h := SSHHost{Name: "gpu1", Host: "10.0.0.5", User: "lab", Password: "secret"}
	if _, err := buildSSHArgv(h, "ssh", "", "uptime"); err == nil {
		t.Fatal("expected error when password auth has no sshpass")
	}
}

func TestBuildSSHArgv_NoSSHBinaryFails(t *testing.T) {
	if _, err := buildSSHArgv(SSHHost{Host: "h"}, "", "sshpass", ""); err == nil {
		t.Fatal("expected error when ssh binary missing")
	}
}

func TestBuildSSHArgv_EmptyHostFails(t *testing.T) {
	if _, err := buildSSHArgv(SSHHost{Name: "x"}, "ssh", "sshpass", ""); err == nil {
		t.Fatal("expected error when host address empty")
	}
}

func TestFindSSHHost(t *testing.T) {
	hosts := []SSHHost{{Name: "gpu1"}, {Name: "Web"}}
	if _, ok := findSSHHost(hosts, "GPU1"); !ok {
		t.Error("case-insensitive lookup failed")
	}
	if _, ok := findSSHHost(hosts, "missing"); ok {
		t.Error("missing host should not be found")
	}
}

func TestRunSSHGatedByEnabled(t *testing.T) {
	cfg := &Config{SSHEnabled: false, SSHHosts: []SSHHost{{Name: "gpu1", Host: "h"}}}
	if _, err := runSSH(nil, cfg, "gpu1", "ls"); err == nil { //nolint:staticcheck // nil ctx unused before gate
		t.Fatal("runSSH must refuse when ssh.enabled is false")
	}
}
