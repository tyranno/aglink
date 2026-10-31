package main

import (
	"bytes"
	"image/png"
	"testing"
)

func TestTrayIconFor(t *testing.T) {
	if !bytes.Equal(trayIconFor(""), trayIcon) || !bytes.Equal(trayIconFor("note"), trayIcon) {
		t.Fatal("inactive / unknown phase should be the plain icon")
	}
	for _, phase := range []string{"listening", "transcribing", "waiting", "speaking", "problem"} {
		b := trayIconFor(phase)
		if bytes.Equal(b, trayIcon) {
			t.Errorf("%s: no pictogram", phase)
			continue
		}
		if _, err := png.Decode(bytes.NewReader(b)); err != nil {
			t.Errorf("%s: %v", phase, err)
		}
	}
}
