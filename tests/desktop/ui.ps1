$ErrorActionPreference = "Stop"
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
param([Parameter(Mandatory)][string]$Exe, [Parameter(Mandatory)][string]$Sshd)
$temp = if ($env:RUNNER_TEMP) { $env:RUNNER_TEMP } else { [IO.Path]::GetTempPath() }
$root = Join-Path $temp "macron-ui"
New-Item -ItemType Directory -Force $root, "$root\shots", "$root\remote user\.local\bin", "$root\local work" | Out-Null
Copy-Item $Exe "$root\remote user\.local\bin\macron.exe"
$fake = Start-Process python -ArgumentList @("$PSScriptRoot\fake_model.py", "18941", "$root\fake.log") -PassThru -NoNewWindow
$remoteHome = "$root\remote home"
$env:REMOTE_ENV = "MACRON_HOME=$remoteHome|HOME=$root\remote user|USERPROFILE=$root\remote user|PATH=$root\remote user\.local\bin;$env:PATH"
$sshd = Start-Process $Sshd -PassThru -NoNewWindow -RedirectStandardOutput "$root\sshd.out"
Start-Sleep 2
Write-Host "== sshd fingerprint: $(Get-Content "$root\sshd.out")"
$env:MACRON_HOME = "$root\local home"
$env:WORKDIR = "$root\local work"
$out = "$root\engine.out"
$eng = Start-Process $Exe -ArgumentList @("desktop", "--no-open", "--addr", "127.0.0.1:18960") -PassThru -NoNewWindow -RedirectStandardOutput $out -RedirectStandardError "$root\engine.err"
$url = $null
for ($i = 0; $i -lt 60 -and -not $url; $i++) {
  Start-Sleep -Milliseconds 500
  if (Test-Path $out) { $m = Select-String -Path $out -Pattern "(http://\S+)" | Select-Object -First 1; if ($m) { $url = $m.Matches[0].Groups[1].Value } }
}
if (-not $url) { Get-Content "$root\engine.err"; throw "the engine did not print its address" }
Write-Host "== engine at $url"
$mods = Join-Path $root "node"
npm install --prefix $mods --no-fund --no-audit --silent playwright-core@1.55.0 | Out-Null
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
Stop-Process -Id $eng.Id, $sshd.Id, $fake.Id -Force -ErrorAction SilentlyContinue
if ($code -ne 0) { throw "the UI test found problems" }
Write-Host "UI SMOKE OK"
