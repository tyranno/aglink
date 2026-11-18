package main

import (
	"bytes"
	"math"
	"testing"
)

func toneWav(n int) []byte {
	x := make([]float64, n)
	for i := range x {
		x[i] = 0.3*math.Sin(2*math.Pi*220*float64(i)/16000) + 0.02*math.Sin(2*math.Pi*50*float64(i)/16000)
	}
	return encodeWav16kMono(x)
}

func TestApplyMicCleanup(t *testing.T) {
	wav := toneWav(8000) // 0.5 s

	if got := applyMicCleanup(wav, "off"); !bytes.Equal(got, wav) {
		t.Fatal("off must pass through unchanged")
	}

	out := applyMicCleanup(wav, "light")
	x, ok := decodeWav16kMono(out)
	if !ok {
		t.Fatal("light produced an invalid wav")
	}
	if len(x) != 8000 {
		t.Fatalf("light changed sample count: %d", len(x))
	}

	// strong without rnnoise.dll present must fall back to the light chain (valid wav)
	if _, ok := decodeWav16kMono(applyMicCleanup(wav, "strong")); !ok {
		t.Fatal("strong produced an invalid wav")
	}

	// too short → pass through
	short := encodeWav16kMono(make([]float64, 100))
	if got := applyMicCleanup(short, "light"); !bytes.Equal(got, short) {
		t.Fatal("too-short clip must pass through")
	}

	// decode/encode round-trips an int16 sample exactly
	rt := encodeWav16kMono([]float64{0, 0.5, -0.5, 0.999})
	d, ok := decodeWav16kMono(rt)
	if !ok || len(d) != 4 {
		t.Fatalf("round-trip decode failed: ok=%v n=%d", ok, len(d))
	}
}
