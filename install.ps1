$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
try { [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12 } catch {}

$Repo = 'Metixpro/Macron-Agent'
$Dest = Join-Path $env:LOCALAPPDATA 'Programs\Macron'
$Menu = Join-Path ([Environment]::GetFolderPath('Programs')) 'Macron.lnk'

function Say([string]$text) { Write-Host "    $text" -ForegroundColor DarkGray }
function Step([string]$text) { Write-Host "  $text" }

function Remove-FromPath {
    $current = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (-not $current) { return }
    $kept = ($current -split ';') | Where-Object { $_ -and ($_.TrimEnd('\') -ne $Dest.TrimEnd('\')) }
    [Environment]::SetEnvironmentVariable('Path', ($kept -join ';'), 'User')
}

function Stop-Macron {
    Get-Process -Name 'macron-desktop', 'macron' -ErrorAction SilentlyContinue |
        Where-Object { $_.Path -and $_.Path.StartsWith($Dest, [StringComparison]::OrdinalIgnoreCase) } |
        Stop-Process -Force -ErrorAction SilentlyContinue
}

Write-Host ''
Write-Host '  Macron' -ForegroundColor White
Write-Host '  for Windows' -ForegroundColor DarkGray
Write-Host ''

if ($env:MACRON_UNINSTALL -eq '1') {
    Step 'Removing Macron'
    Stop-Macron
    Remove-Item -Force -Path $Menu -ErrorAction SilentlyContinue
    Remove-FromPath
    Remove-Item -Recurse -Force -Path $Dest -ErrorAction SilentlyContinue
    Say "Your conversations and settings stay in $(Join-Path $HOME '.macron')"
    Write-Host ''
    Write-Host '  Macron was removed.' -ForegroundColor Green
    Write-Host ''
    return
}

$arch = 'amd64'
if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64' -or $env:PROCESSOR_ARCHITEW6432 -eq 'ARM64') { $arch = 'arm64' }

Step 'Finding the latest release'
$release = Invoke-RestMethod -UseBasicParsing -Headers @{ 'User-Agent' = 'macron-installer' } -Uri "https://api.github.com/repos/$Repo/releases/latest"
$asset = $release.assets | Where-Object { $_.name -like "macron-*-windows-$arch.zip" } | Select-Object -First 1
if (-not $asset) { throw "Release $($release.tag_name) has no Windows build for $arch yet." }
Say "$($release.tag_name), windows/$arch"

$tmp = Join-Path ([IO.Path]::GetTempPath()) ('macron-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    $zip = Join-Path $tmp $asset.name
    Step "Downloading $($asset.name)"
    Invoke-WebRequest -UseBasicParsing -Uri $asset.browser_download_url -OutFile $zip
    $sums = $release.assets | Where-Object { $_.name -eq 'SHA256SUMS' } | Select-Object -First 1
    if (-not $sums) { throw 'The release has no checksum file; nothing was installed.' }
    $list = (Invoke-WebRequest -UseBasicParsing -Uri $sums.browser_download_url).Content
    if ($list -is [byte[]]) { $list = [Text.Encoding]::UTF8.GetString($list) }
    $line = $list -split "`n" | Where-Object { $_.Trim().EndsWith($asset.name) } | Select-Object -First 1
    if (-not $line) { throw 'The checksum file does not list this download; nothing was installed.' }
    $want = ($line.Trim() -split '\s+')[0].ToLower()
    $got = (Get-FileHash -Algorithm SHA256 -Path $zip).Hash.ToLower()
    if ($want -ne $got) { throw 'The download does not match its published checksum; nothing was installed.' }
    Say 'Checksum verified'

    Step 'Installing'
    Stop-Macron
    Expand-Archive -Path $zip -DestinationPath $tmp -Force
    $src = Get-ChildItem -Path $tmp -Directory | Where-Object { $_.Name -like 'macron-*' } | Select-Object -First 1
    if (-not $src) { throw 'The download is not a Macron build.' }
    New-Item -ItemType Directory -Path $Dest -Force | Out-Null
    Copy-Item -Path (Join-Path $src.FullName '*') -Destination $Dest -Recurse -Force
    Say $Dest
}
finally {
    Remove-Item -Recurse -Force -Path $tmp -ErrorAction SilentlyContinue
}

$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
$parts = @()
if ($userPath) { $parts = $userPath -split ';' }
if (-not ($parts | Where-Object { $_.TrimEnd('\') -eq $Dest.TrimEnd('\') })) {
    $parts = @($parts | Where-Object { $_ }) + $Dest
    [Environment]::SetEnvironmentVariable('Path', ($parts -join ';'), 'User')
    Say 'Added to PATH: new terminals know the macron command'
}

$shell = New-Object -ComObject WScript.Shell
$link = $shell.CreateShortcut($Menu)
$link.TargetPath = Join-Path $Dest 'macron-desktop.exe'
$link.WorkingDirectory = $Dest
$link.IconLocation = (Join-Path $Dest 'macron-desktop.exe') + ',0'
$link.Description = 'Macron'
$link.Save()
Say 'Added to the Start menu'

$runtime = '{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}'
$found = @(
    "HKLM:\SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\$runtime",
    "HKLM:\SOFTWARE\Microsoft\EdgeUpdate\Clients\$runtime",
    "HKCU:\SOFTWARE\Microsoft\EdgeUpdate\Clients\$runtime"
) | Where-Object { Test-Path $_ }
if (-not $found) {
    Say 'The WebView2 runtime is missing, so Macron opens in Microsoft Edge for now.'
    Say 'Install it for Macron''s own window: https://go.microsoft.com/fwlink/p/?LinkId=2124703'
}

Write-Host ''
Write-Host "  Macron $($release.tag_name) is ready." -ForegroundColor Green
Write-Host '  Open it from the Start menu, or type macron in a new terminal.'
Write-Host ''
if ($env:MACRON_NO_LAUNCH -ne '1') {
    Start-Process -FilePath (Join-Path $Dest 'macron-desktop.exe')
}
