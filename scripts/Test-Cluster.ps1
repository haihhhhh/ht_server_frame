# 真实多进程回环验证；仅停止本脚本启动的进程，保留产物便于复用。
param([string]$Go = 'go')
$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
$workspace = Split-Path $root -Parent
$output = Join-Path $workspace '.runtime/cluster-check'
New-Item -ItemType Directory -Path $output -Force | Out-Null
$server = Join-Path $output 'cluster.exe'
$exampleClientExe = Join-Path $output 'client.exe'
$buildScript = Join-Path $PSScriptRoot 'Invoke-ExampleBuild.ps1'
& $buildScript -Go $Go -GoArguments @('build', '-mod=readonly', '-o', $server, './examples/cluster')
if ($LASTEXITCODE -ne 0) { throw '集群示例构建失败' }
& $buildScript -Go $Go -GoArguments @('build', '-mod=readonly', '-o', $exampleClientExe, './examples/client')
if ($LASTEXITCODE -ne 0) { throw '客户端构建失败' }

& (Join-Path $PSScriptRoot 'Test-LocalServices.ps1') -Run {
    $children = [Collections.Generic.List[object]]::new()
    $prefix = 'ht_check_' + [Guid]::NewGuid().ToString('N')
    function Start-Node([string]$role) {
        $info = [Diagnostics.ProcessStartInfo]::new()
        $info.FileName = $server
        $info.UseShellExecute = $false
        $info.CreateNoWindow = $true
        $info.RedirectStandardInput = $true
        $info.RedirectStandardOutput = $true
        $info.RedirectStandardError = $true
        foreach ($arg in @('-role',$role,'-addr','127.0.0.1:0','-prefix',$prefix)) { $info.ArgumentList.Add($arg) }
        $p = [Diagnostics.Process]::new()
        $p.StartInfo = $info
        [void]$p.Start()
        $entry = @{Process=$p; Error=$p.StandardError.ReadToEndAsync()}
        $children.Add($entry)
        $deadline = [DateTime]::UtcNow.AddSeconds(20)
        while ([DateTime]::UtcNow -lt $deadline) {
            $lineTask = $p.StandardOutput.ReadLineAsync()
            if (-not $lineTask.Wait(20000)) { throw "$role 启动超时" }
            $line = $lineTask.Result
            if ($null -eq $line) { throw "$role 提前退出：$($entry.Error.Result)" }
            if ($line.StartsWith("READY $role ")) { return @{Entry=$entry; Address=$line.Substring(("READY $role ").Length)} }
        }
        throw "$role 未就绪"
    }
    function Stop-Node($entry) {
        $p=$entry.Process
        $tail=$p.StandardOutput.ReadToEndAsync()
        $p.StandardInput.WriteLine('stop')
        $p.StandardInput.Flush()
        if (-not $p.WaitForExit(10000)) { throw '显式停止超时' }
        if ($p.ExitCode -ne 0 -or $tail.Result -notmatch 'STOPPED') { throw "停止失败：$($entry.Error.Result)" }
    }
    try {
        $backend=Start-Node 'backend'
        $gateway=Start-Node 'gateway'
        $reply = & $exampleClientExe -url ('ws://'+$gateway.Address) -route echo.handle -value cluster-smoke
        if ($LASTEXITCODE -ne 0 -or $reply -ne 'backend: cluster-smoke') { throw "集群响应错误：$reply" }
        Write-Output "PASS: WebSocket → gateway Handler → NATS → backend Remote → $reply"
        Stop-Node $gateway.Entry
        Stop-Node $backend.Entry
        Write-Output 'PASS: 网关与后端均显式 Stop，退出码 0'
    } finally {
        foreach ($entry in $children) {
            if (-not $entry.Process.HasExited) { $entry.Process.Kill(); $entry.Process.WaitForExit() }
            $entry.Process.Dispose()
        }
    }
}
