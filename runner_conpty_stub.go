//go:build !windows

package main

// Non-Windows: no ConPTY, so the interactive (B안) session backend is
// unavailable. Returns nil so main.go's cfg.InteractiveClaude branch logs and
// falls back to the normal headless client instead of failing to build.
func NewInteractiveClaudeRunner(claudePath string, cfgh *ConfigHolder) ClaudeClient {
	return nil
}
