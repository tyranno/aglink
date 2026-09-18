<#
.SYNOPSIS
  Register the scheduled task that keeps the aglink host (Telegram bot) alive.
  MUST be run from an elevated PowerShell.

.DESCRIPTION
  Separate from the aglink-web / aglink-screen task on purpose.

  The host re-launches itself as administrator when screen_control.elevated is
  set. Started from an unelevated task that means a UAC prompt every time it has
  to be revived — and an unattended revival would then sit on that prompt
  forever, which is exactly the case the task exists to cover. So this task runs
  at RunLevel Highest and the host is already elevated when it starts.

  Registering a Highest-level task itself requires administrator rights, which
  is why this is a separate script you run once rather than something the
  always-on script can set up for itself.

  Nothing here is host- or account-specific: the host task talks to no remote
  machine, so this script hardcodes no addresses (public repo).

.EXAMPLE
  # In an ADMIN PowerShell:
  powershell -NoProfile -ExecutionPolicy Bypass -File scripts\install-host-task.ps1
#>
[CmdletBinding()]
param(
    [string] $TaskName = 'aglink host always-on',

    # How often to check. The host either runs or it does not; a check costs one
    # process enumeration, so this can be frequent without being a burden.
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
$inner  = "powershell.exe -NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File `"`"$always`"`" -Host_"
$action = New-ScheduledTaskAction -Execute 'wscript.exe' -Argument "//B //Nologo `"$vbs`" `"$inner`""

$user    = "$env:USERDOMAIN\$env:USERNAME"
$trigger = @(
    New-ScheduledTaskTrigger -AtLogOn -User $user
    New-ScheduledTaskTrigger -Once -At (Get-Date).AddMinutes($IntervalMinutes) `
        -RepetitionInterval (New-TimeSpan -Minutes $IntervalMinutes)
)

# Interactive logon type, not "run whether logged on or not": the host drives an
# interactive Claude session and, with screen control enabled, needs a desktop.
$settings  = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
    -StartWhenAvailable -MultipleInstances IgnoreNew -ExecutionTimeLimit (New-TimeSpan -Minutes 10)
$principalSpec = New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive -RunLevel Highest

Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger `
    -Settings $settings -Principal $principalSpec `
    -Description 'Keep the aglink host (Telegram bot) alive. Runs elevated so the host''s own UAC self-elevation never prompts.' `
    -Force | Out-Null

Get-ScheduledTask -TaskName $TaskName | Select-Object TaskName, State
Write-Host "registered: $TaskName (every $IntervalMinutes min, at logon, elevated)"
