# aglink launcher
# aglink.exe가 exit code 42로 종료하면 aglink_new.exe로 교체 후 재시작.
# 사용법: .\launcher.ps1

$dir = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $dir

Write-Host "[launcher] aglink 시작 ($dir)"

# 이름 변경(teleclaude → aglink) 컷오버: 예전 이름의 exe만 있으면 새 이름으로 옮긴다.
# 이 한 번만 처리하면 되고, 이후에는 aglink.exe만 존재한다.
if (-not (Test-Path "aglink.exe") -and (Test-Path "teleclaude.exe")) {
    Write-Host "[launcher] 이전 이름 감지 → teleclaude.exe를 aglink.exe로 이동"
    Move-Item -Force "teleclaude.exe" "aglink.exe"
}

# 기존에 실행 중인 다른 인스턴스 정리 (컷오버 중이면 옛 이름도 함께)
$existing = Get-Process aglink, teleclaude -ErrorAction SilentlyContinue |
    Where-Object { $_.Id -ne $PID }
if ($existing) {
    Write-Host "[launcher] 기존 인스턴스 종료 중... ($($existing.Count)개)"
    $existing | Stop-Process -Force
    Start-Sleep -Milliseconds 800
}

while ($true) {
    & ".\aglink.exe"
    $code = $LASTEXITCODE

    if ($code -eq 42) {
        Write-Host "[launcher] 업데이트 감지 (exit 42) → 교체 중..."
        if (Test-Path "aglink_new.exe") {
            Move-Item -Force "aglink_new.exe" "aglink.exe"
            Write-Host "[launcher] 교체 완료 → 재시작"
        } else {
            Write-Host "[launcher] aglink_new.exe 없음 → 그냥 재시작"
        }
    } else {
        Write-Host "[launcher] 종료 (exit $code) → 루프 탈출"
        break
    }
}
