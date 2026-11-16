# 复用本机服务托管脚本，验证已观察到的依赖断连拒绝与恢复后的新调用。
param([string]$Go = 'go', [ValidateSet('NATS','Etcd')][string]$Dependency = 'NATS')
$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
$workspace = Split-Path $root -Parent
$output = Join-Path $workspace '.runtime/cluster-check'
New-Item -ItemType Directory -Path $output -Force | Out-Null
$testExe = Join-Path $output 'recovery.test.exe'
$buildScript = Join-Path $PSScriptRoot 'Invoke-ExampleBuild.ps1'
& $buildScript -Go $Go -GoArguments @('test', '-mod=readonly', '-c', '-o', $testExe, './app')
if ($LASTEXITCODE -ne 0) { throw '测试程序构建失败' }

& (Join-Path $PSScriptRoot 'Test-LocalServices.ps1') -Run {
    $nodes = [Collections.Generic.List[object]]::new()
    $prefix = 'ht_recovery_' + [Guid]::NewGuid().ToString('N')
    function Start-RecoveryNode([string]$role) {
        $info = [Diagnostics.ProcessStartInfo]::new()
        $info.FileName = $testExe
        $info.ArgumentList.Add('-test.run=^TestRPCFaultChild$')
        $info.ArgumentList.Add('-test.timeout=60s')
        $info.UseShellExecute = $false
        $info.CreateNoWindow = $true
        $info.RedirectStandardInput = $true
        $info.RedirectStandardOutput = $true
        $info.RedirectStandardError = $true
        $info.Environment['HT_FAULT_ROLE'] = $role
        $info.Environment['HT_FAULT_MODE'] = 'recovery'
        $info.Environment['HT_FAULT_PREFIX'] = $prefix
        $process = [Diagnostics.Process]::new()
        $process.StartInfo = $info
        [void]$process.Start()
        $entry = @{Process=$process; Error=$process.StandardError.ReadToEndAsync()}
        $nodes.Add($entry)
        return $entry
    }
    function Wait-Marker($entry, [string]$marker) {
        $deadline = [DateTime]::UtcNow.AddSeconds(25)
        while ([DateTime]::UtcNow -lt $deadline) {
            $read = $entry.Process.StandardOutput.ReadLineAsync()
            $remaining = [Math]::Max(1, [int]($deadline - [DateTime]::UtcNow).TotalMilliseconds)
            if (-not $read.Wait($remaining)) { throw "等待 $marker 超时" }
            if ($null -eq $read.Result) { throw "进程提前结束：$($entry.Error.Result)" }
            if ($read.Result -eq $marker) { return }
        }
        throw "未收到 $marker"
    }
    try {
        $backend = Start-RecoveryNode 'backend'
        Wait-Marker $backend 'FAULT READY'
        $caller = Start-RecoveryNode 'caller'
        Wait-Marker $caller 'RECOVERY READY'
        # 基础服务变量与 Start-TestService 属于外层托管脚本；清理也由它负责。
        $service = if ($Dependency -eq 'NATS') { $natsProcess } else { $etcdProcess }
        $service.Kill()
        $service.WaitForExit()
        $caller.Process.StandardInput.WriteLine('down')
        $caller.Process.StandardInput.Flush()
        Wait-Marker $caller 'RECOVERY REJECTED'
        if ($Dependency -eq 'NATS') { $null = Start-TestService $nats @('-c',$natsConfig) }
        else { $null = Start-TestService $etcd @('--config-file',$etcdConfig) }
        $caller.Process.StandardInput.WriteLine('up')
        $caller.Process.StandardInput.Flush()
        Wait-Marker $caller 'RECOVERY VERIFIED'
        $backend.Process.StandardInput.WriteLine('stop')
        $backend.Process.StandardInput.Flush()
        foreach ($entry in $nodes) {
            if (-not $entry.Process.WaitForExit(10000)) { throw '节点退出超时' }
            if ($entry.Process.ExitCode -ne 0) { throw "节点退出失败：$($entry.Error.Result)" }
        }
        Write-Output "PASS: $Dependency 断连拒绝；恢复后计数为 0；新调用计数为 1；节点正常退出"
    } finally {
        foreach ($entry in $nodes) {
            if (-not $entry.Process.HasExited) { $entry.Process.Kill(); $entry.Process.WaitForExit() }
            $entry.Process.Dispose()
        }
    }
}
