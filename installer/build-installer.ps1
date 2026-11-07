# Builds aglink-Setup.exe:
#  1. compiles the host fresh (bundled aglink.exe gets install/uninstall)
#  2. stages all 5 binaries flattened into installer\stage
#  3. runs makensis on aglink-setup.nsi (UTF-8 source)
# ASCII-only messages so Windows PowerShell 5.1 parses this file regardless of codepage.
$ErrorActionPreference = "Stop"
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$root = Split-Path -Parent $here
$stage = Join-Path $here "stage"
if (-not (Test-Path $stage)) { New-Item -ItemType Directory -Path $stage | Out-Null }

Write-Host "[1/3] building host (GOWORK=off, version-stamped)..."
$env:GOWORK = "off"
Push-Location (Join-Path $root "host")
$count = ((& git rev-list --count HEAD) | Out-String).Trim()
$hash = ((& git rev-parse --short HEAD) | Out-String).Trim()
$time = [DateTime]::UtcNow.ToString("yyyy-MM-ddTHH:mm:ssZ")
$ld = "-X main.buildCommitCount=$count -X main.buildCommit=$hash -X main.buildTime=$time"
Write-Host ("  version: count=$count commit=$hash")
& go build -ldflags $ld -o (Join-Path $stage "aglink.exe") .
if ($LASTEXITCODE -ne 0) { Pop-Location; throw "host build failed" }
Pop-Location

Write-Host "[2/3] building helper binaries into stage..."
function Build-GoHelper($relDir, $outName, $ldflags = "") {
  Push-Location (Join-Path $root $relDir)
  $out = Join-Path $stage $outName
  if ($ldflags -ne "") {
    & go build -ldflags $ldflags -o $out .
  } else {
    & go build -o $out .
  }
  if ($LASTEXITCODE -ne 0) { Pop-Location; throw "$outName build failed" }
  Pop-Location
  Write-Host ("  built " + $outName)
}

$env:GOWORK = "off"
Build-GoHelper "chat" "aglink-chat.exe"
Build-GoHelper "screen" "aglink-screen.exe"
Build-GoHelper "web" "aglink-web.exe"
Build-GoHelper "desktop" "aglink-desktop.exe" "-H windowsgui -s -w"

# AI setup guide + the Chrome extension aglink-web needs (minus its tests).
Copy-Item (Join-Path $here "AI-SETUP.md") $stage -Force
$ext = Join-Path $stage "chrome-extension"
if (Test-Path $ext) { Remove-Item $ext -Recurse -Force }
New-Item -ItemType Directory -Path $ext | Out-Null
Get-ChildItem (Join-Path $root "web\extension") -File |
  Where-Object { $_.Name -notlike "*.test.js" } |
  Copy-Item -Destination $ext

Write-Host "[3/3] makensis..."
$makensis = @((Join-Path $env:USERPROFILE '.aglink\tools\nsis-3.11\makensis.exe'),'C:\Program Files (x86)\NSIS\makensis.exe') | Where-Object { (Test-Path $_) -and ((& $_ -VERSION) -match '^v3') } | Select-Object -First 1
if (-not $makensis) { throw "NSIS 3 (makensis.exe) not found" }
& $makensis /INPUTCHARSET UTF8 (Join-Path $here "aglink-setup.nsi")
if ($LASTEXITCODE -ne 0) { throw "makensis failed" }

$out = Join-Path $root "aglink-Setup.exe"
if (Test-Path $out) {
  $mb = [math]::Round((Get-Item $out).Length / 1MB, 1)
  Write-Host ("DONE: " + $out + " (" + $mb + " MB)")
} else {
  throw "aglink-Setup.exe not produced"
}
