package main

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
)

// Tray icon badges: while voice conversation runs, a coloured dot in the
// bottom-right corner of the tray icon says what it is doing, so the state can
// be told apart at a glance without hovering for the tooltip.
var trayBadgeColours = map[string]color.RGBA{
	"listening":    {34, 197, 94, 255},  // green: speak now
	"transcribing": {245, 158, 11, 255}, // amber: working out what was said
	"speaking":     {59, 130, 246, 255}, // blue: reading the reply
	"problem":      {239, 68, 68, 255},  // red
}

// trayIconFor returns the tray icon for a voice phase ("" = plain icon), building
// and caching the badged variants on first use. A broken base image falls back
// to the plain icon.
var trayIconCache = map[string][]byte{}

func trayIconFor(phase string) []byte {
	col, ok := trayBadgeColours[phase]
	if !ok {
		return trayIcon
	}
	if b, ok := trayIconCache[phase]; ok {
		return b
	}
	b, err := badgeIcon(trayIcon, col)
	if err != nil {
		return trayIcon
	}
	trayIconCache[phase] = b
	return b
}

// badgeIcon draws a filled dot with a white ring over the bottom-right of the
// PNG icon.
func badgeIcon(icon []byte, col color.RGBA) ([]byte, error) {
	src, err := png.Decode(bytes.NewReader(icon))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	img := image.NewRGBA(b)
	draw.Draw(img, b, src, b.Min, draw.Src)

	size := b.Dx()
	r := float64(size) * 0.24 // dot radius
	ring := float64(size) * 0.06
	cx := float64(b.Max.X) - r - ring
	cy := float64(b.Max.Y) - r - ring
	white := color.RGBA{255, 255, 255, 255}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dx, dy := float64(x)+0.5-cx, float64(y)+0.5-cy
			d2 := dx*dx + dy*dy
			switch {
			case d2 <= r*r:
				img.SetRGBA(x, y, col)
			case d2 <= (r+ring)*(r+ring):
				img.SetRGBA(x, y, white)
			}
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
