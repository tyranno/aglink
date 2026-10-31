package main

import (
	"encoding/binary"
	"testing"
	"time"
)

func TestWavDuration(t *testing.T) {
	const rate, seconds = 44100, 2
	data := rate * 2 * seconds // 16-bit mono
	wav := make([]byte, 44+data)
	copy(wav[0:], "RIFF")
	copy(wav[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wav[16:], 16)
	binary.LittleEndian.PutUint32(wav[28:], rate*2)
	copy(wav[36:], "data")
	binary.LittleEndian.PutUint32(wav[40:], uint32(data))
	d, ok := wavDuration(wav)
	if !ok || d != seconds*time.Second {
		t.Fatalf("wavDuration = %v, %v; want %v", d, ok, seconds*time.Second)
	}
	if _, ok := wavDuration([]byte("not a wav")); ok {
		t.Fatal("garbage parsed as WAV")
	}
}
