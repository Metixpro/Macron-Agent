param([Parameter(Mandatory)][string]$Setup,[Parameter(Mandatory)][string]$Version)
$ErrorActionPreference='Stop'
$dest=Join-Path $env:LOCALAPPDATA 'Programs\Macron'
$originalPath=[Environment]::GetEnvironmentVariable('Path','User')
$profile=Join-Path $env:RUNNER_TEMP 'macron-installer-tests'
$env:MACRON_HOME=$profile
New-Item -ItemType Directory -Force $profile | Out-Null
Set-Content -Encoding utf8 "$profile/preserve.txt" 'keep user data'
function Install-Macron {
 $p=Start-Process $Setup -ArgumentList '/VERYSILENT','/SUPPRESSMSGBOXES','/NORESTART','/TASKS=addtopath' -PassThru -Wait
 if($p.ExitCode -ne 0){throw "installer exit $($p.ExitCode)"}
 $got=& "$dest/macron.exe" version
 if($LASTEXITCODE -ne 0 -or $got -notmatch [regex]::Escape($Version)){throw "installed version: $got"}
 if(-not (Test-Path "$dest/macron-desktop.exe")){throw 'desktop executable missing'}
 if(-not (Test-Path (Join-Path ([Environment]::GetFolderPath('Programs')) 'Macron.lnk'))){throw 'Start menu shortcut missing'}
}
function Uninstall-Macron {
 $p=Start-Process "$dest/unins000.exe" -ArgumentList '/VERYSILENT','/SUPPRESSMSGBOXES','/NORESTART' -PassThru
 if(-not $p.WaitForExit(15000)){Stop-Process -Id $p.Id -Force;throw 'uninstaller launcher timed out'}
 $deadline=[DateTime]::UtcNow.AddSeconds(20)
 do {
  $pending=@(Get-CimInstance Win32_Process | Where-Object {$_.Name -like '*unins*' -and $_.CommandLine -like "*$dest*"})
  if(-not $pending.Count){break}
  Start-Sleep -Milliseconds 200
 }while([DateTime]::UtcNow -lt $deadline)
 if($pending.Count){$pending | ForEach-Object {Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue};throw 'uninstaller second phase hung'}
 if(Test-Path "$dest/macron.exe"){throw 'program was not removed'}
 if((Get-Content "$profile/preserve.txt").Trim() -ne 'keep user data'){throw 'user data was removed'}
 $path=[Environment]::GetEnvironmentVariable('Path','User')
 foreach($entry in $path -split ';') {
  if($entry.Trim().Trim('"').TrimEnd('\','/') -ieq $dest){throw 'uninstaller left the installation on PATH'}
 }
}
try {
 Install-Macron
 $psi=[Diagnostics.ProcessStartInfo]::new("$dest/macron.exe",'desktop --serve --json')
 $psi.RedirectStandardInput=$true;$psi.RedirectStandardOutput=$true;$psi.UseShellExecute=$false
 $engine=[Diagnostics.Process]::Start($psi)
 try {
  $line=$engine.StandardOutput.ReadLineAsync()
  if(-not $line.Wait(15000)){throw 'desktop engine startup timed out'}
  $hello=$line.Result | ConvertFrom-Json
  $state=Invoke-RestMethod "http://$($hello.addr)/api/state" -Headers @{Authorization="Bearer $($hello.token)"}
  if($state.info.version -ne $Version){throw 'engine version mismatch'}
 } finally {
  $engine.StandardInput.Close()
  if(-not $engine.WaitForExit(15000)){$engine.Kill();throw 'desktop engine did not stop on stdin close'}
 }
 Uninstall-Macron
 Install-Macron
 $unrelated="$dest-other"
 $casePath="$originalPath;$unrelated;$($dest.ToUpperInvariant())\;`"$dest`";$dest"
 [Environment]::SetEnvironmentVariable('Path',$casePath,'User')
 Uninstall-Macron
 if(([Environment]::GetEnvironmentVariable('Path','User') -split ';') -notcontains $unrelated){throw 'uninstaller changed an unrelated PATH entry'}
 Write-Output "PASS: $Version install, desktop engine, normal uninstall, mixed-case/quoted/duplicate PATH cleanup and data preservation"
} finally {
 [Environment]::SetEnvironmentVariable('Path',$originalPath,'User')
}
