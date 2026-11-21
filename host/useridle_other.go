//go:build !windows

package main

import "time"

// userIdleDuration: the idle time is unknown off Windows (a Linux host is a
// server, not the PC the user sits at), so away notifications never fire.
func userIdleDuration() (time.Duration, bool) { return 0, false }
