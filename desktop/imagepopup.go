package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// OpenImage writes a (base64 PNG) chat image to a temp file and opens it in the
// OS default image viewer. Unlike an in-app modal, that is a real, movable and
// resizable window — the user can reposition/shrink it and keep typing in aglink
// while the image stays open. Accepts a bare base64 string or a full data URL.
func (c *ControlService) OpenImage(b64 string) error {
	if strings.HasPrefix(b64, "data:") {
		if i := strings.Index(b64, ","); i >= 0 {
			b64 = b64[i+1:]
		}
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return fmt.Errorf("이미지 디코드 실패: %w", err)
	}
	f, err := os.CreateTemp("", "aglink-img-*.png")
	if err != nil {
		return fmt.Errorf("임시 파일 생성 실패: %w", err)
	}
	path := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("이미지 쓰기 실패: %w", err)
	}
	f.Close()

	// Open with the registered default handler (Windows Photos, etc.). rundll32
	// FileProtocolHandler launches it detached with no console flash.
	if err := exec.Command("rundll32", "url.dll,FileProtocolHandler", path).Start(); err != nil {
		return fmt.Errorf("이미지 뷰어 실행 실패: %w", err)
	}
	return nil
}
