package main

import (
	"encoding/binary"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// The reply is played here with winmm rather than by the page: a WebView2
// hidden in the tray does not play <audio>, so a spoken reply stayed silent
// until the window was opened again. It plays asynchronously, because only an
// async PlaySound can be cut off — barge-in has to silence it at once.

var procPlaySound = syscall.NewLazyDLL("winmm.dll").NewProc("PlaySoundW")

const (
	sndAsync     = 0x0001
	sndNoDefault = 0x0002
	sndMemory    = 0x0004
)

var (
	playMu   sync.Mutex
	playing  []byte        // the WAV winmm is reading; kept alive while it plays
	playStop chan struct{} // closed by stopWavNative
)

// playWavNative plays a WAV image and returns when it ends or stopWavNative
// cuts it off.
func playWavNative(wav []byte) error {
	d, ok := wavDuration(wav)
	if !ok {
		return syscall.EINVAL
	}
	playMu.Lock()
	stopLocked()
	playing = wav
	stop := make(chan struct{})
	playStop = stop
	r, _, err := procPlaySound.Call(uintptr(unsafe.Pointer(&wav[0])), 0, sndMemory|sndAsync|sndNoDefault)
	playMu.Unlock()
	if r == 0 {
		return err
	}
	select {
	case <-time.After(d + 50*time.Millisecond):
	case <-stop:
	}
	playMu.Lock()
	if playStop == stop {
		playStop = nil
		playing = nil
	}
	playMu.Unlock()
	return nil
}

func stopWavNative() {
	playMu.Lock()
	stopLocked()
	playMu.Unlock()
}

// stopLocked silences the current clip. Callers hold playMu.
func stopLocked() {
	procPlaySound.Call(0, 0, 0)
	if playStop != nil {
		close(playStop)
		playStop = nil
	}
	playing = nil
}

// wavDuration reads a PCM WAV header: byte rate from "fmt ", length from "data".
func wavDuration(wav []byte) (time.Duration, bool) {
	if len(wav) < 12 || string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		return 0, false
	}
	var byteRate, dataLen uint32
	for off := 12; off+8 <= len(wav); {
		id := string(wav[off : off+4])
		size := binary.LittleEndian.Uint32(wav[off+4 : off+8])
		body := off + 8
		switch id {
		case "fmt ":
			if body+12 <= len(wav) {
				byteRate = binary.LittleEndian.Uint32(wav[body+8 : body+12])
			}
		case "data":
			dataLen = size
			if rest := uint32(len(wav) - body); dataLen > rest {
				dataLen = rest
			}
		}
		if byteRate > 0 && dataLen > 0 {
			return time.Duration(float64(dataLen) / float64(byteRate) * float64(time.Second)), true
		}
		off = body + int(size) + int(size&1)
	}
	return 0, false
}
