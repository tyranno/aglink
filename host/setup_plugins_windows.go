//go:build windows

package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// aglinkRepoURL is the public HTTPS clone URL for an aglink-* plugin repo.
// HTTPS (not the origin SSH remote aglink's own repo uses) so a fresh
// machine with no SSH key configured can still clone it.
func aglinkRepoURL(name string) string {
	return "https://github.com/tyranno/" + name + ".git"
}

// ensureAglinkPlugins offers to clone+build any aglink-* sibling repo
// (screen/browser/web-chat control — Windows-only, hence this file) that
// isn't already checked out next to aglink's own source, mirroring
// updatePlugins' srcDir/parent layout so the result is auto-discovered the
// same way !update's rebuilds are. git/go absence just skips this silently:
// it is a convenience for a from-source setup, never a hard requirement.
func ensureAglinkPlugins(in *bufio.Reader, srcDir string) {
	if _, err := exec.LookPath("git"); err != nil {
		return
	}
	if _, err := exec.LookPath("go"); err != nil {
		return
	}
	parent := filepath.Dir(srcDir)
	anyMissing := false
	for _, name := range pluginNames {
		if _, statErr := os.Stat(filepath.Join(parent, name)); statErr != nil {
			anyMissing = true
			break
		}
	}
	if !anyMissing {
		return
	}

	fmt.Println("\n[추가] aglink 보조 기능 (선택 — 화면 제어 / 브라우저 제어 / 웹 채팅)")
	installedWeb := false
	for _, name := range pluginNames {
		pluginDir := filepath.Join(parent, name)
		if _, statErr := os.Stat(pluginDir); statErr == nil {
			continue // already checked out
		}
		ok, err := confirm(in, fmt.Sprintf("   %s가 없습니다. 지금 설치할까요? (git clone + 빌드) [Y/n]: ", name))
		if err != nil || !ok {
			continue
		}
		if installPlugin(name, pluginDir, srcDir) && name == "aglink-web" {
			installedWeb = true
		}
	}
	if installedWeb {
		guideAglinkWebExtension(parent)
	}
}

// installPlugin clones then builds a single aglink-* plugin, dropping the
// binary into srcDir under name+exeSuffix (the layout resolveScreenBinaryPath/
// resolveWebBinaryPath auto-discover). Returns whether it fully succeeded.
func installPlugin(name, pluginDir, srcDir string) bool {
	fmt.Printf("   %s 내려받는 중...\n", name)
	if err := clonePlugin(name, pluginDir); err != nil {
		fmt.Printf("   ⚠️ %s clone 실패: %v\n", name, err)
		return false
	}
	fmt.Printf("   %s 빌드 중...\n", name)
	if err := buildPlugin(pluginDir, filepath.Join(srcDir, name+exeSuffix)); err != nil {
		fmt.Printf("   ⚠️ %s 빌드 실패: %v\n", name, err)
		return false
	}
	fmt.Printf("   ✅ %s 설치 완료\n", name)
	return true
}

// clonePlugin does a shallow clone of name's public repo into pluginDir.
func clonePlugin(name, pluginDir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", aglinkRepoURL(name), pluginDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, string(out))
	}
	return nil
}

// buildPlugin runs `go build` for the module in pluginDir, writing the
// binary to target. Split out from installPlugin so it can be unit tested
// against a local fixture module without a real network clone.
func buildPlugin(pluginDir, target string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", target, ".")
	cmd.Dir = pluginDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, string(out))
	}
	return nil
}

// guideAglinkWebExtension opens Chrome at chrome://extensions and prints the
// remaining manual steps. Chrome's security model has no silent-install path
// for an unpacked (developer-mode) extension — the "Load unpacked" click is
// unavoidable — so this gets the user to the right screen instead of making
// them find it themselves, rather than pretending it can be fully automated.
func guideAglinkWebExtension(parent string) {
	extDir := filepath.Join(parent, "aglink-web", "extension")
	fmt.Println("\n   aglink-web은 크롬 확장이 있어야 브라우저를 제어할 수 있습니다.")
	fmt.Println("   Chrome을 열어드립니다 — 아래 3단계만 눈으로 따라 해 주세요")
	fmt.Println("   (압축해제된 확장은 크롬 보안 정책상 클릭 없는 완전 자동 설치가 불가능합니다):")
	fmt.Println("     1) 우측 상단 '개발자 모드' 켜기")
	fmt.Println("     2) '압축해제된 확장 프로그램을 로드합니다' 클릭")
	fmt.Printf("     3) 폴더 선택 창에서 이 경로 붙여넣기: %s\n", extDir)
	_ = exec.Command("cmd", "/c", "start", "chrome", "chrome://extensions").Start()
}
