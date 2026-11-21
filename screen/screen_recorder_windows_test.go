//go:build windows

package main

import "testing"

func TestParseStopKey(t *testing.T) {
	if vk, name, err := parseStopKey(""); err != nil || vk != 0x78 || name != "f9" {
		t.Fatalf(`parseStopKey("") = %x %q %v; want f9`, vk, name, err)
	}
	if vk, _, err := parseStopKey(" F10 "); err != nil || vk != 0x79 {
		t.Fatalf("F10 = %x %v", vk, err)
	}
	for _, bad := range []string{"ctrl+f9", "nosuchkey", "ctrl"} {
		if _, _, err := parseStopKey(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestClampRecordSeconds(t *testing.T) {
	for in, want := range map[int]int{0: recordMaxSecondsDefault, -3: recordMaxSecondsDefault, 1: 5, 60: 60, 99999: recordMaxSecondsCap} {
		if got := clampRecordSeconds(in); got != want {
			t.Errorf("clampRecordSeconds(%d) = %d, want %d", in, got, want)
		}
	}
}

// The hook path forwards only button-downs, only while a recording runs, and
// never blocks when the sink is full.
func TestRecordHookForwarding(t *testing.T) {
	h := &msllHookStruct{}
	h.Pt.X, h.Pt.Y = 10, 20

	recordMouseFromHook(wmLButtonDown, h) // no sink: must be a no-op

	ch := make(chan rawRecEvent, 1)
	recordSink.Store(&ch)
	t.Cleanup(func() { recordSink.Store(nil) })
	recordDropped.Store(0)

	recordMouseFromHook(0x0200, h) // WM_MOUSEMOVE: ignored
	recordMouseFromHook(wmLButtonDown, h)
	recordMouseFromHook(wmRButtonDown, h) // sink full: dropped, not blocked
	if len(ch) != 1 {
		t.Fatalf("want 1 forwarded event, got %d", len(ch))
	}
	ev := <-ch
	if !ev.mouse || ev.x != 10 || ev.y != 20 || ev.msg != wmLButtonDown {
		t.Errorf("forwarded %+v", ev)
	}
	if recordDropped.Load() != 1 {
		t.Errorf("dropped = %d, want 1", recordDropped.Load())
	}
}
