package main

import (
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func TestOnAnyScreen(t *testing.T) {
	screens := []*application.Screen{{WorkArea: application.Rect{X: 0, Y: 0, Width: 1920, Height: 1040}}}
	cases := []struct {
		x, y, w int
		want    bool
	}{
		{100, 100, 900, true},
		{1800, 500, 900, true},  // 120px still on screen
		{1850, 500, 900, false}, // only 70px left
		{-850, 10, 900, false},  // off the left edge
		{2500, 100, 900, false}, // monitor that is gone
		{100, 1020, 900, false}, // title bar below the work area
	}
	for _, c := range cases {
		if got := onAnyScreen(c.x, c.y, c.w, screens); got != c.want {
			t.Errorf("onAnyScreen(%d,%d,%d) = %v, want %v", c.x, c.y, c.w, got, c.want)
		}
	}
}
