package main

import (
	"os"
	"sync"
)

// Settings that shape the daemon's behavior — which ports to search for app
// windows, which hosts to continue past certificate warnings, the pause between
// batch steps — are environment variables. The natural place to set them is
// the MCP registration (`claude mcp add -e KEY=VALUE …`), which is what the
// installers do. But that environment reaches only the bridge process Claude
// starts; the daemon is usually already running, started at logon, and never
// sees it. So the bridge forwards these keys with every call, and the daemon
// reads them through webGetenv.
var webSettingKeys = []string{
	"AGLINK_WEB_CDP_PORTS",
	"AGLINK_WEB_INSECURE_HOSTS",
	"AGLINK_WEB_STEP_DELAY_MS",
}

// bridgeEnv collects the settings set in this (bridge) process's environment.
func bridgeEnv() map[string]string {
	var m map[string]string
	for _, k := range webSettingKeys {
		if v := os.Getenv(k); v != "" {
			if m == nil {
				m = map[string]string{}
			}
			m[k] = v
		}
	}
	return m
}

var clientEnv struct {
	sync.RWMutex
	m map[string]string
}

// setClientEnv records the settings the latest call carried. Each call
// replaces the whole set, so a key removed from the registration stops
// applying with the next call instead of lingering.
func setClientEnv(m map[string]string) {
	clientEnv.Lock()
	clientEnv.m = m
	clientEnv.Unlock()
}

// webGetenv reads a setting: what the caller's MCP registration says, else
// the daemon's own environment.
func webGetenv(key string) string {
	clientEnv.RLock()
	v := clientEnv.m[key]
	clientEnv.RUnlock()
	if v != "" {
		return v
	}
	return os.Getenv(key)
}
