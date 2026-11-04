param([string]$OutDir = "evidence/foundation/windows/run")

# Run from the repository root on Windows, after the operator transfers the
# committed revision. No result from this script is supplied by cross-building.
$ErrorActionPreference = "Stop"
if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw "This audition requires actual Windows execution."
}
$env:CGO_ENABLED = "0"
# .NET Framework's Process builds each fixture's stdin writer from the console
# input encoding and flushes its preamble at start. With code page 65001 that
# is a UTF-8 byte order mark, so the fixture's first console line arrived as
# "﻿quit" and was rejected (observed 2026-11-04). Use UTF-8 without one.
[Console]::InputEncoding = New-Object Text.UTF8Encoding($false)
$out = [IO.Path]::GetFullPath($OutDir)
$scratch = Join-Path $env:TEMP "dfx2-foundation-audition"
New-Item -ItemType Directory -Force $out, $scratch | Out-Null
$binary = Join-Path $scratch "lifetime.exe"
$driver = Join-Path $scratch "ctrlbreak.exe"
$utf8 = New-Object Text.UTF8Encoding($false)
function Write-Utf8([string]$Path, [string]$Text) {
    [IO.File]::WriteAllText($Path, $Text, $utf8)
}
function Invoke-Go([string]$Name, [string[]]$GoArgs) {
    # Windows PowerShell can turn native stderr into error records even for a
    # successful command. Retain it and judge the actual process exit code.
    $savedPreference = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        $text = (& go @GoArgs 2>&1 | Out-String)
        $code = $LASTEXITCODE
    } finally { $ErrorActionPreference = $savedPreference }
    Write-Utf8 (Join-Path $out ($Name + ".log")) $text
    if ($code -ne 0) { throw "go $GoArgs failed ($code): $text" }
}

# Portable and ABI tests execute natively before any GUI audition.
Invoke-Go "tests" @("test", "./...", "-count=1", "-timeout=120s")
Invoke-Go "vet" @("vet", "./...")
Invoke-Go "build" @("build", "-o", $binary, "./examples/lifetime")
# The console-signal driver is its own module (see ctrlbreak/main.go).
Invoke-Go "build-ctrlbreak" @("build", "-C", "evidence/foundation/windows/ctrlbreak", "-o", $driver, ".")
$hostInfo = [ordered]@{
    revision = (& git rev-parse HEAD | Out-String).Trim()
    worktree = (& git status --short | Out-String).Trim()
    go = (& go version | Out-String).Trim()
    cgo_enabled = $env:CGO_ENABLED
    os = (Get-CimInstance Win32_OperatingSystem | Select-Object Caption, Version, OSArchitecture)
    session = $env:SESSIONNAME
    powershell = $PSVersionTable.PSVersion.ToString()
    process64 = [Environment]::Is64BitProcess
    display = @(Get-CimInstance Win32_VideoController | Select-Object Name, CurrentHorizontalResolution, CurrentVerticalResolution)
    observed_at = (Get-Date).ToString("o")
    dpi_variation = "not tested by this script; record operator observation separately"
}
Write-Utf8 (Join-Path $out "host.json") ($hostInfo | ConvertTo-Json -Depth 8)

function Start-Fixture([string[]]$Arguments, [string]$FileName = $binary, [bool]$OwnConsole = $false) {
    $info = New-Object Diagnostics.ProcessStartInfo
    $info.FileName = $FileName
    # Fixture arguments are fixed words/durations from the case list; the one
    # path (the driver's -fixture) is quoted by the case.
    $info.Arguments = $Arguments -join " "
    $info.UseShellExecute = $false
    # CREATE_NO_WINDOW gives the process a hidden console of its own, which the
    # signal driver needs so GenerateConsoleCtrlEvent reaches its child.
    $info.CreateNoWindow = $OwnConsole
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    $info.RedirectStandardInput = $true
    $info.StandardOutputEncoding = [Text.Encoding]::UTF8
    $info.StandardErrorEncoding = [Text.Encoding]::UTF8
    $p = New-Object Diagnostics.Process
    $p.StartInfo = $info
    [void]$p.Start()
    return $p
}

function Save-And-Check($Process, $StdoutTask, $StderrTask, [string]$Name, [string]$Prefix, [int]$Expected) {
    $timedOut = -not $Process.WaitForExit(20000)
    if ($timedOut) { $Process.Kill(); $Process.WaitForExit() }
    $stdout = $Prefix + $StdoutTask.GetAwaiter().GetResult()
    $stderr = $StderrTask.GetAwaiter().GetResult()
    Write-Utf8 (Join-Path $out ($Name + ".jsonl")) $stdout
    Write-Utf8 (Join-Path $out ($Name + ".stderr.log")) $stderr
    $events = @($stdout -split "`n" | Where-Object { $_.Trim() } | ForEach-Object { $_ | ConvertFrom-Json })
    $returns = @($events | Where-Object event -eq "return")
    $drains = @($events | Where-Object event -eq "drained")
    $failures = New-Object 'Collections.Generic.List[string]'
    if ($timedOut -or $Process.ExitCode -ne $Expected) { $failures.Add("exit=$($Process.ExitCode), expected=$Expected, timeout=$timedOut") }
    if ($returns.Count -eq 0 -or $drains.Count -eq 0) { $failures.Add("missing return/drained events") }
    else {
        $last = $drains[-1]
        foreach ($field in @("windows", "tasks", "queued", "native_completions", "native_windows", "native_retired", "dispatch_targets")) {
            if ($null -eq $last.PSObject.Properties[$field] -or $last.$field -ne 0) { $failures.Add("outstanding or missing $field") }
        }
        if (-not $last.terminal -or $last.release_pending) { $failures.Add("release not complete") }
    }
    if ($Name -eq "failure" -and ($returns.Count -eq 0 -or $returns[-1].error -notlike "*controlled lifetime task failure*")) { $failures.Add("task failure lost") }
    if ($Name -eq "cancel" -and ($returns.Count -eq 0 -or $returns[-1].error -notlike "*context canceled*")) { $failures.Add("cancellation not observed") }
    if ($Name -like "idle-*") {
        $idle = @($events | Where-Object event -eq "idle-measurement")
        if ($idle.Count -eq 0 -or $idle[-1].wakes -ne 0 -or $idle[-1].dispatches -ne 0) { $failures.Add("application woke while paused") }
    }
    if ($Name -like "lifecycle-*") {
        $independent = @($events | Where-Object event -eq "verified-independent")
        if ($independent.Count -eq 0 -or $independent[-1].after -le $independent[-1].before) { $failures.Add("independent window updates absent") }
        if ($Name -eq "lifecycle-resident" -and @($events | Where-Object event -eq "verified-reopen").Count -eq 0) { $failures.Add("resident reopening absent") }
    }
    if ($Name -eq "create-close" -and @($events | Where-Object event -eq "verified-create-close").Count -eq 0) { $failures.Add("creation-close hook absent") }
    if ($Name -eq "signal") {
        if (@($events | Where-Object event -eq "ctrl-break-sent").Count -eq 0) { $failures.Add("console control event not delivered") }
        if ($returns.Count -eq 0 -or $returns[-1].error -notlike "*context canceled*") { $failures.Add("console control event not observed as cancellation") }
    }
    $result = [ordered]@{ name = $Name; exit_code = $Process.ExitCode; timed_out = $timedOut; events = $events.Count; failures = @($failures.ToArray()) }
    return $result
}

$results = New-Object 'Collections.Generic.List[object]'
foreach ($case in @(
    @{ name="lifecycle-window"; args=@("-script", "lifecycle"); expected=0 },
    @{ name="lifecycle-resident"; args=@("-resident", "-script", "lifecycle"); expected=0 },
    @{ name="idle-window"; args=@("-script", "idle"); expected=0 },
    @{ name="idle-resident"; args=@("-resident", "-script", "idle"); expected=0 },
    @{ name="failure"; args=@("-script", "failure"); expected=1 },
    @{ name="cancel"; args=@("-script", "cancel"); expected=0 },
    @{ name="create-close"; args=@("-script", "create-close"); expected=0 },
    # Default mode under the driver: a CTRL_BREAK_EVENT generated through the
    # console subsystem after both windows have applied updates. Not a keyboard
    # Ctrl+C, console close, logoff or shutdown; those remain the operator's.
    @{ name="signal"; file=$driver; ownConsole=$true; args=@("-fixture", ('"' + $binary + '"')); expected=0 }
)) {
    $file = $binary
    if ($case.ContainsKey("file")) { $file = $case.file }
    $p = Start-Fixture $case.args $file ([bool]$case.ownConsole)
    $stdoutTask = $p.StandardOutput.ReadToEndAsync()
    $stderrTask = $p.StandardError.ReadToEndAsync()
    $result = Save-And-Check $p $stdoutTask $stderrTask $case.name "" $case.expected
    $p.Dispose()
    $results.Add($result)
    Write-Host ($result | ConvertTo-Json -Compress)
}

# Hold a real resident instance open while inspecting its loaded DLLs and
# descendants. Do not race short lifecycle scripts that can already have exited.
$p = Start-Fixture @("-resident", "-pause-after", "500ms")
$stderrTask = $p.StandardError.ReadToEndAsync()
$prefix = ""
$stdoutTask = $null
$probeSaved = $false
try {
    $watch = [Diagnostics.Stopwatch]::StartNew()
    do {
        $lineTask = $p.StandardOutput.ReadLineAsync()
        if (-not $lineTask.Wait([Math]::Max(1, 20000 - [int]$watch.ElapsedMilliseconds))) { throw "fixture startup timed out" }
        $line = $lineTask.GetAwaiter().GetResult()
        if ($null -eq $line) { throw "fixture exited before opening B" }
        $prefix += $line + "`n"
        $event = $line | ConvertFrom-Json
    } until ($event.event -eq "opened" -and $event.window -eq "B")
    $stdoutTask = $p.StandardOutput.ReadToEndAsync()
    $modules = @((Get-Process -Id $p.Id).Modules | ForEach-Object FileName)
    $descendants = New-Object 'Collections.Generic.List[object]'
    $pending = New-Object 'Collections.Generic.Queue[int]'
    $pending.Enqueue($p.Id)
    while ($pending.Count -gt 0) {
        $parentId = $pending.Dequeue()
        foreach ($child in @(Get-CimInstance Win32_Process -Filter "ParentProcessId=$parentId")) {
            $descendants.Add([ordered]@{ name=$child.Name; pid=$child.ProcessId; parent=$child.ParentProcessId })
            $pending.Enqueue([int]$child.ProcessId)
        }
    }
    $browserModules = @($modules | Where-Object { $_ -match "(?i)webview2|embeddedbrowser|msedge|chromium" })
    $browserChildren = @($descendants | Where-Object { $_.name -match "(?i)webview|msedge|chrome|chromium|webkit" })
    Write-Utf8 (Join-Path $out "process-inspection.json") (@{ modules=$modules; descendants=@($descendants.ToArray()); browser_modules=$browserModules; browser_children=$browserChildren } | ConvertTo-Json -Depth 8)
    $p.StandardInput.WriteLine("quit")
    $result = Save-And-Check $p $stdoutTask $stderrTask "inspection" $prefix 0
    $probeSaved = $true
    if ($browserModules.Count -gt 0 -or $browserChildren.Count -gt 0) { $result.failures += "browser runtime/process detected" }
    $results.Add($result)
    Write-Host ($result | ConvertTo-Json -Compress)
} finally {
    if (-not $p.HasExited) { $p.Kill(); $p.WaitForExit() }
    if (-not $probeSaved) {
        $remaining = ""
        if ($null -ne $stdoutTask) { $remaining = $stdoutTask.GetAwaiter().GetResult() }
        Write-Utf8 (Join-Path $out "inspection.jsonl") ($prefix + $remaining)
        Write-Utf8 (Join-Path $out "inspection.stderr.log") ($stderrTask.GetAwaiter().GetResult())
    }
    $p.Dispose()
}

Write-Utf8 (Join-Path $out "summary.json") (@{ host=$hostInfo; results=@($results.ToArray()); manual_audition="pending: native close/resize, resident console reopen, keyboard Ctrl+C, and DPI observation" } | ConvertTo-Json -Depth 10)
if (@($results | Where-Object { $_.failures.Count -gt 0 }).Count -gt 0) { throw "Native audition failures: see $out" }
Write-Host "Automated Windows matrix passed. Manual controls/keyboard-signal/DPI observation still required."
