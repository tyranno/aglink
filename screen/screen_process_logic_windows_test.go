//go:build windows

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestProcNameMatchesIsExact(t *testing.T) {
	cases := []struct {
		proc, want string
		ok         bool
	}{
		{"notepad.exe", "notepad.exe", true},
		{"Notepad.exe", "NOTEPAD", true},
		{"notepad.exe", " notepad ", true},
		{"notepad++.exe", "notepad", false}, // no substring/prefix matching
		{"notepad.exe", "note", false},
		{"notepad.exe", "", false},
		{"System", "system", true},
	}
	for _, c := range cases {
		if got := procNameMatches(c.proc, c.want); got != c.ok {
			t.Errorf("procNameMatches(%q, %q) = %v, want %v", c.proc, c.want, got, c.ok)
		}
	}
}

func TestKillRefusal(t *testing.T) {
	const self, parent = 5000, 4000
	refused := []procInfo{
		{PID: 0, Name: "[System Process]"},
		{PID: 4, Name: "System"},
		{PID: 600, Name: "csrss.exe"},
		{PID: 601, Name: "WININIT.EXE"},
		{PID: 602, Name: "winlogon.exe"},
		{PID: 603, Name: "services.exe"},
		{PID: 604, Name: "lsass.exe"},
		{PID: 605, Name: "smss.exe"},
		{PID: 606, Name: "svchost.exe"},
		{PID: 607, Name: "aglink-screen.exe"},
		{PID: 608, Name: "aglink.exe"},
		{PID: self, Name: "renamed.exe"},
		{PID: parent, Name: "claude.exe"},
		{PID: 609, Name: "explorer.exe"},
	}
	for _, p := range refused {
		if killRefusal(p, self, parent, false) == "" {
			t.Errorf("%s (pid %d) should be refused", p.Name, p.PID)
		}
	}
	// force only lifts the explorer guard.
	if why := killRefusal(procInfo{PID: 609, Name: "explorer.exe"}, self, parent, true); why != "" {
		t.Errorf("explorer with force refused: %s", why)
	}
	for _, p := range refused[:len(refused)-1] {
		if killRefusal(p, self, parent, true) == "" {
			t.Errorf("force must not allow %s (pid %d)", p.Name, p.PID)
		}
	}
	if why := killRefusal(procInfo{PID: 7000, Name: "notepad.exe"}, self, parent, false); why != "" {
		t.Errorf("an ordinary process was refused: %s", why)
	}
}

func TestFilterSortProcs(t *testing.T) {
	ps := []procInfo{
		{PID: 10, Name: "chrome.exe", MemMB: 300, Title: "Inbox - Chrome"},
		{PID: 11, Name: "chrome.exe", MemMB: 900},
		{PID: 12, Name: "Notepad.exe", MemMB: 20, Title: "notes.txt - Notepad"},
		{PID: 13, Name: "code.exe", MemMB: 500},
	}
	pids := func(ps []procInfo) []uint32 {
		var out []uint32
		for _, p := range ps {
			out = append(out, p.PID)
		}
		return out
	}
	got, matched, err := filterSortProcs(ps, "", "", 0)
	if err != nil || matched != 4 || !equalPIDs(pids(got), []uint32{11, 13, 10, 12}) {
		t.Errorf("default memory sort = %v (%d, %v)", pids(got), matched, err)
	}
	got, _, _ = filterSortProcs(ps, "", "name", 0)
	if !equalPIDs(pids(got), []uint32{10, 11, 13, 12}) {
		t.Errorf("name sort = %v", pids(got))
	}
	got, matched, _ = filterSortProcs(ps, "INBOX", "", 0) // window title, case-insensitive
	if matched != 1 || got[0].PID != 10 {
		t.Errorf("title filter = %v", pids(got))
	}
	got, _, _ = filterSortProcs(ps, "12", "", 0) // exact pid
	if len(got) != 1 || got[0].PID != 12 {
		t.Errorf("pid filter = %v", pids(got))
	}
	got, matched, _ = filterSortProcs(ps, "chrome", "", 1)
	if matched != 2 || len(got) != 1 || got[0].PID != 11 {
		t.Errorf("limit = %v of %d", pids(got), matched)
	}
	if _, _, err := filterSortProcs(ps, "", "cpu", 0); err == nil {
		t.Error("an unknown sort should be rejected")
	}
	out := formatProcs(got, matched, len(ps), "memory")
	if !strings.Contains(out, "11 | chrome.exe | 900.0 MB") || !strings.Contains(out, "1 more not shown") {
		t.Errorf("formatProcs:\n%s", out)
	}
}

func equalPIDs(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestListAndKillOwnChild lists and kills a child process this test spawned
// itself, by pid — never a name, so no other process can be touched.
func TestListAndKillOwnChild(t *testing.T) {
	cmd := exec.Command("ping", "-n", "60", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn a child process: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	pid := cmd.Process.Pid

	ps, err := listProcesses()
	if err != nil {
		t.Fatalf("listProcesses: %v", err)
	}
	found := false
	for _, p := range ps {
		if int(p.PID) == pid {
			found = true
			if !strings.EqualFold(p.Name, "PING.EXE") {
				t.Errorf("child listed as %q", p.Name)
			}
			if p.MemMB <= 0 {
				t.Errorf("own child's memory should be readable, got %v", p.MemMB)
			}
		}
	}
	if !found {
		t.Fatalf("child pid %d not listed", pid)
	}

	killed, _, err := killProcesses(pid, "", false)
	if err != nil || len(killed) != 1 {
		t.Fatalf("killProcesses(child) = %v, %v", killed, err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("child still running after kill_process")
	}
}

// TestKillProcessRefusesSelf makes sure this very process is never killed.
func TestKillProcessRefusesSelf(t *testing.T) {
	// The test binary is not named aglink*, so this exercises the pid guard.
	killed, refused, err := killProcesses(os.Getpid(), "", false)
	if err == nil || len(killed) != 0 || len(refused) != 1 || !strings.Contains(refused[0], "aglink process") {
		t.Errorf("self kill = %v / %v / %v", killed, refused, err)
	}
}
