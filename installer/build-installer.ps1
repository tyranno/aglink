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

Write-Host "[1/3] building host (GOWORK=off)..."
$env:GOWORK = "off"
Push-Location (Join-Path $root "host")
& go build -o (Join-Path $stage "aglink.exe") .
if ($LASTEXITCODE -ne 0) { Pop-Location; throw "host build failed" }
Pop-Location

Write-Host "[2/3] staging binaries (flattened)..."
$copies = @{
  "chat\aglink-chat.exe"           = "aglink-chat.exe"
  "desktop\bin\aglink-desktop.exe" = "aglink-desktop.exe"
  "screen\aglink-screen.exe"       = "aglink-screen.exe"
  "web\aglink-web.exe"             = "aglink-web.exe"
}
foreach ($src in $copies.Keys) {
  $s = Join-Path $root $src
  if (-not (Test-Path $s)) { throw "missing binary: $s" }
  Copy-Item $s (Join-Path $stage $copies[$src]) -Force
  Write-Host ("  staged " + $copies[$src])
}

Write-Host "[3/3] makensis..."
$makensis = "C:\Program Files (x86)\NSIS\makensis.exe"
if (-not (Test-Path $makensis)) { throw "NSIS not found at $makensis" }
& $makensis /INPUTCHARSET UTF8 (Join-Path $here "aglink-setup.nsi")
if ($LASTEXITCODE -ne 0) { throw "makensis failed" }

$out = Join-Path $root "aglink-Setup.exe"
if (Test-Path $out) {
  $mb = [math]::Round((Get-Item $out).Length / 1MB, 1)
  Write-Host ("DONE: " + $out + " (" + $mb + " MB)")
} else {
  throw "aglink-Setup.exe not produced"
}
