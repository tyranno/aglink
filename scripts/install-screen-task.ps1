<#
.SYNOPSIS
  Register the scheduled task that keeps the aglink-screen daemon alive AND
  ELEVATED. MUST be run from an elevated PowerShell.

.DESCRIPTION
  Separate from the aglink-web / tunnel task on purpose, and a sibling of the
  host task (install-host-task.ps1).

  aglink-screen is run at RunLevel Highest so the daemon itself is elevated.
  launch_app's child processes then inherit administrator rights with no UAC
  prompt, which is what lets an unattended session run installers and other
  admin programs while nobody is at the keyboard. Registering a Highest-level
  task is admin-only, so this is a once-off script rather than something the
  always-on script sets up for itself.

  SECURITY: an elevated daemon on 127.0.0.1:48220 means anything that can reach
  that port — another local program, or a remote account over the SSH reverse
  tunnel — can have this PC run programs as administrator. That is close to
  turning this account's UAC off. Install this only on a machine where that
  trade is acceptable. To undo: delete the task and let aglink-screen run
  unelevated again (the aglink-web task no longer starts it, so remove-only).

  The aglink-web / tunnel task must be reinstalled WITHOUT starting the screen
  daemon; current aglink-always-on.ps1 already omits it from the server path, so
  only this task starts aglink-screen.

.EXAMPLE
  # In an ADMIN PowerShell:
  powershell -NoProfile -ExecutionPolicy Bypass -File scripts\install-screen-task.ps1
#>
[CmdletBinding()]
param(
    [string] $TaskName = 'aglink screen always-on',

    # How often to check. A down daemon fails /health; reviving it costs one
    # process enumeration plus a launch, so a few minutes is fine.
    [int] $IntervalMinutes = 5
)

$ErrorActionPreference = 'Stop'

$identity  = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Write-Error "This must run from an elevated PowerShell (Run as administrator). Registering a RunLevel=Highest task is an admin-only operation."
    exit 1
}

$scriptDir = $PSScriptRoot
$vbs       = Join-Path $scriptDir 'run-hidden.vbs'
$always    = Join-Path $scriptDir 'aglink-always-on.ps1'

foreach ($p in @($vbs, $always)) {
    if (-not (Test-Path $p)) { Write-Error "missing: $p"; exit 1 }
}

# The inner command line is one argument to the shim, so its own quotes are
# doubled. run-hidden.vbs keeps Task Scheduler from flashing a console window
# every interval.
$inner  = "powershell.exe -NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File `"`"$always`"`" -Screen_"
$action = New-ScheduledTaskAction -Execute 'wscript.exe' -Argument "//B //Nologo `"$vbs`" `"$inner`""

$user    = "$env:USERDOMAIN\$env:USERNAME"
$trigger = @(
    New-ScheduledTaskTrigger -AtLogOn -User $user
    New-ScheduledTaskTrigger -Once -At (Get-Date).AddMinutes($IntervalMinutes) `
        -RepetitionInterval (New-TimeSpan -Minutes $IntervalMinutes)
)

# Interactive logon type, not "run whether logged on or not": aglink-screen
# synthesizes input and captures the screen, which a session-0 service cannot do.
$settings  = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
    -StartWhenAvailable -MultipleInstances IgnoreNew -ExecutionTimeLimit (New-TimeSpan -Minutes 10)
$principalSpec = New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive -RunLevel Highest

Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger `
    -Settings $settings -Principal $principalSpec `
    -Description 'Keep the aglink-screen daemon alive and elevated so launch_app children run as admin without a UAC prompt.' `
    -Force | Out-Null

Get-ScheduledTask -TaskName $TaskName | Select-Object TaskName, State
Write-Host "registered: $TaskName (every $IntervalMinutes min, at logon, elevated)"
Write-Host "note: start it now with an elevated 'aglink-screen serve' or log off/on; the aglink-web task no longer starts the screen daemon."
