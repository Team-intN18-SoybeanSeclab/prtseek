# Compatibility entry point. Deploy .dev/prtseek.exe before using it.
param([switch]$NoBrowser)
$ErrorActionPreference = 'Stop'
& (Join-Path $PSScriptRoot 'start-prtseek.ps1') -NoBrowser:$NoBrowser
