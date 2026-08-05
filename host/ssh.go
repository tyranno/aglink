package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// SSHHost is one registered remote the !ssh command may reach. !ssh takes a host
// *name* (the registry key), never a raw host:port, so enabling SSH turns aglink
// into a client for exactly the hosts an operator listed — not an arbitrary
// outbound SSH client. Password and KeyFile are the two auth modes; KeyFile wins
// when both are set.
type SSHHost struct {
	Name     string `yaml:"name" json:"name"`         // registry key used by !ssh <name>
	Host     string `yaml:"host" json:"host"`         // hostname or IP
	Port     int    `yaml:"port" json:"port"`         // 0 → 22
	User     string `yaml:"user" json:"user"`         // login user
	Password string `yaml:"password" json:"password"` // used when KeyFile is empty
	KeyFile  string `yaml:"key_file" json:"key_file"` // private key path; preferred over Password
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

// knownHostsPath returns the known_hosts file aglink maintains for its own
// pure-Go SSH client (independent of any OpenSSH install's ~/.ssh/known_hosts).
// Created empty on first use.
func knownHostsPath() (string, error) {
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, "ssh_known_hosts")
	if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			return "", err
		}
	}
	return p, nil
}

// hostKeyCallback returns a trust-on-first-use HostKeyCallback backed by
// aglink's own known_hosts file: a key seen for the first time on an address
// is trusted and recorded, a key that later changes for the same address is
// rejected. This mirrors the StrictHostKeyChecking=accept-new behavior of the
// OpenSSH CLI without depending on it.
func hostKeyCallback(khPath string) (ssh.HostKeyCallback, error) {
	verify, err := knownhosts.New(khPath)
	if err != nil {
		return nil, err
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := verify(hostname, remote, key)
		if err == nil {
			return nil
		}
		var keyErr *knownhosts.KeyError
		if errors.As(err, &keyErr) && len(keyErr.Want) == 0 {
			f, oerr := os.OpenFile(khPath, os.O_APPEND|os.O_WRONLY, 0o600)
			if oerr != nil {
				return oerr
			}
			defer f.Close()
			line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)
			_, werr := f.WriteString(line + "\n")
			return werr
		}
		return err // known address, but the key changed → reject
	}, nil
}

// sshClientConfig builds the auth + host-key config for host h. Key auth wins
// when KeyFile is set; otherwise Password is sent directly to the remote over
// the encrypted transport — the pure-Go client reads it from config rather
// than a tty, so (unlike the old ssh.exe subprocess approach) no sshpass
// helper is needed for headless password login.
func sshClientConfig(h SSHHost) (*ssh.ClientConfig, error) {
	if strings.TrimSpace(h.Host) == "" {
		return nil, fmt.Errorf("호스트 주소가 비어 있습니다: %q", h.Name)
	}
	var auth []ssh.AuthMethod
	switch {
	case h.KeyFile != "":
		key, err := os.ReadFile(h.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("키 파일을 읽을 수 없습니다: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("키 파일 파싱 실패: %w", err)
		}
		auth = []ssh.AuthMethod{ssh.PublicKeys(signer)}
	case h.Password != "":
		auth = []ssh.AuthMethod{ssh.Password(h.Password)}
	default:
		return nil, fmt.Errorf("인증 수단이 없습니다: %q (key_file 또는 password를 설정하세요)", h.Name)
	}
	khPath, err := knownHostsPath()
	if err != nil {
		return nil, fmt.Errorf("known_hosts 파일을 열 수 없습니다: %w", err)
	}
	hkcb, err := hostKeyCallback(khPath)
	if err != nil {
		return nil, fmt.Errorf("호스트 키 검증 초기화 실패: %w", err)
	}
	return &ssh.ClientConfig{
		User:            h.User,
		Auth:            auth,
		HostKeyCallback: hkcb,
		Timeout:         15 * time.Second,
	}, nil
}

// dialSSHHost opens a pure-Go SSH connection to h, bounded by ctx for the TCP
// dial (the handshake itself uses the fixed timeout in sshClientConfig).
func dialSSHHost(ctx context.Context, h SSHHost) (*ssh.Client, error) {
	cfg, err := sshClientConfig(h)
	if err != nil {
		return nil, err
	}
	port := h.Port
	if port == 0 {
		port = 22
	}
	addr := net.JoinHostPort(h.Host, strconv.Itoa(port))
	d := net.Dialer{Timeout: 15 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("접속 실패: %w", err)
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("SSH 핸드셰이크 실패: %w", err)
	}
	return ssh.NewClient(sshConn, chans, reqs), nil
}

// dialRegisteredHost enforces the SSHEnabled gate and host registry (only
// names in ssh.hosts are reachable), then dials. Shared by runSSH and the
// SFTP put/get helpers so every remote entry point goes through the same
// checks.
func dialRegisteredHost(ctx context.Context, cfg *Config, hostName string) (*ssh.Client, error) {
	if cfg == nil || !cfg.SSHEnabled {
		return nil, fmt.Errorf("SSH가 비활성화되어 있습니다 (config.yaml ssh.enabled=true 설정 필요)")
	}
	h, ok := findSSHHost(cfg.SSHHosts, hostName)
	if !ok {
		return nil, fmt.Errorf("등록되지 않은 호스트: %q (ssh.hosts에 추가하세요)", hostName)
	}
	return dialSSHHost(ctx, h)
}

// runSSH executes remoteCmd on the named host over a pure-Go SSH connection
// (golang.org/x/crypto/ssh — no ssh.exe/sshpass subprocess) and returns
// combined stdout+stderr. The context bounds the whole call so a hung
// connection or command can't freeze message processing.
func runSSH(ctx context.Context, cfg *Config, hostName, remoteCmd string) (string, error) {
	client, err := dialRegisteredHost(ctx, cfg, hostName)
	if err != nil {
		return "", err
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("세션 생성 실패: %w", err)
	}
	defer session.Close()

	var out bytes.Buffer
	session.Stdout = &out
	session.Stderr = &out

	done := make(chan error, 1)
	go func() { done <- session.Run(remoteCmd) }()
	select {
	case runErr := <-done:
		return out.String(), runErr
	case <-ctx.Done():
		_ = session.Close()
		return out.String(), ctx.Err()
	}
}

// uploadSSHFile copies localPath to remotePath on the named host over SFTP —
// the pure-Go replacement for scp, reusing the same SSH connection/auth as
// runSSH. remotePath's parent directory must already exist on the remote.
func uploadSSHFile(ctx context.Context, cfg *Config, hostName, localPath, remotePath string) (int64, error) {
	client, err := dialRegisteredHost(ctx, cfg, hostName)
	if err != nil {
		return 0, err
	}
	defer client.Close()

	sc, err := sftp.NewClient(client)
	if err != nil {
		return 0, fmt.Errorf("SFTP 세션 생성 실패: %w", err)
	}
	defer sc.Close()

	src, err := os.Open(localPath)
	if err != nil {
		return 0, fmt.Errorf("로컬 파일을 열 수 없습니다: %w", err)
	}
	defer src.Close()

	dst, err := sc.Create(remotePath)
	if err != nil {
		return 0, fmt.Errorf("원격 파일 생성 실패: %w", err)
	}
	defer dst.Close()

	n, err := io.Copy(dst, src)
	if err != nil {
		return n, fmt.Errorf("전송 실패: %w", err)
	}
	return n, nil
}

// downloadSSHFile copies remotePath from the named host to localPath over
// SFTP. localPath's parent directory must already exist locally.
func downloadSSHFile(ctx context.Context, cfg *Config, hostName, remotePath, localPath string) (int64, error) {
	client, err := dialRegisteredHost(ctx, cfg, hostName)
	if err != nil {
		return 0, err
	}
	defer client.Close()

	sc, err := sftp.NewClient(client)
	if err != nil {
		return 0, fmt.Errorf("SFTP 세션 생성 실패: %w", err)
	}
	defer sc.Close()

	src, err := sc.Open(remotePath)
	if err != nil {
		return 0, fmt.Errorf("원격 파일을 열 수 없습니다: %w", err)
	}
	defer src.Close()

	dst, err := os.Create(localPath)
	if err != nil {
		return 0, fmt.Errorf("로컬 파일 생성 실패: %w", err)
	}
	defer dst.Close()

	n, err := io.Copy(dst, src)
	if err != nil {
		return n, fmt.Errorf("전송 실패: %w", err)
	}
	return n, nil
}
