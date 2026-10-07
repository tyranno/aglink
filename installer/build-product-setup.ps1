<#
.SYNOPSIS
  Build per-product setup programs: aglink-web-Setup.exe, aglink-screen-Setup.exe,
  or the full aglink-Setup.exe.

.DESCRIPTION
  Each product can be released on its own. The setups for web and screen are
  per-user (no admin) and register themselves with Claude Code; see common.nsh.
  "aglink" delegates to the existing build-installer.ps1 (the full bundle with
  the Telegram host).

  Files are staged in %TEMP% rather than in the repo, so a build leaves nothing
  for git to pick up. The setups land in the repo root (*.exe is gitignored),
  and are also gathered with the AI install guide into dist\ (gitignored) —
  that folder is what to hand out.

  Messages are ASCII-only so Windows PowerShell 5.1 parses this file whatever
  the console codepage.

.EXAMPLE
  powershell -NoProfile -ExecutionPolicy Bypass -File installer\build-product-setup.ps1 -Product web
  powershell -NoProfile -ExecutionPolicy Bypass -File installer\build-product-setup.ps1 -Product team   # web + screen -> dist\
  powershell -NoProfile -ExecutionPolicy Bypass -File installer\build-product-setup.ps1 -Product all
#>
[CmdletBinding()]
param(
    [ValidateSet('web', 'screen', 'team', 'aglink', 'all')]
    [string] $Product = 'all'
)

$ErrorActionPreference = 'Stop'
$here = $PSScriptRoot
$root = Split-Path -Parent $here
$makensis = 'C:\Program Files (x86)\NSIS\makensis.exe'
if (-not (Test-Path $makensis)) { throw "NSIS not found at $makensis" }

# Version: 1.0.<commit count>. The commit count only grows, so a later build
# always installs over an earlier one as an upgrade.
Push-Location $root
$count = ((& git rev-list --count HEAD) | Out-String).Trim()
$hash = ((& git rev-parse --short HEAD) | Out-String).Trim()
Pop-Location
$version = "1.0.$count"
Write-Host "version $version ($hash)"

function Build-Go([string] $dir, [string] $out) {
    Push-Location (Join-Path $root $dir)
    try {
        $env:GOWORK = 'off'
        $env:GOFLAGS = '-mod=readonly'
        & go build -o $out .
        if ($LASTEXITCODE -ne 0) { throw "go build failed in $dir" }
    } finally {
        Remove-Item Env:GOWORK -ErrorAction SilentlyContinue
        Remove-Item Env:GOFLAGS -ErrorAction SilentlyContinue
        Pop-Location
    }
}

function New-Stage([string] $name) {
    $stage = Join-Path $env:TEMP "aglink-setup-stage-$name"
    if (Test-Path $stage) { Remove-Item $stage -Recurse -Force }
    New-Item -ItemType Directory -Path $stage | Out-Null
    return $stage
}

function Invoke-Makensis([string] $nsi, [string] $stage, [string] $outFile) {
    & $makensis /INPUTCHARSET UTF8 "/DVERSION=$version" "/DSTAGE=$stage" "/DOUTFILE=$outFile" (Join-Path $here $nsi)
    if ($LASTEXITCODE -ne 0) { throw "makensis failed for $nsi" }
    $mb = [math]::Round((Get-Item $outFile).Length / 1MB, 1)
    Write-Host "DONE: $outFile ($mb MB)"
}

function Build-Web {
    Write-Host '== aglink-web =='
    $stage = New-Stage 'web'
    Build-Go 'web' (Join-Path $stage 'aglink-web.exe')

    # Chrome extension, minus its tests.
    $ext = Join-Path $stage 'chrome-extension'
    New-Item -ItemType Directory -Path $ext | Out-Null
    Get-ChildItem (Join-Path $root 'web\extension') -File |
        Where-Object { $_.Name -notlike '*.test.js' } |
        Copy-Item -Destination $ext

    # VS Code extension, packaged by its own script.
    & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $root 'vscode\install.ps1') -PackageOnly
    if ($LASTEXITCODE -ne 0) { throw 'vsix packaging failed' }
    $vsix = Get-ChildItem (Join-Path $root 'vscode') -Filter 'aglink-vscode-*.vsix' | Sort-Object LastWriteTime -Descending | Select-Object -First 1
    Copy-Item $vsix.FullName (Join-Path $stage 'aglink-vscode.vsix')

    Copy-Item (Join-Path $here 'start-web-daemon.vbs') $stage
    Copy-Item (Join-Path $here 'web-guide.html') (Join-Path $stage 'guide.html')

    Invoke-Makensis 'aglink-web-setup.nsi' $stage (Join-Path $root 'aglink-web-Setup.exe')
}

function Build-Screen {
    Write-Host '== aglink-screen =='
    $stage = New-Stage 'screen'
    Build-Go 'screen' (Join-Path $stage 'aglink-screen.exe')
    Copy-Item (Join-Path $here 'screen-guide.html') (Join-Path $stage 'guide.html')
    Invoke-Makensis 'aglink-screen-setup.nsi' $stage (Join-Path $root 'aglink-screen-Setup.exe')
}

function Build-Aglink {
    Write-Host '== aglink (full bundle) =='
    & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $here 'build-installer.ps1')
    if ($LASTEXITCODE -ne 0) { throw 'build-installer.ps1 failed' }
}

switch ($Product) {
    'web'    { Build-Web }
    'screen' { Build-Screen }
    'team'   { Build-Web; Build-Screen }
    'aglink' { Build-Aglink }
    'all'    { Build-Web; Build-Screen; Build-Aglink }
}

# dist\ is what gets handed to a teammate: the per-user setups plus README.md,
# the install guide written for the teammate's AI agent (AI-INSTALL.md). Each
# build replaces dist\ with only what this build made, so a setup left over from
# an older build is never handed out next to a newer one.
if ($Product -ne 'aglink') {
    $dist = Join-Path $root 'dist'
    if (Test-Path $dist) { Remove-Item $dist -Recurse -Force }
    New-Item -ItemType Directory -Path $dist | Out-Null
    $both = @('aglink-web-Setup.exe', 'aglink-screen-Setup.exe')
    $built = @{ web = @('aglink-web-Setup.exe'); screen = @('aglink-screen-Setup.exe'); team = $both; all = $both }[$Product]
    foreach ($name in $built) { Copy-Item (Join-Path $root $name) $dist -Force }
    Copy-Item (Join-Path $here 'AI-INSTALL.md') (Join-Path $dist 'README.md') -Force
    Set-Content -Path (Join-Path $dist 'VERSION.txt') -Value "$version ($hash)" -Encoding ASCII
    Write-Host "dist: $dist"
}
