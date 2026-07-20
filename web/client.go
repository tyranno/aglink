package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

const extIDEnv = "AGLINK_WEB_EXT_ID"

// expectedExtID returns the pinned Chrome extension ID (from AGLINK_WEB_EXT_ID),
// or "" to accept any chrome-extension:// origin.
func expectedExtID() string {
	return strings.TrimSpace(os.Getenv(extIDEnv))
}

// callDaemon is used by the MCP bridge and the `cmd` fast-path. It ensures the
// daemon is running (auto-spawning it detached if not), then POSTs the command
// to /call and returns the result.
func callDaemon(method string, params map[string]any) CallResult {
	if err := ensureDaemon(); err != nil {
		return CallResult{Error: err.Error()}
	}
	port := readPort()
	body, _ := json.Marshal(callRequest{Method: method, Params: params})
	resp, err := http.Post(daemonBaseURL(port)+"/call", "application/json", bytes.NewReader(body))
	if err != nil {
		return CallResult{Error: fmt.Sprintf("browser daemon unavailable: %v", err)}
	}
	defer resp.Body.Close()
	var out CallResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return CallResult{Error: fmt.Sprintf("bad daemon response: %v", err)}
	}
	return out
}

// ensureDaemon returns nil once the daemon answers /health, spawning it
// detached if the current port is dead.
func ensureDaemon() error {
	if daemonHealthy(readPort()) {
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate self to start daemon: %w", err)
	}
	cmd := detachedCommand(self, "serve")
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
	// The daemon writes the port file on bind; poll /health until it is up.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if daemonHealthy(readPort()) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("browser daemon did not become ready")
}

func daemonHealthy(port int) bool {
	client := http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(daemonBaseURL(port) + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// detachedCommand builds an *exec.Cmd that keeps running after the bridge exits.
// The OS-specific process-attribute setup lives in proc_windows.go / proc_other.go.
func detachedCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	applyDetach(cmd)
	return cmd
}
