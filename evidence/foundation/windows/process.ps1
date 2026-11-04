# Shared by the audition and timeout regression; compatible with PowerShell 5.1.
# Kill the tree while the parent is still alive. Killing the parent first loses
# the relationship needed by taskkill /T and can leave inherited pipes open.
function Stop-FixtureTree($Process, [int]$WaitMs = 3000) {
    if ($Process.HasExited) { return }
    $killer = New-Object Diagnostics.Process
    try {
        $killer.StartInfo.FileName = Join-Path $env:SystemRoot "System32\taskkill.exe"
        $killer.StartInfo.Arguments = "/PID $($Process.Id) /T /F"
        $killer.StartInfo.UseShellExecute = $false
        $killer.StartInfo.CreateNoWindow = $true
        [void]$killer.Start()
        if (-not $killer.WaitForExit($WaitMs)) {
            $killer.Kill()
            "process-tree termination command timed out"
        } elseif ($killer.ExitCode -ne 0) {
            "process-tree termination failed: exit=$($killer.ExitCode)"
        }
    } catch {
        "process-tree termination failed: $_"
    } finally {
        $killer.Dispose()
    }
    if (-not $Process.WaitForExit($WaitMs)) {
        "fixture process $($Process.Id) survived termination"
    }
}

function Read-FixtureOutput($Task, [string]$Stream, [int]$WaitMs = 2000) {
    if ($null -eq $Task) { return @{ text = ""; failure = "" } }
    try {
        if (-not $Task.Wait($WaitMs)) {
            return @{ text = ""; failure = "$Stream collection timed out; output incomplete" }
        }
        return @{ text = $Task.GetAwaiter().GetResult(); failure = "" }
    } catch {
        return @{ text = ""; failure = "$Stream collection failed; output incomplete: $_" }
    }
}

function Complete-Fixture($Process, $StdoutTask, $StderrTask, [int]$TimeoutMs = 20000) {
    $timedOut = -not $Process.WaitForExit($TimeoutMs)
    $failures = New-Object 'Collections.Generic.List[string]'
    if ($timedOut) {
        foreach ($problem in @(Stop-FixtureTree $Process)) { $failures.Add($problem) }
    }
    # Never await EOF indefinitely: a surviving descendant may still own a pipe,
    # even after its parent exits. A collection failure must fail the audition.
    $stdout = Read-FixtureOutput $StdoutTask "stdout"
    $stderr = Read-FixtureOutput $StderrTask "stderr"
    foreach ($capture in @($stdout, $stderr)) {
        if ($capture.failure) { $failures.Add($capture.failure) }
    }
    $code = $null
    if ($Process.HasExited) { $code = $Process.ExitCode }
    return [pscustomobject]@{
        timed_out = $timedOut; exit_code = $code
        stdout = $stdout.text; stderr = $stderr.text
        cleanup_failures = @($failures.ToArray())
    }
}
