param([Parameter(Mandatory)][string]$Exe, [Parameter(Mandatory)][string]$Sshd)
$ErrorActionPreference = "Stop"
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$temp = if ($env:RUNNER_TEMP) { $env:RUNNER_TEMP } else { [IO.Path]::GetTempPath() }
$root = Join-Path $temp ("macron-ui-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force $root, "$root\shots", "$root\remote user\.local\bin", "$root\local work" | Out-Null
Copy-Item $Exe "$root\remote user\.local\bin\macron.exe"
Write-Host "== UI fixture output: $root"
$fake = $sshProcess = $eng = $null
function Wait-ForFixture($proc, $output, $errorPath, $pattern, $name) {
  for ($attempt = 0; $attempt -lt 40 -and -not $proc.HasExited; $attempt++) {
    Start-Sleep -Milliseconds 100
    $line = Select-String -LiteralPath $output -Pattern $pattern | Select-Object -First 1
    if ($line) { return $line }
  }
  Get-Content -LiteralPath $errorPath -ErrorAction SilentlyContinue | Write-Host
  throw "the $name fixture did not start"
}
try {
  $fake = Start-Process python -ArgumentList @("`"$PSScriptRoot\fake_model.py`"", "0", "`"$root\fake.log`"") -PassThru -WindowStyle Hidden -RedirectStandardOutput "$root\fake.out" -RedirectStandardError "$root\fake.err"
  $modelPort = Wait-ForFixture $fake "$root\fake.out" "$root\fake.err" '^([0-9]+)$' 'model'
  $env:MACRON_UI_MODEL_URL = "http://127.0.0.1:$($modelPort.Matches[0].Groups[1].Value)/v1"
  $remoteHome = "$root\remote home"
  $env:REMOTE_ENV = "MACRON_HOME=$remoteHome|HOME=$root\remote user|USERPROFILE=$root\remote user|PATH=$root\remote user\.local\bin;$env:PATH"
  $sshProcess = Start-Process $Sshd -PassThru -WindowStyle Hidden -RedirectStandardOutput "$root\sshd.out" -RedirectStandardError "$root\sshd.err"
  $listener = Wait-ForFixture $sshProcess "$root\sshd.out" "$root\sshd.err" '^([0-9]+) (SHA256:\S+)' 'SSH'
  $env:MACRON_UI_SSH_PORT = $listener.Matches[0].Groups[1].Value
  Write-Host "== sshd fingerprint: $($listener.Matches[0].Groups[2].Value)"
  $env:MACRON_HOME = "$root\local home"
  $env:WORKDIR = "$root\local work"
  $out = "$root\engine.out"
  $eng = Start-Process $Exe -ArgumentList @("desktop", "--no-open", "--addr", "127.0.0.1:0") -PassThru -WindowStyle Hidden -RedirectStandardOutput $out -RedirectStandardError "$root\engine.err"
  $url = $null
  for ($i = 0; $i -lt 60 -and -not $url -and -not $eng.HasExited; $i++) {
    Start-Sleep -Milliseconds 500
    $line = Select-String -Path $out -Pattern '(http://\S+)' | Select-Object -First 1
    if ($line) { $url = $line.Matches[0].Groups[1].Value }
  }
  if (-not $url) { Get-Content "$root\engine.err"; throw "the engine did not print its address" }
  Write-Host "== engine at $(([uri]$url).GetLeftPart([System.UriPartial]::Authority))"
  $mods = Join-Path $temp "macron-ui-node"
  npm.cmd install --prefix $mods --no-fund --no-audit --silent playwright-core@1.55.0 | Out-Null
  if ($LASTEXITCODE -ne 0) { throw "could not install Playwright for the UI tests" }
  $env:NODE_PATH = Join-Path $mods "node_modules"
  node "$PSScriptRoot\ui.js" $url "$root\shots"
  $code = $LASTEXITCODE
  Write-Host "== local desktop.log"
  Get-Content "$env:MACRON_HOME\desktop.log" -ErrorAction SilentlyContinue | Select-Object -Last 40
  Write-Host "== remote desktop.log"
  Get-Content "$remoteHome\desktop.log" -ErrorAction SilentlyContinue | Select-Object -Last 25
  Write-Host "== engine stderr"
  Get-Content "$root\engine.err" -ErrorAction SilentlyContinue | Select-Object -Last 20
  Write-Host "== engine alive at the end: $(-not $eng.HasExited)"
  if ($code -ne 0) { throw "the UI test found problems" }
  Write-Host "UI SMOKE OK"
} finally {
  foreach ($proc in @($eng, $sshProcess, $fake)) {
    if ($proc -and -not $proc.HasExited) { Stop-Process -Id $proc.Id -ErrorAction SilentlyContinue }
  }
}
