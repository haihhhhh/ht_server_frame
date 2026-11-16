# 示例从本机缓存构建；仅给 Go 子进程设置构建环境。
param([string]$Go, [string[]]$GoArguments)
$ErrorActionPreference = 'Stop'
$info = [Diagnostics.ProcessStartInfo]::new($Go)
$info.WorkingDirectory = Split-Path $PSScriptRoot -Parent
if ([IO.Path]::GetDirectoryName($Go)) {
    $info.FileName = [IO.Path]::GetFullPath($Go, $info.WorkingDirectory)
}
$info.UseShellExecute = $false
$info.Environment['GOTOOLCHAIN'] = 'local'
$info.Environment['GOWORK'] = 'off'
$info.Environment['GOPROXY'] = 'off'
foreach ($argument in $GoArguments) { $info.ArgumentList.Add($argument) }

$process = [Diagnostics.Process]::Start($info)
try {
    $process.WaitForExit()
    exit $process.ExitCode
} finally {
    $process.Dispose()
}
