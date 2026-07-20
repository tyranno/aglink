# Rebuilds an aglink-* plugin from its sibling source repo and hot-swaps the
# deployed binary next to aglink.exe, without touching aglink itself.
#
# Why this exists: during local dev, redeploying a plugin after an edit was a
# manual 3-step dance (kill process -> copy exe -> eyeball whether it came back)
# repeated by hand many times in a single session. This script does all three
# and reports the result, so an agent (or a human) can redeploy in one call
# instead of composing PowerShell each time.
#
# Usage: .\scripts\redeploy-plugin.ps1 -Name aglink-chat
#        .\scripts\redeploy-plugin.ps1 -Name aglink-screen
#        .\scripts\redeploy-plugin.ps1 -Name aglink-web
#
# Assumes the standard sibling-directory layout:
#   88.MyProject/
#     Aglink/        <- this repo (aglink.exe lives here)
#     aglink-chat/       <- plugin source (built here, then copied)
#     aglink-screen/
#     aglink-web/

param(
    [Parameter(Mandatory = $true)]
    [ValidateSet("aglink-chat", "aglink-screen", "aglink-web")]
    [string]$Name
)

$ErrorActionPreference = "Stop"

$aglinkDir = Split-Path -Parent $PSScriptRoot
$parentDir = Split-Path -Parent $aglinkDir
$srcDir = Join-Path $parentDir $Name
$exeName = "$Name.exe"
$srcExe = Join-Path $srcDir $exeName
$deployExe = Join-Path $aglinkDir $exeName

if (-not (Test-Path $srcDir)) {
    Write-Error "[redeploy] source repo not found: $srcDir (expected as a sibling of $aglinkDir)"
    exit 1
}

Write-Host "[redeploy] building $Name from $srcDir ..."
Push-Location $srcDir
try {
    go build -o $exeName .
    if ($LASTEXITCODE -ne 0) {
        Write-Error "[redeploy] go build failed (exit $LASTEXITCODE)"
        exit 1
    }
}
finally {
    Pop-Location
}

$before = Get-Process -Name $Name -ErrorAction SilentlyContinue
if ($before) {
    Write-Host "[redeploy] stopping running $Name (PID $($before.Id), started $($before.StartTime)) ..."
    Stop-Process -Id $before.Id -Force
    Start-Sleep -Milliseconds 500
}

Copy-Item -Path $srcExe -Destination $deployExe -Force
Write-Host "[redeploy] deployed -> $deployExe"

if ($Name -eq "aglink-chat") {
    # aglink-chat is supervised by aglink (aglinkchat_supervisor.go) and
    # respawns on its own, but not instantly — the supervisor's backoff loop and
    # the new process's own startup (control-API connect, browser listener) take
    # a few seconds. Poll instead of a single fixed sleep so a slow respawn
    # doesn't produce a false "did not respawn" warning.
    $after = $null
    for ($i = 0; $i -lt 10; $i++) {
        Start-Sleep -Milliseconds 500
        $after = Get-Process -Name $Name -ErrorAction SilentlyContinue
        if ($after) { break }
    }
    if ($after) {
        Write-Host "[redeploy] OK — $Name respawned: PID $($after.Id), started $($after.StartTime)"
    }
    else {
        Write-Warning "[redeploy] $Name did not respawn within 5s — is aglink running, and is aglink_chat.enabled: true in config.yaml?"
    }
}
else {
    # aglink-screen / aglink-web are spawned fresh per MCP tool call / per
    # worker, not supervised — there's nothing to wait for. Killing the old
    # process (above) just makes sure the next call can't hit a stale binary
    # still holding the file (or, for aglink-web, the persistent serve daemon).
    Write-Host "[redeploy] OK — $Name is spawned per call, not supervised; the next tool call will use the new build."
}
