param([Parameter(Mandatory)][string]$Exe)
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
$OutputEncoding = [Console]::OutputEncoding
$root = Join-Path ([IO.Path]::GetTempPath()) ('macron-cli-' + [guid]::NewGuid().ToString('N'))
$programs = Join-Path $root "برنامه من O'Brien `$name"
$homeDir = Join-Path $root '.macron'
New-Item -ItemType Directory -Force $programs, $homeDir | Out-Null
$originalPath = $env:PATH
$originalHome = $env:MACRON_HOME
$originalEnvFile = $env:MACRON_ENV_FILE
function Path-StillExists([string]$path) {
    try { Get-Item -LiteralPath $path -Force -ErrorAction Stop | Out-Null; return $true }
    catch {
        if ($_.CategoryInfo.Category -eq 'ObjectNotFound') { return $false }
        # Windows reports a file awaiting final deletion as access denied.
        if ($_.CategoryInfo.Category -eq 'PermissionDenied' -or $_.Exception -is [UnauthorizedAccessException]) { return $true }
        throw
    }
}
try {
    $localExe = Join-Path $programs 'macron.exe'
    Copy-Item -LiteralPath $Exe -Destination $localExe
    $unrelated = Join-Path $programs 'keep-this.txt'
    Set-Content -LiteralPath $unrelated -Value 'unrelated'
    $env:MACRON_HOME = $homeDir
    $env:MACRON_ENV_FILE = Join-Path $homeDir 'config.env'
    Set-Content -LiteralPath (Join-Path $homeDir 'secret.key') -Value 'fixture-key'
    # Check a minimal Windows installation, with no Unix tools or Python.
    $env:PATH = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0'
    $result = & $localExe doctor --offline 2>&1 | Out-String
    if ($LASTEXITCODE -ne 0) { throw "doctor failed: $result" }
    if ($result -match 'sudo |apt install|chmod |install.sh|deadsnakes|sshpass is|rsync is|journalctl') { throw "Unix advice on Windows: $result" }
    Write-Host $result
    [IO.File]::WriteAllText((Join-Path $homeDir 'macron.log'), "old`nسلام ویندوز`nlast`n", [Text.UTF8Encoding]::new($false))
    $result = & $localExe logs -n 2 2>&1 | Out-String
    if ($LASTEXITCODE -ne 0 -or $result -notmatch 'سلام ویندوز' -or $result -match 'old') { throw "logs failed: $result" }
    $env:PATH = $originalPath
    & $localExe backup (Join-Path $root 'profile.tar.gz')
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path (Join-Path $root 'profile.tar.gz'))) { throw 'backup failed' }
    $result = & $localExe uninstall --dry-run --keep-data 2>&1 | Out-String
    if ($LASTEXITCODE -ne 0 -or $result -notmatch 'Windows uninstall' -or -not (Path-StillExists $localExe)) { throw "uninstall dry-run failed: $result" }
    $result = & $localExe uninstall --yes --keep-data 2>&1 | Out-String
    if ($LASTEXITCODE -ne 0) { throw "uninstall failed: $result" }
    Write-Host $result
    $deadline = (Get-Date).AddSeconds(30)
    while ((Path-StillExists $localExe) -and (Get-Date) -lt $deadline) { Start-Sleep -Milliseconds 200 }
    if (Path-StillExists $localExe) { throw "Windows still locks the executable after uninstall: $result" }
    if (-not (Test-Path -LiteralPath $unrelated) -or -not (Test-Path -LiteralPath (Join-Path $homeDir 'secret.key'))) { throw 'uninstall removed unrelated files or --keep-data was ignored' }
    # Repeat without --keep-data: only the selected temporary profile is erased.
    Copy-Item -LiteralPath $Exe -Destination $localExe
    $result = & $localExe uninstall --yes 2>&1 | Out-String
    if ($LASTEXITCODE -ne 0) { throw "uninstall with data removal failed: $result" }
    $deadline = (Get-Date).AddSeconds(30)
    while (((Path-StillExists $localExe) -or (Path-StillExists $homeDir)) -and (Get-Date) -lt $deadline) { Start-Sleep -Milliseconds 200 }
    if ((Path-StillExists $localExe) -or (Path-StillExists $homeDir) -or -not (Test-Path -LiteralPath $unrelated)) {
        if ($result -match 'Log: (.+)') { Get-Content -LiteralPath $Matches[1].Trim() -ErrorAction SilentlyContinue | Write-Host }
        throw "uninstall did not remove exactly the selected installation: $result"
    }
    Write-Host "Windows CLI passed on PowerShell $($PSVersionTable.PSVersion)"
} finally {
    $env:PATH = $originalPath
    $env:MACRON_HOME = $originalHome
    $env:MACRON_ENV_FILE = $originalEnvFile
    Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue
}
