$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8

function Out-Json($o) {
  $j = ConvertTo-Json -InputObject $o -Depth 6 -Compress
  $j = [regex]::Replace($j, '[^\x00-\x7F]', { param($m) '\u{0:x4}' -f [int][char]$m.Value })
  [Console]::Out.WriteLine($j)
  [Console]::Out.Flush()
}

# One-time setup: the WinRT type load and the AsTask reflection helper are
# what cost the ~0.5s this server exists to pay only once instead of on every
# OCR call — see screen_ocr_windows.go's startOCRServer doc comment.
Add-Type -AssemblyName System.Runtime.WindowsRuntime
$null = [Windows.Media.Ocr.OcrEngine, Windows.Foundation, ContentType = WindowsRuntime]
$null = [Windows.Globalization.Language, Windows.Foundation, ContentType = WindowsRuntime]
$null = [Windows.Graphics.Imaging.BitmapDecoder, Windows.Foundation, ContentType = WindowsRuntime]
$null = [Windows.Storage.StorageFile, Windows.Foundation, ContentType = WindowsRuntime]
$asTask = [System.WindowsRuntimeSystemExtensions].GetMethods() | Where-Object {
  $_.Name -eq 'AsTask' -and $_.GetParameters().Count -eq 1 -and
  $_.GetParameters()[0].ParameterType.Name -eq 'IAsyncOperation`1' } | Select-Object -First 1
function Await($op, [Type]$t) {
  $task = $asTask.MakeGenericMethod($t).Invoke($null, @($op))
  $null = $task.Wait(-1)
  $task.Result
}

$avail = @([Windows.Media.Ocr.OcrEngine]::AvailableRecognizerLanguages | ForEach-Object { $_.LanguageTag })

# Engines are also cached across requests, keyed by the requested lang tag
# ("" = auto) — switching back and forth between two languages across calls
# costs nothing after each has been created once.
$engines = @{}
function Get-Engine([string]$wantLang) {
  if ($engines.ContainsKey($wantLang)) { return $engines[$wantLang] }
  $engine = $null
  if ($wantLang -ne '') {
    $tag = $avail | Where-Object { $_ -eq $wantLang -or $_ -like "$wantLang-*" -or $wantLang -like "$_-*" } | Select-Object -First 1
    if ($tag) { $engine = [Windows.Media.Ocr.OcrEngine]::TryCreateFromLanguage((New-Object Windows.Globalization.Language $tag)) }
  } else {
    $ko = $avail | Where-Object { $_ -like 'ko*' } | Select-Object -First 1
    if ($ko) { $engine = [Windows.Media.Ocr.OcrEngine]::TryCreateFromLanguage((New-Object Windows.Globalization.Language $ko)) }
    if (-not $engine) { $engine = [Windows.Media.Ocr.OcrEngine]::TryCreateFromUserProfileLanguages() }
  }
  $engines[$wantLang] = $engine
  return $engine
}

function Process-One($path, $wantLang) {
  if ($wantLang -ne '' -and -not ($avail -contains $wantLang -or ($avail | Where-Object { $_ -like "$wantLang-*" -or $wantLang -like "$_-*" }))) {
    Out-Json @{ error = 'lang_unavailable'; lang = $wantLang; available = $avail }; return
  }
  $engine = Get-Engine $wantLang
  if (-not $engine) { Out-Json @{ error = 'no_engine'; available = $avail }; return }
  $file = Await ([Windows.Storage.StorageFile]::GetFileFromPathAsync($path)) ([Windows.Storage.StorageFile])
  $stream = Await ($file.OpenAsync([Windows.Storage.FileAccessMode]::Read)) ([Windows.Storage.Streams.IRandomAccessStream])
  try {
    $decoder = Await ([Windows.Graphics.Imaging.BitmapDecoder]::CreateAsync($stream)) ([Windows.Graphics.Imaging.BitmapDecoder])
    $bmp = Await ($decoder.GetSoftwareBitmapAsync()) ([Windows.Graphics.Imaging.SoftwareBitmap])
    $res = Await ($engine.RecognizeAsync($bmp)) ([Windows.Media.Ocr.OcrResult])
  } finally { $stream.Dispose() }
  $lines = @(foreach ($l in $res.Lines) {
      @{ t = $l.Text; w = @(foreach ($w in $l.Words) {
            $r = $w.BoundingRect
            @{ t = $w.Text; x = $r.X; y = $r.Y; w = $r.Width; h = $r.Height }
          }) }
    })
  Out-Json @{ lang = $engine.RecognizerLanguage.LanguageTag; available = $avail; lines = $lines }
}

# Tell the Go side setup is done and it may start sending requests — otherwise
# the first request raced against the still-loading WinRT types above.
Out-Json @{ ready = $true }

while ($true) {
  $line = [Console]::In.ReadLine()
  if ($null -eq $line) { break } # stdin closed: the Go side is gone, exit
  if ($line.Trim() -eq '') { continue }
  try {
    $req = ConvertFrom-Json -InputObject $line
    Process-One $req.path $req.lang
  } catch {
    # One bad request must not take the whole warm server down with it —
    # every other OCR call waiting on this process would fail too.
    Out-Json @{ error = $_.Exception.Message }
  }
}
