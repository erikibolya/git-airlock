# Builds airlock-desktop.exe, airlock.exe and the Windows installer.
#
#   pwsh -File installer/build.ps1
#   pwsh -File installer/build.ps1 -Config Z:\repos\ActiUnit\Au.Net\.airlock.windows.toml
#
# Without -Config the installer ships airlock.toml.example, so the user picks
# their config on first launch. With -Config that file becomes the default
# {app}\.airlock.toml, which is what you want for a machine you control.
param(
    [string]$Config = "",
    [string]$Version = "",
    [string]$Wails = "",
    [switch]$SkipDesktop,
    [switch]$SkipInstaller
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$files = Join-Path $PSScriptRoot "files"
New-Item -ItemType Directory -Force -Path $files | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $PSScriptRoot "dist") | Out-Null

function Resolve-Command([string]$name, [string[]]$candidates) {
    if ($candidates) {
        foreach ($c in $candidates) { if ($c -and (Test-Path $c)) { return $c } }
    }
    $cmd = Get-Command $name -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    return $null
}

if (-not $Version) {
    $tag = (git -C $root describe --tags --abbrev=0 2>$null)
    if ($tag) { $Version = $tag } else { $Version = "1.4.0" }
}
$Version = $Version.TrimStart("v")
Write-Host "Building Git Airlock $Version" -ForegroundColor Cyan

if (-not $SkipDesktop) {
    $wailsExe = Resolve-Command "wails" @($Wails, "$env:USERPROFILE\go\bin\wails.exe", "$env:LOCALAPPDATA\Programs\wails\wails.exe")
    if (-not $wailsExe) {
        throw "wails not found. Install it with: go install github.com/wailsapp/wails/v2/cmd/wails@latest"
    }
    Push-Location (Join-Path $root "desktop")
    try {
        & $wailsExe build -clean -platform windows/amd64
        if ($LASTEXITCODE -ne 0) { throw "wails build failed with exit code $LASTEXITCODE" }
    } finally {
        Pop-Location
    }
}

$desktopExe = Join-Path $root "desktop\build\bin\airlock-desktop.exe"
if (-not (Test-Path $desktopExe)) {
    throw "airlock-desktop.exe is missing; build the desktop app first (drop -SkipDesktop)."
}
Copy-Item $desktopExe (Join-Path $files "airlock-desktop.exe") -Force

$goExe = Resolve-Command "go" @()
if (-not $goExe) { throw "go not found on PATH." }
Push-Location $root
try {
    & $goExe build -trimpath -ldflags "-s -w -X main.version=$Version" -o (Join-Path $files "airlock.exe") ./cmd/airlock
    if ($LASTEXITCODE -ne 0) { throw "go build failed with exit code $LASTEXITCODE" }
} finally {
    Pop-Location
}

if ($Config) {
    if (-not (Test-Path $Config)) { throw "Config file not found: $Config" }
    Copy-Item $Config (Join-Path $files "airlock.toml") -Force
    Write-Host "Default config: $Config"
} elseif (-not (Test-Path (Join-Path $files "airlock.toml"))) {
    Copy-Item (Join-Path $files "airlock.toml.example") (Join-Path $files "airlock.toml")
    Write-Host "Default config: airlock.toml.example (user picks a config on first launch)"
}

Copy-Item (Join-Path $root "README.md") (Join-Path $files "README.md") -Force
Copy-Item (Join-Path $root "LICENSE") (Join-Path $files "LICENSE") -Force

if ($SkipInstaller) {
    Write-Host "Skipping installer (SkipInstaller)." -ForegroundColor Yellow
    return
}

$iscc = Resolve-Command "iscc" @(
    "$env:LOCALAPPDATA\Programs\Inno Setup 6\ISCC.exe",
    "$env:ProgramFiles\Inno Setup 6\ISCC.exe",
    "${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe"
)
if (-not $iscc) {
    throw "Inno Setup not found. Install it with: winget install -e --id JRSoftware.InnoSetup"
}
Push-Location $PSScriptRoot
try {
    & $iscc "/DAppVersion=$Version" (Join-Path $PSScriptRoot "git-airlock.iss")
    if ($LASTEXITCODE -ne 0) { throw "ISCC failed with exit code $LASTEXITCODE" }
} finally {
    Pop-Location
}

Get-ChildItem (Join-Path $PSScriptRoot "dist") | ForEach-Object {
    Write-Host ("Installer: {0} ({1:N1} MB)" -f $_.FullName, ($_.Length / 1MB)) -ForegroundColor Green
}
