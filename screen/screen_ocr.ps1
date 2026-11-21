$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$ImagePath = '__PATH__'
$WantLang = '__LANG__'
function Out-Json($o) {
  $j = ConvertTo-Json -InputObject $o -Depth 6 -Compress
  $j = [regex]::Replace($j, '[^\x00-\x7F]', { param($m) '\u{0:x4}' -f [int][char]$m.Value })
  [Console]::Out.WriteLine($j)
}
try {
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
  $engine = $null
  if ($WantLang -ne '') {
    $tag = $avail | Where-Object { $_ -eq $WantLang -or $_ -like "$WantLang-*" -or $WantLang -like "$_-*" } | Select-Object -First 1
    if (-not $tag) { Out-Json @{ error = 'lang_unavailable'; lang = $WantLang; available = $avail }; exit 0 }
    $engine = [Windows.Media.Ocr.OcrEngine]::TryCreateFromLanguage((New-Object Windows.Globalization.Language $tag))
  } else {
    $ko = $avail | Where-Object { $_ -like 'ko*' } | Select-Object -First 1
    if ($ko) { $engine = [Windows.Media.Ocr.OcrEngine]::TryCreateFromLanguage((New-Object Windows.Globalization.Language $ko)) }
    if (-not $engine) { $engine = [Windows.Media.Ocr.OcrEngine]::TryCreateFromUserProfileLanguages() }
  }
  if (-not $engine) { Out-Json @{ error = 'no_engine'; available = $avail }; exit 0 }
  $file = Await ([Windows.Storage.StorageFile]::GetFileFromPathAsync($ImagePath)) ([Windows.Storage.StorageFile])
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
} catch {
  Out-Json @{ error = $_.Exception.Message }
}
