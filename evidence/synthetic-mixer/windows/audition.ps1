param(
    [string]$OutDir = "evidence/synthetic-mixer/windows/run",
    [switch]$SkipFoundation
)

# Run from the repository root on Windows, in a connected interactive desktop
# session, after the operator transfers the committed revision. See README.md
# beside this script. No result here is supplied by cross-building.
$ErrorActionPreference = "Stop"
if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw "This audition requires actual Windows execution."
}
. (Join-Path $PSScriptRoot "..\..\foundation\windows\process.ps1")
$env:CGO_ENABLED = "0"
# Without this, .NET Framework prefixes each fixture's stdin with a UTF-8 byte
# order mark under code page 65001 (recorded in the foundation's Windows record).
[Console]::InputEncoding = New-Object Text.UTF8Encoding($false)

$out = [IO.Path]::GetFullPath($OutDir)
$captures = Join-Path $out "captures"
$snapshots = Join-Path $out "snapshots"
$scratch = Join-Path $env:TEMP "dfx2-mixer-audition"
New-Item -ItemType Directory -Force $out, $captures, $snapshots, $scratch | Out-Null
$mixer = Join-Path $scratch "mixer.exe"
$desk = Join-Path $scratch "desk.exe"
$ctrlbreak = Join-Path $scratch "ctrlbreak.exe"
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

Invoke-Go "tests" @("test", "./...", "-count=1", "-timeout=300s")
Invoke-Go "tests-win32-verbose" @("test", "./internal/platform/win32", "-count=1", "-v")
Invoke-Go "tests-repeated" @("test", "./internal/appcore", "./internal/platform/win32", "-count=50", "-timeout=300s")
Invoke-Go "vet" @("vet", "./...")
Invoke-Go "build" @("build", "-o", $mixer, "./examples/mixer")
# The helpers are their own modules, so the root module never builds them.
Invoke-Go "build-desk" @("build", "-C", "evidence/synthetic-mixer/windows/desk", "-o", $desk, ".")
Invoke-Go "build-ctrlbreak" @("build", "-C", "evidence/foundation/windows/ctrlbreak", "-o", $ctrlbreak, ".")

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
    dpi_variation = "not exercised by this script; first_surface records the DPI scale of each run. Record monitor transitions separately."
}
Write-Utf8 (Join-Path $out "host.json") ($hostInfo | ConvertTo-Json -Depth 8)

function Start-Fixture([string[]]$Arguments, [string]$FileName, [bool]$OwnConsole = $false) {
    $info = New-Object Diagnostics.ProcessStartInfo
    $info.FileName = $FileName
    $info.Arguments = $Arguments -join " "
    $info.UseShellExecute = $false
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

function Select-Events($Events, [string]$Name) {
    # The comma keeps a one-element result an array: under Windows PowerShell 5.1
    # a lone custom object has no Count and cannot be indexed.
    return ,@($Events | Where-Object { $_.event -eq $Name })
}

function Add-Failure($Failures, [bool]$Condition, [string]$Message) {
    if ($Condition) { $Failures.Add($Message) }
}

function Test-Quiet($Failures, $Quiet) {
    if ($null -eq $Quiet) { $Failures.Add("no quiet measurement"); return }
    foreach ($field in @("frames", "frame_timers", "wakes", "dispatches", "presents", "animating")) {
        Add-Failure $Failures ($Quiet.$field -ne 0) "settled window not quiet: $field=$($Quiet.$field)"
    }
}

# Judges one case's events. Observed numbers stay in the retained log; these
# are the contract's pass conditions, not performance promises.
function Test-Case([string]$Name, $Events, $Failures) {
    $quiet = @(Select-Events $Events "quiet")
    $lastQuiet = $null
    if ($quiet.Count -gt 0) { $lastQuiet = $quiet[-1] }
    switch -Wildcard ($Name) {
        "animate" {
            Test-Quiet $Failures $lastQuiet
            $e = Select-Events $Events "animating"
            Add-Failure $Failures ($e.Count -eq 0 -or $e[-1].frames -lt 10) "animation did not render"
        }
        "resize" {
            Test-Quiet $Failures $lastQuiet
            $steps = Select-Events $Events "resized"
            Add-Failure $Failures ($steps.Count -ne 4) "resize steps missing"
            foreach ($e in $steps) {
                $size = $e.first_surface.Split("@")
                $scale = [double]::Parse($size[1].Split(" ")[0], [Globalization.CultureInfo]::InvariantCulture)
                $want = @($e.requested.Split("x") | ForEach-Object { [Math]::Round([double]$_ * $scale) })
                $got = $size[0].Split("x")
                Add-Failure $Failures ([double]$got[0] -ne $want[0] -or [double]$got[1] -ne $want[1]) "surface $($e.first_surface) for requested $($e.requested)"
            }
        }
        "minimize" {
            Test-Quiet $Failures $lastQuiet
            $during = Select-Events $Events "minimized"
            $later = Select-Events $Events "minimized-later"
            $restored = Select-Events $Events "restored"
            if ($during.Count -eq 0 -or $later.Count -eq 0 -or $restored.Count -eq 0) {
                $Failures.Add("minimize steps missing")
            } else {
                Add-Failure $Failures ($during[-1].first_surface -notlike "*visible=false*") "minimized window reported visible"
                Add-Failure $Failures ($later[-1].first_frames -ne $during[-1].first_frames) "frames continued while minimized"
                Add-Failure $Failures ($later[-1].frame_timers -ne $during[-1].frame_timers) "deadline timers continued while minimized"
                Add-Failure $Failures ($restored[-1].first_surface -notlike "*visible=true*") "restored window not visible"
                Add-Failure $Failures ($restored[-1].first_frames -le $later[-1].first_frames) "restored window did not render"
            }
        }
        "close-sample" {
            $e = Select-Events $Events "close-sample"
            Add-Failure $Failures ($e.Count -eq 0 -or $e[-1].accepted -eq 0 -or $e[-1].refused_after_close -eq 0) "close did not race live invalidation"
        }
        "reopen" {
            Add-Failure $Failures ((Select-Events $Events "reopen-cycle").Count -ne 5) "reopen cycles missing"
        }
        "two" {
            Test-Quiet $Failures $lastQuiet
            $e = Select-Events $Events "one-running"
            Add-Failure $Failures ($e.Count -eq 0 -or $e[-1].frames_after_close -eq 0) "remaining window stopped"
        }
        "expose" {
            Test-Quiet $Failures $lastQuiet
            $e = Select-Events $Events "exposure"
            if ($e.Count -eq 0) { $Failures.Add("no exposure measurement") }
            else {
                Add-Failure $Failures ($e[-1].frames -ne 0 -or $e[-1].presents -ne 0) "exposure evaluated the view"
                Add-Failure $Failures ($e[-1].paints -eq 0) "exposure painted nothing"
            }
        }
        "large" {
            Test-Quiet $Failures $lastQuiet
            $steps = Select-Events $Events "large-size"
            Add-Failure $Failures ($steps.Count -ne 5) "large image steps missing"
            Test-LargeImages $steps (Select-Events $Events "snapshot") $Failures
            Add-Failure $Failures ((Select-Events $Events "driver-capture").Count -ne 5) "large image captures missing"
        }
        "fonts" {
            Add-Failure $Failures ((Select-Events $Events "font-coverage").Count -ne 7) "font samples missing"
        }
        "signal" {
            Add-Failure $Failures ((Select-Events $Events "ctrl-break-sent").Count -eq 0) "console control event not delivered"
            Test-SignalProgress $Events $Failures
            $r = Select-Events $Events "return"
            Add-Failure $Failures ($r.Count -eq 0 -or $r[-1].error -notlike "*context canceled*") "console control event not observed as cancellation"
        }
        "modal-move" { Test-ModalProgress $Events $Failures $lastQuiet }
        "modal-size" { Test-ModalProgress $Events $Failures $lastQuiet }
        "modal-idle" {
            Test-Quiet $Failures $lastQuiet
            $e = Select-Events $Events "modal-quiet"
            if ($e.Count -eq 0) { $Failures.Add("no quiet measurement inside the loop") }
            else {
                Add-Failure $Failures (-not $e[-1].modal_active) "loop ended before the quiet measurement finished"
                foreach ($field in @("frames", "modal_timers", "modal_turns", "wakes", "dispatches")) {
                    Add-Failure $Failures ($e[-1].$field -ne 0) "idle loop not quiet: $field=$($e[-1].$field)"
                }
            }
            $post = Select-Events $Events "modal-post-after-idle"
            Add-Failure $Failures ($post.Count -eq 0 -or -not $post[-1].modal_active) "post after idle did not run inside the loop"
            $resume = Select-Events $Events "modal-resume"
            Add-Failure $Failures ($resume.Count -eq 0 -or $resume[-1].frames -eq 0) "feed did not render after idle"
        }
        "modal-close-self" {
            $e = Select-Events $Events "modal-closed"
            Add-Failure $Failures ($e.Count -eq 0 -or $e[-1].remaining_frames -eq 0) "remaining window stopped after the dragged window closed"
            Add-Failure $Failures ($e.Count -gt 0 -and $e[-1].modal_active) "loop still active after its window was destroyed"
            Test-Quiet $Failures $lastQuiet
        }
        "modal-close-other" {
            $e = Select-Events $Events "modal-closed"
            Add-Failure $Failures ($e.Count -eq 0 -or $e[-1].remaining_frames -eq 0) "dragged window stopped after the other closed"
            Add-Failure $Failures ($e.Count -gt 0 -and -not $e[-1].modal_active_at_done) "other window's Done waited for the loop to end"
            Test-Quiet $Failures $lastQuiet
        }
        "modal-quit" {
            Add-Failure $Failures ((Select-Events $Events "modal-quit-sent").Count -eq 0) "quit not sent inside the loop"
        }
    }
}

function Get-PngSize([string]$Path) {
    $stream = [IO.File]::OpenRead($Path)
    try {
        $b = New-Object byte[] 24
        if ($stream.Read($b, 0, 24) -ne 24) { return $null }
    } finally { $stream.Dispose() }
    if ($b[0] -ne 0x89 -or $b[1] -ne 0x50 -or $b[2] -ne 0x4E -or $b[3] -ne 0x47 -or $b[12] -ne 0x49 -or $b[13] -ne 0x48 -or $b[14] -ne 0x44 -or $b[15] -ne 0x52) { return $null }
    $width = $b[16] * 16777216 + $b[17] * 65536 + $b[18] * 256 + $b[19]
    $height = $b[20] * 16777216 + $b[21] * 65536 + $b[22] * 256 + $b[23]
    return "${width}x${height}"
}

# The same rules as large_failures in linux/drive.py. The host may clamp a
# request, so each step's retained image is compared with its observed surface.
function Test-LargeImages($Steps, $Snapshots, $Failures) {
    if ($Steps.Count -eq 0) { return }
    $sizes = @($Steps | ForEach-Object { [double]$_.bytes })
    for ($i = 1; $i -lt $sizes.Count; $i++) {
        Add-Failure $Failures ($sizes[$i] -lt $sizes[$i - 1]) "large surface shrank: $($sizes -join ', ')"
    }
    Add-Failure $Failures ($sizes[-1] -le $sizes[0]) "missing coverage: large surface did not grow: $($sizes -join ', ')"
    $largest = ($sizes | Measure-Object -Maximum).Maximum
    # The spike's failures were frames of 1.9-3.0 MB.
    Add-Failure $Failures ($largest -le 3000000) "missing coverage: largest surface $largest bytes is not beyond the spike's failure sizes"
    foreach ($e in $Steps) {
        $written = @($Snapshots | Where-Object { $_.path -and (Split-Path -Leaf $_.path) -eq $e.snapshot })
        if ($written.Count -eq 0) { $Failures.Add("large step $($e.requested): no snapshot written"); continue }
        $got = $null
        try { $got = Get-PngSize $written[-1].path } catch { $Failures.Add("large step $($e.requested): unreadable snapshot: $_"); continue }
        $surface = $e.first_surface.Split("@")[0]
        if ($got -ne $surface) { $Failures.Add("large step $($e.requested): snapshot '$got' does not match surface $($e.first_surface)") }
    }
}

# Before the console event, each window must report three updates whose applied
# and frame counts strictly rise. The driver only counts records; this reads them.
function Test-SignalProgress($Events, $Failures) {
    $seen = @{ A = @{ updates = 0; applied = 0; frames = 0 }; B = @{ updates = 0; applied = 0; frames = 0 } }
    foreach ($e in $Events) {
        if ($e.event -eq "ctrl-break-sent") { break }
        if ($e.event -ne "update") { continue }
        $last = $seen[[string]$e.window]
        if ($null -eq $last) { $Failures.Add("update from unknown window '$($e.window)'"); continue }
        Add-Failure $Failures ($null -eq $e.applied -or $e.applied -le $last.applied) "window $($e.window) applied $($e.applied) did not rise past $($last.applied)"
        Add-Failure $Failures ($null -eq $e.frames -or $e.frames -le $last.frames) "window $($e.window) frames $($e.frames) did not rise past $($last.frames)"
        $last.updates++
        $last.applied = $e.applied
        $last.frames = $e.frames
    }
    foreach ($name in @("A", "B")) {
        Add-Failure $Failures ($seen[$name].updates -ne 3) "window $name reported $($seen[$name].updates) updates before the console event, not 3"
    }
}

function Test-ModalProgress($Events, $Failures, $Quiet) {
    $s = Select-Events $Events "modal-summary"
    if ($s.Count -eq 0) { $Failures.Add("no modal summary"); return }
    $e = $s[-1]
    Add-Failure $Failures ($e.seconds -lt 10) "only $($e.seconds) whole seconds inside the loop; ten are required"
    foreach ($field in @("least_frames_A", "least_frames_B", "least_titles_A", "least_titles_B")) {
        Add-Failure $Failures ($e.$field -eq 0) "a second of the loop passed with $field=0"
    }
    Add-Failure $Failures ($e.modal_turns -eq 0) "no modal turns"
    Test-Quiet $Failures $Quiet
}

function Save-And-Check($Process, $StdoutTask, $StderrTask, [string]$Name, [string]$Prefix, [int]$Expected, [int]$TimeoutMs) {
    $completion = Complete-Fixture $Process $StdoutTask $StderrTask $TimeoutMs
    $timedOut = $completion.timed_out
    $stdout = $Prefix + $completion.stdout
    Write-Utf8 (Join-Path $out ($Name + ".jsonl")) $stdout
    Write-Utf8 (Join-Path $out ($Name + ".stderr.log")) $completion.stderr
    Write-Utf8 (Join-Path $out ($Name + ".completion.json")) ($completion | Select-Object timed_out, exit_code, cleanup_failures | ConvertTo-Json -Depth 4)
    if ($timedOut) {
        # A driver killed mid-drag cannot release the button itself.
        & $desk -release | Out-Null
    }
    $events = @($stdout -split "`n" | Where-Object { $_.Trim() } | ForEach-Object { $_ | ConvertFrom-Json })
    $failures = New-Object 'Collections.Generic.List[string]'
    foreach ($problem in $completion.cleanup_failures) { $failures.Add($problem) }
    if ($timedOut -or $completion.exit_code -ne $Expected) { $failures.Add("exit=$($completion.exit_code), expected=$Expected, timeout=$timedOut") }
    if ($completion.stderr.Trim()) { $failures.Add("stderr not empty; see $Name.stderr.log") }
    $drains = Select-Events $events "drained"
    if ((Select-Events $events "return").Count -eq 0 -or $drains.Count -eq 0) {
        $failures.Add("missing return/drained events")
    } else {
        $last = $drains[-1]
        foreach ($field in @("windows", "tasks", "queued", "native_completions", "native_windows", "native_retired", "dispatch_targets", "paint_failures")) {
            if ($null -eq $last.PSObject.Properties[$field] -or $last.$field -ne 0) { $failures.Add("outstanding or missing $field") }
        }
        if (-not $last.terminal -or $last.release_pending) { $failures.Add("release not complete") }
    }
    foreach ($e in (Select-Events $events "driver-failed")) { $failures.Add("driver: $($e.step): $($e.error)") }
    foreach ($e in (Select-Events $events "driver-capture")) {
        # Black is what a blit that set nothing left behind in the spike.
        if ($e.black_fraction -gt 0.5) { $failures.Add("capture $($e.name) is $($e.black_fraction) black") }
        if ($e.background_fraction -lt 0.2) { $failures.Add("capture $($e.name) is $($e.background_fraction) background") }
    }
    Test-Case $Name $events $failures
    return [ordered]@{ name = $Name; exit_code = $completion.exit_code; timed_out = $timedOut; events = $events.Count; failures = @($failures.ToArray()) }
}

$results = New-Object 'Collections.Generic.List[object]'
$snapshotArgs = @("-snapshots", ('"' + $snapshots + '"'))
foreach ($case in @(
    # Runs first: a cold font index can delay this host's first frame.
    @{ name="fonts"; args=@("-script", "fonts") },
    @{ name="animate"; args=@("-script", "animate") + $snapshotArgs },
    @{ name="resize"; args=@("-script", "resize") + $snapshotArgs },
    @{ name="minimize"; args=@("-script", "minimize") },
    @{ name="expose"; args=@("-script", "expose") },
    @{ name="large"; args=@("-script", "large") + $snapshotArgs },
    @{ name="close-sample"; args=@("-script", "close-sample") },
    @{ name="reopen"; args=@("-script", "reopen") },
    @{ name="two"; args=@("-script", "two") },
    @{ name="modal-move"; args=@("-script", "modal-move") },
    @{ name="modal-size"; args=@("-script", "modal-size") },
    @{ name="modal-idle"; args=@("-script", "modal-idle") },
    @{ name="modal-close-self"; args=@("-script", "modal-close-self") },
    @{ name="modal-close-other"; args=@("-script", "modal-close-other") },
    @{ name="modal-quit"; args=@("-script", "modal-quit") }
)) {
    $arguments = @("-fixture", ('"' + $mixer + '"'), "-out", ('"' + $captures + '"'), "--") + $case.args
    $p = Start-Fixture $arguments $desk
    $stdoutTask = $p.StandardOutput.ReadToEndAsync()
    $stderrTask = $p.StandardError.ReadToEndAsync()
    $result = Save-And-Check $p $stdoutTask $stderrTask $case.name "" 0 90000
    $p.Dispose()
    $results.Add($result)
    Write-Host ($result | ConvertTo-Json -Compress)
}

# A console control event while two content windows render. ctrlbreak is the
# foundation's driver and needs a hidden console of its own.
$p = Start-Fixture @("-fixture", ('"' + $mixer + '"'), "--", "-script", "signal") $ctrlbreak $true
$stdoutTask = $p.StandardOutput.ReadToEndAsync()
$stderrTask = $p.StandardError.ReadToEndAsync()
$result = Save-And-Check $p $stdoutTask $stderrTask "signal" "" 0 40000
$p.Dispose()
$results.Add($result)
Write-Host ($result | ConvertTo-Json -Compress)

# Hold a real content window open while inspecting loaded DLLs and descendants.
$p = Start-Fixture @("-resident", "-paused") $mixer
$stderrTask = $p.StandardError.ReadToEndAsync()
$prefix = ""
$stdoutTask = $null
$probeSaved = $false
try {
    $p.StandardInput.WriteLine("open")
    $watch = [Diagnostics.Stopwatch]::StartNew()
    do {
        $lineTask = $p.StandardOutput.ReadLineAsync()
        if (-not $lineTask.Wait([Math]::Max(1, 30000 - [int]$watch.ElapsedMilliseconds))) { throw "fixture startup timed out" }
        $line = $lineTask.GetAwaiter().GetResult()
        if ($null -eq $line) { throw "fixture exited before opening a window" }
        $prefix += $line + "`n"
        $event = $line | ConvertFrom-Json
    } until ($event.event -eq "opened")
    $stdoutTask = $p.StandardOutput.ReadToEndAsync()
    Start-Sleep -Seconds 1
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
    $result = Save-And-Check $p $stdoutTask $stderrTask "inspection" $prefix 0 30000
    $probeSaved = $true
    if ($browserModules.Count -gt 0 -or $browserChildren.Count -gt 0) { $result.failures += "browser runtime/process detected" }
    $results.Add($result)
    Write-Host ($result | ConvertTo-Json -Compress)
} finally {
    if (-not $probeSaved) {
        $completion = Complete-Fixture $p $stdoutTask $stderrTask 0
        Write-Utf8 (Join-Path $out "inspection.jsonl") ($prefix + $completion.stdout)
        Write-Utf8 (Join-Path $out "inspection.stderr.log") $completion.stderr
        Write-Utf8 (Join-Path $out "inspection.completion.json") ($completion | Select-Object timed_out, exit_code, cleanup_failures | ConvertTo-Json -Depth 4)
    }
    $p.Dispose()
}

# The foundation's lifecycle matrix, because modal dispatch changed its context.
$foundation = "skipped"
if (-not $SkipFoundation) {
    $foundationDir = Join-Path $out "foundation"
    $savedPreference = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        & powershell.exe -NoProfile -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot "..\..\foundation\windows\audition.ps1") -OutDir $foundationDir 2>&1 | Out-String | ForEach-Object { Write-Utf8 (Join-Path $out "foundation.log") $_ }
        $code = $LASTEXITCODE
    } finally { $ErrorActionPreference = $savedPreference }
    $foundation = "exit $code; see foundation/ and foundation.log"
    $results.Add([ordered]@{ name = "foundation-matrix"; exit_code = $code; timed_out = $false; events = 0; failures = @(if ($code -ne 0) { "foundation matrix exit $code" }) })
    Write-Host "foundation matrix: $foundation"
}

Write-Utf8 (Join-Path $out "summary.json") (@{ host=$hostInfo; results=@($results.ToArray()); foundation=$foundation; manual_audition="pending: see README.md" } | ConvertTo-Json -Depth 10)
$failed = @($results | Where-Object { $_.failures.Count -gt 0 })
if ($failed.Count -gt 0) { throw "Native audition failures in $(@($failed | ForEach-Object { $_.name }) -join ', '): see $out" }
Write-Host "Automated Windows matrix passed. The manual audition in README.md is still required."
