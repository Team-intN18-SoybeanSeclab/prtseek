# Compatibility entry point. The output is now .dev/prtseek-next.exe.
$ErrorActionPreference = 'Stop'
& (Join-Path $PSScriptRoot 'build-prtseek.ps1')
