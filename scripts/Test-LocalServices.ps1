# 仅验证工作区的本机便携服务；无论成功或失败，退出时关闭本脚本启动的进程。
param([scriptblock]$Run)
$ErrorActionPreference = 'Stop'
$workspace = Split-Path (Split-Path $PSScriptRoot -Parent) -Parent
$runtime = Join-Path $workspace '.runtime/local-services'
$nats = Join-Path $workspace '.tools/nats-server-v2.15.1-windows-amd64/nats-server.exe'
$etcd = Join-Path $workspace '.tools/etcd-v3.5.34-windows-amd64/etcd.exe'
$etcdctl = Join-Path $workspace '.tools/etcd-v3.5.34-windows-amd64/etcdctl.exe'
foreach ($exe in @($nats, $etcd, $etcdctl)) {
    if (-not (Test-Path -LiteralPath $exe)) { throw "缺少已固定版本的程序：$exe" }
}
$listeners = [Net.NetworkInformation.IPGlobalProperties]::GetIPGlobalProperties().GetActiveTcpListeners()
foreach ($port in @(14222,12379,12380)) {
    if ($listeners.Port -contains $port) { throw "端口 $port 已占用，不启动或终止其他服务" }
}
New-Item -ItemType Directory -Path $runtime -Force | Out-Null
$natsLog = (Join-Path $runtime 'nats.log').Replace('\','/')
$etcdLog = (Join-Path $runtime 'etcd.log').Replace('\','/')
$data = (Join-Path $runtime 'etcd-data').Replace('\','/')
$natsConfig = Join-Path $runtime 'nats.conf'
$etcdConfig = Join-Path $runtime 'etcd.yaml'
@"
listen: 127.0.0.1:14222
max_connections: 64
max_payload: 1048576
max_pending: 4194304
log_file: '$natsLog'
log_size_limit: 1048576
logfile_max_num: 2
"@ | Set-Content -LiteralPath $natsConfig -Encoding utf8
@"
name: ht-local
data-dir: '$data'
listen-client-urls: http://127.0.0.1:12379
advertise-client-urls: http://127.0.0.1:12379
listen-peer-urls: http://127.0.0.1:12380
initial-advertise-peer-urls: http://127.0.0.1:12380
initial-cluster: ht-local=http://127.0.0.1:12380
initial-cluster-token: ht-local-development
initial-cluster-state: new
quota-backend-bytes: 134217728
snapshot-count: 1000
max-snapshots: 2
max-wals: 2
auto-compaction-mode: periodic
auto-compaction-retention: 1h
logger: zap
log-level: warn
log-outputs:
  - '$etcdLog'
enable-log-rotation: true
log-rotation-config-json: '{"maxsize":1,"maxage":1,"maxbackups":2}'
"@ | Set-Content -LiteralPath $etcdConfig -Encoding utf8

$children = [Collections.Generic.List[object]]::new()
function Start-TestService([string]$exe, [string[]]$arguments) {
    $info = [Diagnostics.ProcessStartInfo]::new()
    $info.FileName = $exe
    $info.UseShellExecute = $false
    $info.CreateNoWindow = $true
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    foreach ($argument in $arguments) { $info.ArgumentList.Add($argument) }
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $info
    [void]$process.Start()
    $entry = @{Process=$process; Output=$process.StandardOutput.ReadToEndAsync(); Error=$process.StandardError.ReadToEndAsync()}
    $children.Add($entry)
    return $process
}
try {
    & $nats -t -c $natsConfig
    if ($LASTEXITCODE -ne 0) { throw 'NATS 配置校验失败' }
    $natsProcess = Start-TestService $nats @('-c',$natsConfig)
    $etcdProcess = Start-TestService $etcd @('--config-file',$etcdConfig)
    $deadline = [DateTime]::UtcNow.AddSeconds(20)
    do {
        foreach ($child in $children) {
            if ($child.Process.HasExited) { throw ('服务提前退出：'+$child.Error.Result) }
        }
        try { $health = Invoke-RestMethod 'http://127.0.0.1:12379/health' -TimeoutSec 2 } catch { $health = $null }
        if ($health.health -eq 'true') { break }
        Start-Sleep -Milliseconds 200
    } while ([DateTime]::UtcNow -lt $deadline)
    if ($health.health -ne 'true') { throw 'etcd 未在期限内就绪，请查看 .runtime/local-services/etcd.log' }

    $client = [Net.Sockets.TcpClient]::new()
    try {
        $client.ReceiveTimeout = 2000
        $client.SendTimeout = 2000
        $client.Connect('127.0.0.1',14222)
        $stream = $client.GetStream()
        $reader = [IO.StreamReader]::new($stream)
        if (-not $reader.ReadLine().StartsWith('INFO ')) { throw 'NATS 未返回 INFO' }
        $bytes = [Text.Encoding]::ASCII.GetBytes("CONNECT {`"verbose`":false}`r`nPING`r`n")
        $stream.Write($bytes,0,$bytes.Length)
        if ($reader.ReadLine() -ne 'PONG') { throw 'NATS 未返回 PONG' }
    } finally { $client.Dispose() }
    & $etcdctl --endpoints=http://127.0.0.1:12379 --dial-timeout=2s --command-timeout=3s endpoint health
    if ($LASTEXITCODE -ne 0) { throw 'etcdctl 健康验证失败' }
    Write-Output 'PASS: NATS PING/PONG；etcd HTTP 与客户端健康检查通过'
    if ($Run) { & $Run }
} finally {
    foreach ($child in $children) {
        # 这是短期基础服务测试，不宣称强制退出是生产环境的优雅停机。
        if (-not $child.Process.HasExited) { $child.Process.Kill(); $child.Process.WaitForExit() }
        $child.Process.Dispose()
    }
}
