package main

import (
	"context"
	"os"
)

// Settings that shape the daemon's behavior — which ports to search for app
// windows, which hosts to continue past certificate warnings, the pause between
// batch steps — are environment variables. The natural place to set them is
// the MCP registration (`claude mcp add -e KEY=VALUE …`), which is what the
// installers do. But that environment reaches only the bridge process Claude
// starts; the daemon is usually already running, started at logon, and never
// sees it. So the bridge forwards these keys with every call, and the daemon
// reads them from the call that carried them (callEnv) — never from a copy
// shared between calls, so two sessions registered with different values
// calling at the same moment each get their own.
var webSettingKeys = []string{
	"AGLINK_WEB_CDP_PORTS",
	"AGLINK_WEB_INSECURE_HOSTS",
	"AGLINK_WEB_STEP_DELAY_MS",
	"AGLINK_WEB_RECONNECT_WAIT_MS",
	// Approval gate (approvalgate.go): read per request, never from the
	// shared last-seen copy.
	"AGLINK_APPROVAL_URL",
	"AGLINK_APPROVAL_TOOLS",
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

// callEnv is the settings one call carried. A key the call did not set
// falls back to the daemon's own environment.
type callEnv map[string]string

func (e callEnv) get(key string) string {
	if v := e[key]; v != "" {
		return v
	}
	return os.Getenv(key)
}

type callEnvKey struct{}

// withCallEnv hands a call's settings down through ctx to code (app
// discovery, app tools) that only sees the context.
func withCallEnv(ctx context.Context, e callEnv) context.Context {
	return context.WithValue(ctx, callEnvKey{}, e)
}

// envFrom is the settings of the call ctx belongs to; nil (the daemon's own
// environment) when it carries none.
func envFrom(ctx context.Context) callEnv {
	e, _ := ctx.Value(callEnvKey{}).(callEnv)
	return e
}
