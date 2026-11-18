package main

// Optional mic cleanup before whisper (mirrors the aglink-voice engine's
// AudioCleanup): a high-pass, conservative spectral-subtraction noise
// suppression, and AGC — for reverberant rooms, far-field mics and ambient
// noise. "strong" additionally runs RNNoise (rnnoise.dll next to the exe).
// It NEVER changes the turn on failure — any problem returns the original wav.
//
// level: "off" (passthrough) | "light" (pure-Go chain) | "strong" (RNNoise).

import (
	"bytes"
	"encoding/binary"
	"log"
	"math"
	"syscall"
	"unsafe"
)

// applyMicCleanup returns a cleaned 16 kHz mono WAV, or the input unchanged on
// any problem (wrong format, too short, errors).
func applyMicCleanup(wav []byte, level string) []byte {
	if level == "" || level == "off" {
		return wav
	}
	x, ok := decodeWav16kMono(wav)
	if !ok || len(x) < 1024 {
		return wav
	}
	defer func() { _ = recover() }() // never crash the turn
	if level == "strong" && rnnoiseStrong(x) {
		log.Printf("[voice] mic-cleanup: rnnoise")
		return encodeWav16kMono(x)
	}
	highPass(x, 16000, 90)
	spectralSubtract(x)
	agc(x)
	log.Printf("[voice] mic-cleanup: light%s", map[bool]string{true: " (rnnoise unavailable)", false: ""}[level == "strong"])
	return encodeWav16kMono(x)
}

// decodeWav16kMono parses a 16 kHz mono 16-bit PCM wav into float64 [-1,1].
func decodeWav16kMono(wav []byte) ([]float64, bool) {
	if len(wav) < 44 || string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		return nil, false
	}
	var channels, bits uint16
	var rate uint32
	var data []byte
	for p := 12; p+8 <= len(wav); {
		id := string(wav[p : p+4])
		sz := int(binary.LittleEndian.Uint32(wav[p+4 : p+8]))
		body := p + 8
		if body+sz > len(wav) {
			sz = len(wav) - body
		}
		switch id {
		case "fmt ":
			if sz >= 16 {
				channels = binary.LittleEndian.Uint16(wav[body+2 : body+4])
				rate = binary.LittleEndian.Uint32(wav[body+4 : body+8])
				bits = binary.LittleEndian.Uint16(wav[body+14 : body+16])
			}
		case "data":
			data = wav[body : body+sz]
		}
		p = body + sz + (sz & 1)
	}
	if channels != 1 || bits != 16 || rate != 16000 || len(data) < 2 {
		return nil, false
	}
	n := len(data) / 2
	x := make([]float64, n)
	for i := 0; i < n; i++ {
		x[i] = float64(int16(binary.LittleEndian.Uint16(data[i*2:]))) / 32768
	}
	return x, true
}

func encodeWav16kMono(x []float64) []byte {
	n := len(x)
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+n*2))
	b.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(16000), uint32(32000), uint16(2), uint16(16)} {
		_ = binary.Write(&b, binary.LittleEndian, v)
	}
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(n*2))
	buf := make([]byte, n*2)
	for i, s := range x {
		if s > 1 {
			s = 1
		} else if s < -1 {
			s = -1
		}
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(int16(math.Round(s*32767))))
	}
	b.Write(buf)
	return b.Bytes()
}

// highPass is a one-pass biquad high-pass (RBJ cookbook, Q=0.707).
func highPass(x []float64, rate, cutoff float64) {
	w0 := 2 * math.Pi * cutoff / rate
	cw, sw := math.Cos(w0), math.Sin(w0)
	alpha := sw / (2 * 0.707)
	b0 := (1 + cw) / 2
	b1 := -(1 + cw)
	b2 := (1 + cw) / 2
	a0 := 1 + alpha
	a1 := -2 * cw
	a2 := 1 - alpha
	b0, b1, b2, a1, a2 = b0/a0, b1/a0, b2/a0, a1/a0, a2/a0
	var x1, x2, y1, y2 float64
	for i, in := range x {
		out := b0*in + b1*x1 + b2*x2 - a1*y1 - a2*y2
		x2, x1 = x1, in
		y2, y1 = y1, out
		x[i] = out
	}
}

// spectralSubtract: conservative 50%-overlap Hann STFT noise subtraction.
func spectralSubtract(x []float64) {
	const N, hop = 512, 256
	const alpha, beta = 1.5, 0.08
	frames := (len(x)-N)/hop + 1
	if frames < 4 {
		return
	}
	win := make([]float64, N)
	for i := range win {
		win[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(N-1))
	}
	re := make([][]float64, frames)
	im := make([][]float64, frames)
	noise := make([]float64, N/2+1)
	magcol := make([]float64, frames)
	for t := 0; t < frames; t++ {
		fr := make([]float64, N)
		fi := make([]float64, N)
		off := t * hop
		for i := 0; i < N; i++ {
			fr[i] = x[off+i] * win[i]
		}
		fft(fr, fi, false)
		re[t], im[t] = fr, fi
	}
	for b := 0; b <= N/2; b++ {
		for t := 0; t < frames; t++ {
			magcol[t] = math.Hypot(re[t][b], im[t][b])
		}
		cp := append([]float64(nil), magcol...)
		insertionSort(cp)
		noise[b] = cp[frames/10]
	}
	out := make([]float64, len(x))
	wsum := make([]float64, len(x))
	for t := 0; t < frames; t++ {
		fr, fi := re[t], im[t]
		for b := 0; b <= N/2; b++ {
			m := math.Hypot(fr[b], fi[b])
			g := beta
			if m > 1e-12 {
				if v := (m - alpha*noise[b]) / m; v > g {
					g = v
				}
			} else {
				g = 0
			}
			fr[b] *= g
			fi[b] *= g
			if b > 0 && b < N/2 {
				c := N - b
				fr[c] = fr[b]
				fi[c] = -fi[b]
			}
		}
		fft(fr, fi, true)
		off := t * hop
		for i := 0; i < N; i++ {
			out[off+i] += fr[i] * win[i]
			wsum[off+i] += win[i] * win[i]
		}
	}
	for i := range x {
		if wsum[i] > 1e-9 {
			x[i] = out[i] / wsum[i]
		}
	}
}

func insertionSort(a []float64) {
	for i := 1; i < len(a); i++ {
		v := a[i]
		j := i - 1
		for j >= 0 && a[j] > v {
			a[j+1] = a[j]
			j--
		}
		a[j+1] = v
	}
}

func agc(x []float64) {
	var sum float64
	for _, v := range x {
		sum += v * v
	}
	rms := math.Sqrt(sum / float64(max(1, len(x))))
	if rms < 1e-5 {
		return
	}
	gain := 0.12 / rms
	if gain > 8 {
		gain = 8
	}
	for i := range x {
		v := x[i] * gain
		if v > 0.98 {
			v = 0.98
		} else if v < -0.98 {
			v = -0.98
		}
		x[i] = v
	}
}

// fft: in-place iterative radix-2 Cooley-Tukey. inverse=true divides by n.
func fft(re, im []float64, inverse bool) {
	n := len(re)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			re[i], re[j] = re[j], re[i]
			im[i], im[j] = im[j], im[i]
		}
	}
	for length := 2; length <= n; length <<= 1 {
		ang := 2 * math.Pi / float64(length)
		if !inverse {
			ang = -ang
		}
		wr, wi := math.Cos(ang), math.Sin(ang)
		for i := 0; i < n; i += length {
			cr, ci := 1.0, 0.0
			for k := 0; k < length/2; k++ {
				a, b := i+k, i+k+length/2
				tr := re[b]*cr - im[b]*ci
				ti := re[b]*ci + im[b]*cr
				re[b], im[b] = re[a]-tr, im[a]-ti
				re[a], im[a] = re[a]+tr, im[a]+ti
				cr, ci = cr*wr-ci*wi, cr*wi+ci*wr
			}
		}
	}
	if inverse {
		for i := 0; i < n; i++ {
			re[i] /= float64(n)
			im[i] /= float64(n)
		}
	}
}

// ---- RNNoise (strong) -------------------------------------------------------
var (
	rnnoiseDLL = syscall.NewLazyDLL("rnnoise.dll")
	rnCreate   = rnnoiseDLL.NewProc("rnnoise_create")
	rnDestroy  = rnnoiseDLL.NewProc("rnnoise_destroy")
	rnProcess  = rnnoiseDLL.NewProc("rnnoise_process_frame")
)

// rnnoiseStrong denoises 16 kHz x in place via RNNoise at 48 kHz. Returns false
// (caller then uses the light chain) when rnnoise.dll is missing or anything fails.
func rnnoiseStrong(x []float64) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	if rnnoiseDLL.Load() != nil || rnCreate.Find() != nil || rnProcess.Find() != nil {
		return false
	}
	st, _, _ := rnCreate.Call(0)
	if st == 0 {
		return false
	}
	defer rnDestroy.Call(st)

	highPass(x, 16000, 90)
	up := upsample3(x)
	for i := range up {
		up[i] *= 32768
	}
	const F = 480
	inb := make([]float32, F)
	outb := make([]float32, F)
	for off := 0; off < len(up); off += F {
		for i := range inb {
			inb[i] = 0
		}
		m := F
		if off+F > len(up) {
			m = len(up) - off
		}
		for i := 0; i < m; i++ {
			inb[i] = float32(up[off+i])
		}
		rnProcess.Call(st, uintptr(unsafe.Pointer(&outb[0])), uintptr(unsafe.Pointer(&inb[0])))
		for i := 0; i < m; i++ {
			up[off+i] = float64(outb[i])
		}
	}
	for i := range up {
		up[i] /= 32768
	}
	downsample3(up, x)
	agc(x)
	return true
}

func upsample3(x []float64) []float64 {
	y := make([]float64, len(x)*3)
	for i := 0; i < len(x); i++ {
		a := x[i]
		b := a
		if i+1 < len(x) {
			b = x[i+1]
		}
		y[i*3] = a
		y[i*3+1] = a + (b-a)/3
		y[i*3+2] = a + 2*(b-a)/3
	}
	return y
}

func downsample3(up, dst []float64) {
	for i := range dst {
		j := i * 3
		s := up[j]
		if j+1 < len(up) {
			s += up[j+1]
		}
		if j+2 < len(up) {
			s += up[j+2]
		}
		dst[i] = s / 3
	}
}
