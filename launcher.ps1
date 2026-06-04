# teleclaude launcher
# teleclaude.exe가 exit code 42로 종료하면 teleclaude_new.exe로 교체 후 재시작.
# 사용법: .\launcher.ps1

$dir = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $dir

Write-Host "[launcher] teleclaude 시작 ($dir)"

while ($true) {
    & ".\teleclaude.exe"
    $code = $LASTEXITCODE

    if ($code -eq 42) {
        Write-Host "[launcher] 업데이트 감지 (exit 42) → 교체 중..."
        if (Test-Path "teleclaude_new.exe") {
            Move-Item -Force "teleclaude_new.exe" "teleclaude.exe"
            Write-Host "[launcher] 교체 완료 → 재시작"
        } else {
            Write-Host "[launcher] teleclaude_new.exe 없음 → 그냥 재시작"
        }
    } else {
        Write-Host "[launcher] 종료 (exit $code) → 루프 탈출"
        break
    }
}
