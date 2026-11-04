param([string]$OutDir = "evidence/foundation/windows/timeout-run")

# A failure-path regression, separate from the successful GUI audition matrix.
$ErrorActionPreference = "Stop"
if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw "This regression requires Windows."
}
. (Join-Path $PSScriptRoot "process.ps1")
$env:CGO_ENABLED = "0"
$out = [IO.Path]::GetFullPath($OutDir)
$scratch = Join-Path $env:TEMP "dfx2-foundation-audition"
New-Item -ItemType Directory -Force $out, $scratch | Out-Null
$binary = Join-Path $scratch "stall.exe"
& go build -C (Join-Path $PSScriptRoot "ctrlbreak") -o $binary ./stall
if ($LASTEXITCODE -ne 0) { throw "could not build stalled fixture" }
$utf8 = New-Object Text.UTF8Encoding($false)

$p = New-Object Diagnostics.Process
$p.StartInfo.FileName = $binary
$p.StartInfo.UseShellExecute = $false
$p.StartInfo.CreateNoWindow = $true
$p.StartInfo.RedirectStandardOutput = $true
$p.StartInfo.RedirectStandardError = $true
$p.StartInfo.StandardOutputEncoding = [Text.Encoding]::UTF8
$p.StartInfo.StandardErrorEncoding = [Text.Encoding]::UTF8
$child = $null
$stderrTask = $null
$stdoutTask = $null
$prefix = ""
$started = $false
$failures = New-Object 'Collections.Generic.List[string]'
try {
    [void]$p.Start()
    $started = $true
    $stderrTask = $p.StandardError.ReadToEndAsync()
    $readyTask = $p.StandardOutput.ReadLineAsync()
    if (-not $readyTask.Wait(10000)) { throw "stalled child did not announce readiness" }
    $line = $readyTask.GetAwaiter().GetResult()
    $ready = $line | ConvertFrom-Json
    if ($ready.event -ne "stalled-child") { throw "unexpected fixture readiness: $line" }
    $prefix = $line + "`n"
    $stdoutTask = $p.StandardOutput.ReadToEndAsync()
    $child = Get-Process -Id $ready.pid
    # Keep a handle to the original child, rather than checking a reusable PID.
    $null = $child.Handle
    $watch = [Diagnostics.Stopwatch]::StartNew()
    $completion = Complete-Fixture $p $stdoutTask $stderrTask 200
    $watch.Stop()
    if (-not $completion.timed_out) { $failures.Add("stalled parent did not time out") }
    if ($completion.cleanup_failures.Count -ne 0) { $failures.Add("termination/output collection failed") }
    if (-not $p.HasExited -or -not $child.WaitForExit(3000)) { $failures.Add("parent or child survived") }
    if ($watch.ElapsedMilliseconds -gt 15000) { $failures.Add("completion exceeded bounded cleanup allowance") }
    if ($completion.stderr -notlike "*stalled child holds inherited stderr*") { $failures.Add("child stderr was not retained") }

    # Independently exercise the output deadline even if tree cleanup succeeded.
    $never = New-Object 'System.Threading.Tasks.TaskCompletionSource[string]'
    $watchOutput = [Diagnostics.Stopwatch]::StartNew()
    $capture = Read-FixtureOutput $never.Task "inherited-pipe" 50
    $watchOutput.Stop()
    if ($capture.failure -notlike "*timed out*" -or $watchOutput.ElapsedMilliseconds -gt 3000) {
        $failures.Add("unfinished output task was not bounded and reported")
    }
    $never.SetResult("")
    [IO.File]::WriteAllText((Join-Path $out "timeout.jsonl"), ($prefix + $completion.stdout), $utf8)
    [IO.File]::WriteAllText((Join-Path $out "timeout.stderr.log"), $completion.stderr, $utf8)
    $record = [ordered]@{
        revision = (& git rev-parse HEAD | Out-String).Trim()
        powershell = $PSVersionTable.PSVersion.ToString()
        elapsed_ms = $watch.ElapsedMilliseconds
        parent_exited = $p.HasExited; child_exited = $child.HasExited
        completion = $completion; unfinished_output = $capture
        failures = @($failures.ToArray())
    }
    [IO.File]::WriteAllText((Join-Path $out "summary.json"), ($record | ConvertTo-Json -Depth 6), $utf8)
    if ($failures.Count -ne 0) { throw ($failures -join "; ") }
    Write-Host "Timeout regression passed: parent and child exited, stderr retained, output wait bounded."
} finally {
    # Keep failure of the regression itself from leaving deliberately stuck work.
    if ($started -and -not $p.HasExited) { Stop-FixtureTree $p | Write-Warning }
    if ($null -ne $child) {
        if (-not $child.HasExited) { Stop-FixtureTree $child | Write-Warning }
        $child.Dispose()
    }
    $p.Dispose()
}
