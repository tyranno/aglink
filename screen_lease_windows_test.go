//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLeaseCanTake(t *testing.T) {
	const me = 4242
	const ttl = int64(8000)
	const now = int64(1_000_000)
	aliveYes := func(int) bool { return true }
	aliveNo := func(int) bool { return false }

	cases := []struct {
		name  string
		rec   *leaseRecord
		alive func(int) bool
		want  bool
	}{
		{"free (nil)", nil, aliveYes, true},
		{"free (pid 0)", &leaseRecord{OwnerPID: 0}, aliveYes, true},
		{"mine", &leaseRecord{OwnerPID: me, LastActivity: now - 100}, aliveYes, true},
		{"other fresh alive → busy", &leaseRecord{OwnerPID: me + 1, LastActivity: now - 100}, aliveYes, false},
		{"other stale", &leaseRecord{OwnerPID: me + 1, LastActivity: now - ttl - 1}, aliveYes, true},
		{"other fresh but dead", &leaseRecord{OwnerPID: me + 1, LastActivity: now - 100}, aliveNo, true},
		{"other exactly at ttl edge (not yet stale) alive → busy", &leaseRecord{OwnerPID: me + 1, LastActivity: now - ttl}, aliveYes, false},
	}
	for _, c := range cases {
		if got := leaseCanTake(c.rec, me, now, ttl, c.alive); got != c.want {
			t.Errorf("%s: leaseCanTake = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestLeaseTTLMS(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want int64
	}{
		{"default", "", leaseDefaultTTLMS},
		{"explicit", "5000", 5000},
		{"below min clamps", "10", leaseMinTTLMS},
		{"over max clamps", "9999999", leaseMaxTTLMS},
		{"invalid falls back", "abc", leaseDefaultTTLMS},
	}
	for _, c := range cases {
		t.Setenv("AGLINK_CONTROL_LEASE_TTL_MS", c.env)
		if got := leaseTTLMS(); got != c.want {
			t.Errorf("%s: leaseTTLMS() env %q = %d, want %d", c.name, c.env, got, c.want)
		}
	}
}

func TestReadWriteLeaseRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "screen-control.lock")

	// Missing file → free.
	if rec, _ := readLeaseFrom(path); rec != nil {
		t.Fatalf("missing file should read as free (nil), got %+v", rec)
	}
	in := &leaseRecord{OwnerPID: 777, OwnerLabel: "tg:1/turn:2", Since: 100, LastActivity: 200}
	if err := writeLeaseTo(path, in); err != nil {
		t.Fatalf("writeLeaseTo: %v", err)
	}
	out, _ := readLeaseFrom(path)
	if out == nil || *out != *in {
		t.Fatalf("round-trip mismatch: wrote %+v, read %+v", in, out)
	}

	// Corrupt content → free.
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rec, _ := readLeaseFrom(path); rec != nil {
		t.Errorf("corrupt file should read as free (nil), got %+v", rec)
	}
}

// withLeaseSeams points the lease at a temp file and installs a fixed clock and a
// liveness stub, restoring everything on cleanup.
func withLeaseSeams(t *testing.T, nowMS int64, alive func(int) bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "screen-control.lock")
	origPath, origNow, origAlive, origOff := leasePathFn, leaseNowMS, leaseAlive, controlLeaseOff
	t.Cleanup(func() {
		leasePathFn, leaseNowMS, leaseAlive, controlLeaseOff = origPath, origNow, origAlive, origOff
	})
	leasePathFn = func() (string, error) { return path, nil }
	leaseNowMS = func() int64 { return nowMS }
	leaseAlive = alive
	controlLeaseOff = false
	return path
}

func TestAcquireControlLease(t *testing.T) {
	me := os.Getpid()
	other := me + 1
	const now = int64(1_000_000)

	t.Run("takes when free", func(t *testing.T) {
		path := withLeaseSeams(t, now, func(int) bool { return true })
		if err := acquireControlLease(); err != nil {
			t.Fatalf("expected to take a free lease, got %v", err)
		}
		rec, _ := readLeaseFrom(path)
		if rec == nil || rec.OwnerPID != me || rec.LastActivity != now {
			t.Fatalf("lease not claimed by us: %+v", rec)
		}
	})

	t.Run("renews mine preserving since", func(t *testing.T) {
		path := withLeaseSeams(t, now, func(int) bool { return true })
		writeLeaseTo(path, &leaseRecord{OwnerPID: me, Since: 500, LastActivity: 600})
		if err := acquireControlLease(); err != nil {
			t.Fatalf("expected to renew my lease, got %v", err)
		}
		rec, _ := readLeaseFrom(path)
		if rec.Since != 500 {
			t.Errorf("since should be preserved at 500, got %d", rec.Since)
		}
		if rec.LastActivity != now {
			t.Errorf("last_activity should be renewed to %d, got %d", now, rec.LastActivity)
		}
	})

	t.Run("busy when other is live and fresh", func(t *testing.T) {
		path := withLeaseSeams(t, now, func(int) bool { return true })
		writeLeaseTo(path, &leaseRecord{OwnerPID: other, OwnerLabel: "tg:X", LastActivity: now - 200})
		err := acquireControlLease()
		if err == nil || !strings.Contains(err.Error(), "SCREEN_BUSY:") {
			t.Fatalf("expected SCREEN_BUSY error, got %v", err)
		}
		// The other session's lease must be left intact.
		rec, _ := readLeaseFrom(path)
		if rec == nil || rec.OwnerPID != other {
			t.Errorf("busy must not overwrite the owner's lease, got %+v", rec)
		}
	})

	t.Run("steals a stale lease", func(t *testing.T) {
		path := withLeaseSeams(t, now, func(int) bool { return true })
		writeLeaseTo(path, &leaseRecord{OwnerPID: other, LastActivity: now - leaseDefaultTTLMS - 1})
		if err := acquireControlLease(); err != nil {
			t.Fatalf("expected to steal a stale lease, got %v", err)
		}
		rec, _ := readLeaseFrom(path)
		if rec.OwnerPID != me {
			t.Errorf("stale lease should be taken over by us, got owner %d", rec.OwnerPID)
		}
	})

	t.Run("steals a dead owner's lease", func(t *testing.T) {
		path := withLeaseSeams(t, now, func(int) bool { return false })
		writeLeaseTo(path, &leaseRecord{OwnerPID: other, LastActivity: now - 200}) // fresh but owner "dead"
		if err := acquireControlLease(); err != nil {
			t.Fatalf("expected to reclaim a dead owner's lease, got %v", err)
		}
		rec, _ := readLeaseFrom(path)
		if rec.OwnerPID != me {
			t.Errorf("dead owner's lease should be reclaimed, got owner %d", rec.OwnerPID)
		}
	})

	t.Run("disabled bypasses entirely", func(t *testing.T) {
		path := withLeaseSeams(t, now, func(int) bool { return true })
		controlLeaseOff = true
		writeLeaseTo(path, &leaseRecord{OwnerPID: other, LastActivity: now - 200}) // would be busy if enabled
		if err := acquireControlLease(); err != nil {
			t.Fatalf("disabled lease must never refuse, got %v", err)
		}
	})
}

func TestControlStatusText(t *testing.T) {
	me := os.Getpid()
	other := me + 1
	const now = int64(1_000_000)

	t.Run("free", func(t *testing.T) {
		withLeaseSeams(t, now, func(int) bool { return true })
		if got := controlStatusText(); got != "control: free" {
			t.Errorf("got %q, want 'control: free'", got)
		}
	})

	t.Run("held by me", func(t *testing.T) {
		path := withLeaseSeams(t, now, func(int) bool { return true })
		writeLeaseTo(path, &leaseRecord{OwnerPID: me, Since: now - 3000, LastActivity: now - 100})
		if got := controlStatusText(); !strings.Contains(got, "held by me") {
			t.Errorf("got %q, want 'held by me'", got)
		}
	})

	t.Run("held by another", func(t *testing.T) {
		path := withLeaseSeams(t, now, func(int) bool { return true })
		writeLeaseTo(path, &leaseRecord{OwnerPID: other, OwnerLabel: "tg:X", LastActivity: now - 100})
		got := controlStatusText()
		if !strings.Contains(got, "held by another") || !strings.Contains(got, "tg:X") {
			t.Errorf("got %q, want 'held by another' incl label", got)
		}
	})

	t.Run("another expired reads free", func(t *testing.T) {
		path := withLeaseSeams(t, now, func(int) bool { return true })
		writeLeaseTo(path, &leaseRecord{OwnerPID: other, LastActivity: now - leaseDefaultTTLMS - 1})
		if got := controlStatusText(); !strings.Contains(got, "free") {
			t.Errorf("got %q, want free (expired)", got)
		}
	})
}
