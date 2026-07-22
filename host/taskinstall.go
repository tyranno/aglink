package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

// aglink auto-start is a per-user logon Scheduled Task (not a Windows service):
// aglink drives the interactive desktop (screen/web control) and needs the user
// session + a High integrity token, which a Session-0 service can't provide
// without CreateProcessAsUser plumbing. A logon task with RunLevel
// HighestAvailable runs elevated in the user session with no UAC prompt — exactly
// the environment aglink needs, started automatically at login.
const taskName = "aglink"

// taskXML builds a Task Scheduler 1.2 definition: at-logon trigger for userID,
// highest privileges (silent elevation), no execution time limit (a gateway runs
// for days), and restart-on-failure. command runs from workDir.
func taskXML(userID, command, workDir string) string {
	esc := func(s string) string {
		s = strings.ReplaceAll(s, "&", "&amp;")
		s = strings.ReplaceAll(s, "<", "&lt;")
		s = strings.ReplaceAll(s, ">", "&gt;")
		return s
	}
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>aglink AI 게이트웨이 — 로그온 시 자동 시작 (관리자 권한, 화면제어용 유저 세션)</Description>
    <URI>\` + esc(taskName) + `</URI>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>` + esc(userID) + `</UserId>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>` + esc(userID) + `</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>HighestAvailable</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>3</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + esc(command) + `</Command>
      <Arguments>run</Arguments>
      <WorkingDirectory>` + esc(workDir) + `</WorkingDirectory>
    </Exec>
  </Actions>
</Task>`
}

// utf16LE encodes s as UTF-16 little-endian with a leading BOM, the byte format
// schtasks /xml expects.
func utf16LE(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 0, 2+len(u)*2)
	b = append(b, 0xFF, 0xFE) // little-endian BOM
	for _, r := range u {
		b = append(b, byte(r), byte(r>>8))
	}
	return b
}

// installTask registers the logon auto-start task pointing at THIS executable, so
// running `aglink install` from wherever aglink lives (dev tree or the installer's
// target dir) wires auto-start to that same copy. Requires administrator rights
// (schtasks registering a HighestAvailable task).
func installTask() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("실행 파일 경로 확인 실패: %w", err)
	}
	u, err := user.Current()
	if err != nil {
		return fmt.Errorf("현재 사용자 확인 실패: %w", err)
	}
	xml := taskXML(u.Username, exe, filepath.Dir(exe))

	tmp, err := os.CreateTemp("", "aglink-task-*.xml")
	if err != nil {
		return fmt.Errorf("임시 XML 생성 실패: %w", err)
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)
	// schtasks /xml requires UTF-16LE with a BOM — a UTF-8 file fails with
	// "인코딩을 전환할 수 없습니다". Encode with the stdlib (no x/text dependency).
	if err := os.WriteFile(tmpPath, utf16LE(xml), 0600); err != nil {
		return fmt.Errorf("XML 쓰기 실패: %w", err)
	}

	// /f overwrites an existing task so re-install (e.g. installer upgrade) is idempotent.
	out, err := exec.Command("schtasks", "/create", "/tn", taskName, "/xml", tmpPath, "/f").CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks 등록 실패: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	fmt.Printf("자동시작 작업 '%s' 등록 완료 (로그온 시 관리자 권한으로 실행).\n", taskName)
	fmt.Printf("  대상: %s run\n", exe)
	fmt.Printf("  사용자: %s\n", u.Username)
	fmt.Println("지금 바로 시작하려면: aglink start   (또는 다음 로그온 때 자동 시작)")
	return nil
}

// uninstallTask removes the auto-start task.
func uninstallTask() error {
	out, err := exec.Command("schtasks", "/delete", "/tn", taskName, "/f").CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks 삭제 실패: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	fmt.Printf("자동시작 작업 '%s' 삭제 완료.\n", taskName)
	return nil
}

// startTask runs the registered task now (so install → immediate start, no logout).
func startTask() error {
	out, err := exec.Command("schtasks", "/run", "/tn", taskName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks 실행 실패: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	fmt.Printf("자동시작 작업 '%s' 실행됨.\n", taskName)
	return nil
}

// stopTask ends the running task instance.
func stopTask() error {
	out, err := exec.Command("schtasks", "/end", "/tn", taskName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks 중지 실패: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	fmt.Printf("자동시작 작업 '%s' 중지됨.\n", taskName)
	return nil
}

// taskStatus prints the task's current state (or a clear "not installed" note).
func taskStatus() error {
	out, err := exec.Command("schtasks", "/query", "/tn", taskName, "/fo", "LIST").CombinedOutput()
	if err != nil {
		log.Printf("자동시작 작업 '%s' 미등록(또는 조회 실패).", taskName)
		fmt.Println(strings.TrimSpace(string(out)))
		return nil
	}
	fmt.Println(strings.TrimSpace(string(out)))
	return nil
}
