param([string]$Version, [string]$Zips)
$ErrorActionPreference = "Stop"
$here = $PSScriptRoot
foreach ($arch in "amd64", "386", "arm64") {
  $zip = Get-ChildItem $Zips -Filter "macron-$Version-windows-$arch.zip" | Select-Object -First 1
  if (-not $zip) { throw "no windows/$arch zip for $Version in $Zips" }
  $tmp = Join-Path $env:RUNNER_TEMP "unzip-$arch"
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
  Expand-Archive $zip.FullName $tmp
  $dir = Join-Path $here "bin\$arch"
  New-Item -ItemType Directory -Force $dir | Out-Null
  Copy-Item (Join-Path $tmp "macron-$Version-windows-$arch\macron.exe"), (Join-Path $tmp "macron-$Version-windows-$arch\macron-desktop.exe") $dir
  if ($arch -eq "amd64") {
    $extra = Join-Path $here "extra"
    New-Item -ItemType Directory -Force $extra | Out-Null
    Copy-Item (Join-Path $tmp "macron-$Version-windows-$arch\*") $extra -Recurse -Exclude "*.exe"
  }
}
$iscc = Join-Path ${env:ProgramFiles(x86)} "Inno Setup 6\ISCC.exe"
if (-not (Test-Path $iscc)) {
  choco install innosetup -y --no-progress | Out-Null
}
if (-not (Test-Path $iscc)) { throw "Inno Setup is not available" }
& $iscc /Q "/DVersion=$Version" (Join-Path $here "macron.iss")
if ($LASTEXITCODE -ne 0) { throw "the installer did not build" }
Get-Item (Join-Path $here "out\Macron-Setup-$Version.exe")
