$ErrorActionPreference = 'Stop'
$projectRoot = [IO.Path]::GetFullPath($PSScriptRoot)
$workspacePrefix = $projectRoot.TrimEnd('\') + [IO.Path]::DirectorySeparatorChar
$goPath = Join-Path $projectRoot '.dev/runtime/go/bin/go.exe'
$outputPath = Join-Path $projectRoot '.dev/prtseek-next.exe'

if (!(Test-Path -LiteralPath $goPath -PathType Leaf)) {
    $goCommand = Get-Command go.exe -ErrorAction SilentlyContinue
    if (!$goCommand) { throw '请安装 Go 并加入 PATH，或放入 .dev/runtime/go/bin。' }
    $goPath = $goCommand.Source
}
if (!(Get-Command npm.cmd -ErrorAction SilentlyContinue)) { throw '未找到 npm.cmd，请先准备 Node.js 构建环境。' }

$oldExport = [Environment]::GetEnvironmentVariable('NEXT_EXPORT', 'Process')
$oldGoCache = [Environment]::GetEnvironmentVariable('GOCACHE', 'Process')
$oldGoModCache = [Environment]::GetEnvironmentVariable('GOMODCACHE', 'Process')
$oldGoPath = [Environment]::GetEnvironmentVariable('GOPATH', 'Process')
Push-Location -LiteralPath $projectRoot
try {
    Push-Location -LiteralPath (Join-Path $projectRoot 'web')
    try {
        & npm.cmd ci --cache (Join-Path $projectRoot '.dev/npm-cache')
        if ($LASTEXITCODE -ne 0) { throw '前端依赖安装失败。' }
        $env:NEXT_EXPORT = '1'
        & npm.cmd run build:static
        if ($LASTEXITCODE -ne 0) { throw '前端静态导出失败。' }
    } finally {
        [Environment]::SetEnvironmentVariable('NEXT_EXPORT', $oldExport, 'Process')
        Pop-Location
    }

    $sourcePath = [IO.Path]::GetFullPath((Join-Path $projectRoot 'web/out'))
    if (!(Test-Path -LiteralPath (Join-Path $sourcePath 'index.html') -PathType Leaf)) {
        throw '前端静态导出缺少 web/out/index.html，保留原有嵌入目录。'
    }
    $destinationPath = [IO.Path]::GetFullPath((Join-Path $projectRoot 'server/webui/dist'))
    $allowedPath = [IO.Path]::GetFullPath((Join-Path $projectRoot 'server/webui')).TrimEnd('\') + [IO.Path]::DirectorySeparatorChar
    $archiveParent = [IO.Path]::GetFullPath((Join-Path $projectRoot '.dev/embed-history'))
    $archivePath = [IO.Path]::GetFullPath((Join-Path $archiveParent ([guid]::NewGuid().ToString('N'))))
    if (!$sourcePath.StartsWith($workspacePrefix, [StringComparison]::OrdinalIgnoreCase) -or
        !$destinationPath.StartsWith($allowedPath, [StringComparison]::OrdinalIgnoreCase) -or
        !$archiveParent.StartsWith($workspacePrefix, [StringComparison]::OrdinalIgnoreCase) -or
        !$archivePath.StartsWith($archiveParent + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
        throw '构建输入、嵌入或归档路径越界。'
    }
    foreach ($checkedPath in @((Join-Path $projectRoot 'web'), $sourcePath, (Join-Path $projectRoot 'server'), (Split-Path -Parent $destinationPath), $destinationPath, (Join-Path $projectRoot '.dev'), $archiveParent)) {
        if (Test-Path -LiteralPath $checkedPath) {
            $item = Get-Item -LiteralPath $checkedPath -Force
            if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw '构建目录含重解析点，拒绝移动或覆盖。' }
        }
    }
    if (Test-Path -LiteralPath $destinationPath) {
        New-Item -ItemType Directory -Path $archiveParent -Force | Out-Null
        Move-Item -LiteralPath $destinationPath -Destination $archivePath
        Write-Host "原嵌入前端已归档：$archivePath"
    }
    New-Item -ItemType Directory -Path $destinationPath -Force | Out-Null
    Get-ChildItem -LiteralPath $sourcePath -Force | Copy-Item -Destination $destinationPath -Recurse -Force
    $env:GOCACHE = Join-Path $projectRoot '.dev/gocache'
    $env:GOMODCACHE = Join-Path $projectRoot '.dev/gomodcache'
    $env:GOPATH = Join-Path $projectRoot '.dev/gopath'
    & $goPath build -buildvcs=false -tags embedui -ldflags '-s -w -X main.version=prtseek-v1.3.2' -o $outputPath ./cmd/prtseek
    if ($LASTEXITCODE -ne 0) { throw 'prtseek 后端构建失败，运行中的程序未被替换。' }
    Write-Host "构建成功：$outputPath"
    Write-Host '此脚本只构建，不停止服务、不替换 .dev/prtseek.exe 或任何旧版程序。'
} finally {
    [Environment]::SetEnvironmentVariable('NEXT_EXPORT', $oldExport, 'Process')
    [Environment]::SetEnvironmentVariable('GOCACHE', $oldGoCache, 'Process')
    [Environment]::SetEnvironmentVariable('GOMODCACHE', $oldGoModCache, 'Process')
    [Environment]::SetEnvironmentVariable('GOPATH', $oldGoPath, 'Process')
    Pop-Location
}
