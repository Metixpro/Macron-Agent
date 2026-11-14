$ErrorActionPreference = "Stop"
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
param([Parameter(Mandatory)][string]$Exe, [string]$Gui)
$temp = if ($env:RUNNER_TEMP) { $env:RUNNER_TEMP } else { [IO.Path]::GetTempPath() }
$root = Join-Path $temp ("macron-engine-" + [IO.Path]::GetFileNameWithoutExtension($Exe))
New-Item -ItemType Directory -Force $root | Out-Null
$env:MACRON_HOME = Join-Path $root "home dir"
$work = Join-Path $root "پروژه من"
New-Item -ItemType Directory -Force $work | Out-Null
Write-Host "== $Exe version"
& $Exe version
$fake = Start-Process python -ArgumentList @("$PSScriptRoot\fake_model.py", "18941", "$root\fake.log") -PassThru -NoNewWindow
Start-Sleep 2
$psi = [System.Diagnostics.ProcessStartInfo]::new($Exe, "desktop --serve --json")
$psi.RedirectStandardInput = $true
$psi.RedirectStandardOutput = $true
$psi.RedirectStandardError = $true
$psi.UseShellExecute = $false
$p = [System.Diagnostics.Process]::Start($psi)
$line = $p.StandardOutput.ReadLine()
if (-not $line) { Write-Host $p.StandardError.ReadToEnd(); throw "the engine did not start" }
Write-Host "== engine: $line"
$j = $line | ConvertFrom-Json
$base = "http://$($j.addr)"
$h = @{ Authorization = "Bearer $($j.token)"; "X-Macron" = "1" }
function Api($method, $path, $body) {
  $params = @{ Method = $method; Uri = "$base$path"; Headers = $h }
  if ($null -ne $body) {
    $params.ContentType = "application/json; charset=utf-8"
    $params.Body = [System.Text.Encoding]::UTF8.GetBytes(($body | ConvertTo-Json -Depth 8 -Compress))
  }
  try { return Invoke-RestMethod @params } catch { Write-Host "API $method $path failed: $($_.ErrorDetails.Message) $_"; throw }
}
$state = Api GET "/api/state"
Write-Host "== state: version $($state.info.version) platform $($state.info.platform) projects $($state.projects.Count)"
Api POST "/api/providers/connect" @{ name = "fake"; url = "http://127.0.0.1:18941/v1"; key = "x"; model = "fake-model" } | Out-Null
Write-Host "== model connected"
$proj = Api POST "/api/projects" @{ path = $work }
Write-Host "== project $($proj.id) $($proj.path)"
$fsl = Api GET ("/api/fs?path=" + [uri]::EscapeDataString($root))
Write-Host "== fs listing: $(($fsl.dirs | ForEach-Object { $_.name }) -join ', ') roots $(($fsl.roots | ForEach-Object { $_.path }) -join ', ')"
Api POST "/api/projects/$($proj.id)/send" @{ text = "smoke test: run every step" } | Out-Null
$deadline = (Get-Date).AddMinutes(4)
do {
  Start-Sleep -Milliseconds 700
  $snap = Api GET "/api/projects/$($proj.id)"
  if ($snap.live.ask) {
    Write-Host "== approving: $($snap.live.ask.command)"
    Api POST "/api/projects/$($proj.id)/answer" @{ id = $snap.live.ask.id; choice = "once" } | Out-Null
  }
} while ($snap.live.running -and (Get-Date) -lt $deadline)
$bad = 0
$expect = @(
  @("bash_execute", "ps ok"), @("bash_execute", "سلام دنیا"), @("write_file", "saved"), @("apply_patch", "Applied"),
  @("edit_file", "Edited"), @("bash_execute", "sum 5 Macron"), @("bash_execute", "slept"), @("bash_execute", "exit 3"),
  @("run_code", "42"), @("shell_jobs", "bgdone"), @("bash_execute", "probe="), @("read_file", "return a + b")
)
$tools = @($snap.blocks | Where-Object { $_.kind -eq "tool" })
foreach ($b in $snap.blocks) {
  switch ($b.kind) {
    "tool" {
      $mark = if ($b.failed) { "failed" } else { "ok" }
      Write-Host "-- [$mark] $($b.name) $($b.arg)"
      Write-Host ($b.output -split "`n" | Select-Object -First 12 | Out-String)
    }
    "file" { Write-Host "-- [file] $($b.file.base) $($b.file.size) bytes $($b.file.type) '$($b.file.caption)'" }
    default { Write-Host "-- [$($b.kind)] $($b.text)" }
  }
}
foreach ($e in $expect) {
  $hit = $tools | Where-Object { $_.name -eq $e[0] -and $_.output -and $_.output.Contains($e[1]) }
  if ($hit) { Write-Host "PASS $($e[0]) has '$($e[1])'" } else { Write-Host "MISSING $($e[0]) with '$($e[1])'"; $bad++ }
}
$unexpected = $tools | Where-Object { $_.failed -and -not $_.output.Contains("exit 3") }
foreach ($u in $unexpected) { Write-Host "UNEXPECTED FAILURE $($u.name): $($u.output)"; $bad++ }
if (-not ($snap.blocks | Where-Object { $_.kind -eq "file" })) { Write-Host "NO FILE CARD"; $bad++ }
$calc = Join-Path $work "calc.py"
if (Test-Path $calc) {
  $raw = [IO.File]::ReadAllText($calc)
  Write-Host "== calc.py (CRLF kept: $($raw.Contains("`r`n"))):"
  Write-Host $raw
  if ($raw.Contains("a + b + 1")) { Write-Host "PATCH NOT APPLIED"; $bad++ }
} else { Write-Host "calc.py MISSING"; $bad++ }
$dl = Invoke-WebRequest -Uri "$base/api/projects/$($proj.id)/file?name=calc.py&download=1" -Headers $h -UseBasicParsing
Write-Host "== download: $($dl.StatusCode) $($dl.Headers['Content-Disposition']) $($dl.RawContentLength) bytes"
Get-ChildItem $env:MACRON_HOME | ForEach-Object { Write-Host "home: $($_.Name)" }
Api POST "/api/projects/$($proj.id)/send" @{ text = "smoke test: long job" } | Out-Null
$p.StandardInput.Close()
if (-not $p.WaitForExit(15000)) { Write-Host "engine did not stop on stdin close"; $p.Kill(); $bad++ }
Write-Host "== engine exit $($p.ExitCode)"
Stop-Process -Id $fake.Id -ErrorAction SilentlyContinue
Get-Content "$env:MACRON_HOME\desktop.log" -ErrorAction SilentlyContinue | Select-Object -Last 20
if ($Gui) {
  $gui = $Gui
  $env:MACRON_HOME = Join-Path $root "gui home"
  Write-Host "== starting the desktop app $gui"
  $g = Start-Process $gui -PassThru
  $lock = Join-Path $env:MACRON_HOME "desktop.json"
  $deadline = (Get-Date).AddSeconds(40)
  while (-not (Test-Path $lock) -and (Get-Date) -lt $deadline -and -not $g.HasExited) { Start-Sleep -Milliseconds 500 }
  if (Test-Path $lock) {
    $info = Get-Content $lock -Raw
    Write-Host "== desktop lock: $info"
    Start-Sleep 5
    if ($g.HasExited) { Write-Host "DESKTOP EXITED $($g.ExitCode)"; $bad++ } else { Write-Host "PASS the desktop app is running" }
  } else {
    Write-Host "DESKTOP DID NOT START (exited: $($g.HasExited) code $($g.ExitCode))"; $bad++
  }
  Get-Content (Join-Path $env:MACRON_HOME "desktop.log") -ErrorAction SilentlyContinue | Select-Object -Last 15
  Get-Process | Where-Object { $_.Path -eq $gui } | Stop-Process -Force -ErrorAction SilentlyContinue
}
if ($bad -gt 0) { throw "$bad problems" }
Write-Host "SMOKE OK $Exe"
