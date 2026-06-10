# teleclaude Windows 작업 스케줄러 등록 스크립트
# 관리자 권한 불필요 (현재 사용자 세션에 등록)
# 사용법: .\scripts\install-windows-task.ps1 [-BinaryPath <경로>] [-Uninstall]
#
# 특징:
#   - 로그온 시 자동 시작 (Task Scheduler, 현재 사용자)
#   - launcher.ps1 을 통해 hot-swap 업데이트 지원 (!update 명령)
#   - 관리자 권한 불필요

param(
    [string]$BinaryDir = (Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)),
    [switch]$Uninstall
)

$TaskName = "Teleclaude"
$LauncherPath = Join-Path $BinaryDir "launcher.ps1"
$BinaryPath = Join-Path $BinaryDir "teleclaude.exe"

if ($Uninstall) {
    if (Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue) {
        Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
        Write-Host "✅ 작업 스케줄러에서 $TaskName 제거됨"
    } else {
        Write-Host "ℹ  $TaskName 작업이 등록되지 않았습니다"
    }
    exit 0
}

# 사전 검사
if (-not (Test-Path $BinaryPath)) {
    Write-Error "teleclaude.exe 를 찾을 수 없습니다: $BinaryPath"
    Write-Host "  먼저 빌드하세요: go build -o teleclaude.exe ."
    exit 1
}

$ConfigPath = Join-Path $env:USERPROFILE ".teleclaude\config.txt"
if (-not (Test-Path $ConfigPath)) {
    Write-Warning "설정 파일이 없습니다: $ConfigPath"
    Write-Host "  서비스 등록 전에 설정 마법사를 실행하세요:"
    Write-Host "    .\teleclaude.exe run"
}

# 기존 작업 제거
if (Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue) {
    Write-Host "▶ 기존 작업 제거 중..."
    Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
}

# launcher.ps1 이 있으면 그것을 사용, 없으면 직접 실행
if (Test-Path $LauncherPath) {
    $Action = New-ScheduledTaskAction `
        -Execute "powershell.exe" `
        -Argument "-NonInteractive -WindowStyle Hidden -File `"$LauncherPath`"" `
        -WorkingDirectory $BinaryDir
    Write-Host "▶ launcher.ps1 을 통해 실행 (hot-swap 업데이트 지원)"
} else {
    $Action = New-ScheduledTaskAction `
        -Execute $BinaryPath `
        -Argument "run" `
        -WorkingDirectory $BinaryDir
    Write-Host "▶ teleclaude.exe 직접 실행"
}

# 로그온 시 트리거
$Trigger = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME

# 실행 설정: 숨김 창, 최대 지속 시간 없음
$Settings = New-ScheduledTaskSettingsSet `
    -ExecutionTimeLimit ([TimeSpan]::Zero) `
    -MultipleInstances IgnoreNew `
    -RestartCount 3 `
    -RestartInterval (New-TimeSpan -Minutes 1)

$Principal = New-ScheduledTaskPrincipal `
    -UserId $env:USERNAME `
    -LogonType Interactive `
    -RunLevel Limited

Register-ScheduledTask `
    -TaskName $TaskName `
    -Action $Action `
    -Trigger $Trigger `
    -Settings $Settings `
    -Principal $Principal `
    -Description "Teleclaude - Telegram Claude Agent (자동 시작)" | Out-Null

Write-Host "✅ 작업 스케줄러 등록 완료: $TaskName"
Write-Host ""
Write-Host "  지금 시작:   Start-ScheduledTask -TaskName '$TaskName'"
Write-Host "  중단:        Stop-ScheduledTask  -TaskName '$TaskName'"
Write-Host "  제거:        .\scripts\install-windows-task.ps1 -Uninstall"
Write-Host ""

$start = Read-Host "지금 바로 시작할까요? [Y/n]"
if ($start -eq "" -or $start.ToLower() -eq "y") {
    Start-ScheduledTask -TaskName $TaskName
    Start-Sleep -Seconds 2
    $state = (Get-ScheduledTask -TaskName $TaskName).State
    Write-Host "▶ 상태: $state"
}
