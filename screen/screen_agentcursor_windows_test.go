//go:build windows

package main

import (
	"testing"
	"time"
)

func TestAgentCursorEnabled(t *testing.T) {
	for env, want := range map[string]bool{"": true, "1": true, "on": true, "0": false, "false": false, "OFF": false, "no": false} {
		t.Setenv("AGLINK_SCREEN_AGENT_CURSOR", env)
		if got := agentCursorEnabled(); got != want {
			t.Errorf("AGLINK_SCREEN_AGENT_CURSOR=%q: enabled = %v, want %v", env, got, want)
		}
	}
}

func TestAgentCursorAlphaAt(t *testing.T) {
	if a := agentCursorAlphaAt(0); a != agentCursorAlpha {
		t.Errorf("alpha at start = %d, want full %d", a, agentCursorAlpha)
	}
	if a := agentCursorAlphaAt(agentCursorTotalMS - agentCursorFadeMS/2); a == 0 || a >= agentCursorAlpha {
		t.Errorf("alpha mid-fade = %d, want between 0 and full", a)
	}
	if a := agentCursorAlphaAt(agentCursorTotalMS); a != 0 {
		t.Errorf("alpha at end = %d, want 0", a)
	}
}

func TestShowAgentCursorHonorsEnv(t *testing.T) {
	orig := agentCursorShow
	t.Cleanup(func() { agentCursorShow = orig })
	shown := make(chan [2]int, 1)
	agentCursorShow = func(x, y int) { shown <- [2]int{x, y} }

	t.Setenv("AGLINK_SCREEN_AGENT_CURSOR", "0")
	showAgentCursor(1, 2)
	select {
	case <-shown:
		t.Fatal("disabled agent cursor must not show")
	case <-time.After(30 * time.Millisecond):
	}

	t.Setenv("AGLINK_SCREEN_AGENT_CURSOR", "")
	showAgentCursor(10, 20)
	select {
	case p := <-shown:
		if p != [2]int{10, 20} {
			t.Errorf("shown at %v, want (10,20)", p)
		}
	case <-time.After(time.Second):
		t.Fatal("agent cursor not shown")
	}
}
