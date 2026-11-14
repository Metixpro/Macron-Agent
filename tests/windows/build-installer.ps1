$ErrorActionPreference='Stop'
$v = (Select-String '^const Version = "([^"]+)"' pkg/app/app.go).Matches[0].Groups[1].Value
$zips=Join-Path $env:RUNNER_TEMP 'macron-setup-test-zips'
New-Item -ItemType Directory -Force $zips | Out-Null
$savedArch=$env:GOARCH
try {
 foreach($arch in 'amd64','386','arm64') {
  $env:GOARCH=$arch
  $dir=Join-Path $env:RUNNER_TEMP "macron-$v-windows-$arch"
  New-Item -ItemType Directory -Force $dir | Out-Null
  go build -trimpath -ldflags '-s -w' -o "$dir/macron.exe" ./cmd/macron
  if($LASTEXITCODE -ne 0){throw "CLI build failed for $arch"}
  go build -trimpath -ldflags '-s -w -H windowsgui -X main.launch=desktop' -o "$dir/macron-desktop.exe" ./cmd/macron
  if($LASTEXITCODE -ne 0){throw "desktop build failed for $arch"}
  Copy-Item README.md,README.en.md,LICENSE,config.env.example $dir
  Compress-Archive -Path $dir -DestinationPath "$zips/macron-$v-windows-$arch.zip" -Force
 }
} finally { $env:GOARCH=$savedArch }
packaging/windows/build.ps1 -Version $v -Zips $zips
& "$PSScriptRoot/installer.ps1" -Setup "$PWD/packaging/windows/out/Macron-Setup-$v.exe" -Version $v
