//go:build windows

package main

import "testing"

// TestResolveKeyVKMediaKeys guards the media/system key additions — these are
// looked up directly from vkMap (no VkKeyScanW fallback needed, unlike a bare
// printable character), so a typo in the map would silently make e.g.
// key("volumedown") fail with "unknown key" instead of lowering the volume.
func TestResolveKeyVKMediaKeys(t *testing.T) {
	cases := []struct {
		name string
		want uint16
	}{
		{"volumeup", 0xAF},
		{"volumedown", 0xAE},
		{"volumemute", 0xAD},
		{"mute", 0xAD},
		{"medianext", 0xB0},
		{"mediaprev", 0xB1},
		{"mediastop", 0xB2},
		{"mediaplay", 0xB3},
		{"playpause", 0xB3},
		{"printscreen", 0x2C},
		{"prtsc", 0x2C},
	}
	for _, c := range cases {
		got, ok := resolveKeyVK(c.name)
		if !ok {
			t.Errorf("resolveKeyVK(%q) = not found, want vk=0x%02X", c.name, c.want)
			continue
		}
		if got != c.want {
			t.Errorf("resolveKeyVK(%q) = 0x%02X, want 0x%02X", c.name, got, c.want)
		}
	}
}
