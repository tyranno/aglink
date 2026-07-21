package main

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// SSHHost is one registered remote the !ssh command may reach. !ssh takes a host
// *name* (the registry key), never a raw host:port, so enabling SSH turns aglink
// into a client for exactly the hosts an operator listed — not an arbitrary
// outbound SSH client. Password and KeyFile are the two auth modes; KeyFile wins
// when both are set (key auth needs no sshpass and is the Windows-friendly path).
type SSHHost struct {
	Name     string `yaml:"name" json:"name"`         // registry key used by !ssh <name>
	Host     string `yaml:"host" json:"host"`         // hostname or IP
	Port     int    `yaml:"port" json:"port"`         // 0 → 22
	User     string `yaml:"user" json:"user"`         // login user
	Password string `yaml:"password" json:"password"` // used via sshpass when set and KeyFile empty
	KeyFile  string `yaml:"key_file" json:"key_file"` // private key path; preferred over Password
	Options  string `yaml:"options" json:"options"`   // extra raw ssh options, space-separated
}

// findSSHHost returns the named host from the registry (case-insensitive).
func findSSHHost(hosts []SSHHost, name string) (SSHHost, bool) {
	for _, h := range hosts {
		if strings.EqualFold(strings.TrimSpace(h.Name), strings.TrimSpace(name)) {
			return h, true
		}
	}
	return SSHHost{}, false
}

// buildSSHArgv builds the argv to run remoteCmd on host h. Key auth uses
// `ssh -i <key>` with BatchMode=yes (fail instead of hanging on a prompt).
// Password auth prefixes `sshpass -p <pw>` because ssh only reads a password from
// a tty, which never exists in a headless subprocess and would hang the turn —
// so a password host with no sshpass available is a hard error, not a hang.
// StrictHostKeyChecking=accept-new auto-trusts a first-seen host key (but still
// rejects a *changed* one); operators can override any of this via Options.
// sshPath/sshpassPath come from resolveToolPath.
func buildSSHArgv(h SSHHost, sshPath, sshpassPath, remoteCmd string) ([]string, error) {
	if sshPath == "" {
		return nil, fmt.Errorf("ssh 실행파일을 찾을 수 없습니다 (tools.ssh 설정 또는 PATH 확인)")
	}
	if strings.TrimSpace(h.Host) == "" {
		return nil, fmt.Errorf("호스트 주소가 비어 있습니다: %q", h.Name)
	}
	port := h.Port
	if port == 0 {
		port = 22
	}
	target := h.Host
	if u := strings.TrimSpace(h.User); u != "" {
		target = u + "@" + h.Host
	}

	usePassword := h.KeyFile == "" && h.Password != ""

	args := []string{sshPath,
		"-p", strconv.Itoa(port),
		"-o", "ConnectTimeout=15",
		"-o", "StrictHostKeyChecking=accept-new",
	}
	if h.KeyFile != "" {
		args = append(args, "-i", h.KeyFile, "-o", "BatchMode=yes")
	} else if !usePassword {
		// Neither key nor password → rely on agent/existing keys; fail fast rather
		// than block on an interactive prompt.
		args = append(args, "-o", "BatchMode=yes")
	}
	if opts := strings.Fields(h.Options); len(opts) > 0 {
		args = append(args, opts...)
	}
	args = append(args, target)
	if strings.TrimSpace(remoteCmd) != "" {
		args = append(args, remoteCmd)
	}

	if usePassword {
		if sshpassPath == "" {
			return nil, fmt.Errorf("비밀번호 인증에는 sshpass가 필요합니다 (tools.sshpass 설정). 또는 key_file로 키 인증을 쓰세요")
		}
		return append([]string{sshpassPath, "-p", h.Password}, args...), nil
	}
	return args, nil
}

// runSSH executes remoteCmd on the named host and returns combined stdout+stderr.
// It enforces the SSHEnabled gate and the registry (only named hosts), so it is
// safe to call straight from the command dispatcher. The context bounds the call
// so a hung connection can't freeze message processing.
func runSSH(ctx context.Context, cfg *Config, hostName, remoteCmd string) (string, error) {
	if cfg == nil || !cfg.SSHEnabled {
		return "", fmt.Errorf("SSH가 비활성화되어 있습니다 (config.yaml ssh.enabled=true 설정 필요)")
	}
	h, ok := findSSHHost(cfg.SSHHosts, hostName)
	if !ok {
		return "", fmt.Errorf("등록되지 않은 호스트: %q (ssh.hosts에 추가하세요)", hostName)
	}
	argv, err := buildSSHArgv(h, resolveToolPath(cfg, "ssh"), resolveToolPath(cfg, "sshpass"), remoteCmd)
	if err != nil {
		return "", err
	}
	out, runErr := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
	return string(out), runErr
}
