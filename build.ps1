# build.ps1 - build the three programs on Windows, into C:\Program Files\Matriline (or the
# folder given), from any folder. Program Files needs an administrator PowerShell; the folder
# is then also added to the system PATH (user: easy to find, unlike %LOCALAPPDATA%).
#   powershell -ExecutionPolicy Bypass -File build.ps1 [-NoSecurity] [-Out C:\some\folder]
#   -NoSecurity   build without the security functions (trusted machines only; INSTALL.md)
# Linux and macOS: build.sh. Release binaries with published checksums: tools/release.sh.
param([switch]$NoSecurity, [string]$Out = "$env:ProgramFiles\Matriline")
$ErrorActionPreference = 'Stop'
$admin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $PSBoundParameters.ContainsKey('Out') -and -not $admin) {
    Write-Error "Installing into $Out needs an administrator PowerShell (right-click PowerShell > Run as administrator), or choose a folder of yours with -Out"
    exit 1
}
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Error "Go is not installed: get it from https://go.dev/dl/ (the Windows .msi), then open a new PowerShell"
    exit 1
}
New-Item -ItemType Directory -Force $Out | Out-Null
Push-Location (Join-Path $PSScriptRoot 'src')
try {
    foreach ($p in 'server', 'client', 'relay') {
        $exe = Join-Path $Out "matriline-$p.exe"
        if ($NoSecurity) { go build -tags nosecurity -o $exe ".\$p" } else { go build -o $exe ".\$p" }
        if ($LASTEXITCODE -ne 0) { throw "building $p failed" }
        Write-Host "built $exe$(if ($NoSecurity) { ' (no-security build)' })"
    }
} finally { Pop-Location }
$machinePath = [Environment]::GetEnvironmentVariable('Path', 'Machine')
if ((($env:Path -split ';') -notcontains $Out) -and (($machinePath -split ';') -notcontains $Out)) {
    if ($admin) {
        [Environment]::SetEnvironmentVariable('Path', ($machinePath.TrimEnd(';') + ';' + $Out), 'Machine')
        Write-Host "added $Out to the system PATH: open a new PowerShell to use the commands by name"
    } else {
        Write-Host "note: $Out is not in your PATH; add it (Settings > Environment variables), or call the programs with their full path"
    }
}
