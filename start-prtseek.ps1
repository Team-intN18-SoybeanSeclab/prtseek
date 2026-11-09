param([switch]$NoBrowser, [switch]$Build, [int]$Port = 18787, [int]$ProxyPort = 18788)
$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath($PSScriptRoot)
$binary = Join-Path $root '.dev/prtseek.exe'
$release = Join-Path $root 'prtseek.exe'
Push-Location -LiteralPath $root
try {
    if ((Test-Path -LiteralPath $release) -and !$Build) { $binary = $release }
    if ($Build -or !(Test-Path -LiteralPath $binary)) {
        & (Join-Path $root 'build-prtseek.ps1')
        if (!$?) { throw 'Build failed.' }
        Copy-Item -LiteralPath (Join-Path $root '.dev/prtseek-next.exe') -Destination $binary -Force
    }
    $url = "http://127.0.0.1:$Port/chat/"
    Write-Host "prtseek: $url (Ctrl-C to stop)"
    Write-Host 'SQLite is created automatically in data/. No database service is needed.'
    if (!$NoBrowser) { Start-Process $url }
    & $binary -addr "127.0.0.1:$Port" -proxy "127.0.0.1:$ProxyPort" -data (Join-Path $root 'data')
    exit $LASTEXITCODE
} finally { Pop-Location }
