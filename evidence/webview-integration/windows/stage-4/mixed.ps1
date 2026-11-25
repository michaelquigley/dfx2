# Windows mixed-view checks for webview stage 4, scripted through the mixer's
# console (stage-4.md, "Windows handoff", items 3 and 5). No input reaches the
# page: real mouse and keyboard remain the hands-on checklist. Each run starts
# the mixer with its own TEMP, writes console commands to its stdin and reads
# its JSON lines; the mixer's stdout and stderr are kept untouched in OutDir.
param(
    [string]$Mixer = (Join-Path $env:TEMP "dfx2-mixer.exe"),
    [string]$OutDir = "evidence/webview-integration/windows/stage-4/mixed"
)

$ErrorActionPreference = "Stop"
$out = [IO.Path]::GetFullPath($OutDir)
if (Test-Path -LiteralPath $out) { throw "output directory exists: $out" }
New-Item -ItemType Directory -Path $out | Out-Null
$utf8 = New-Object System.Text.UTF8Encoding $false

function Write-Json($path, $value) {
    [IO.File]::WriteAllText($path, (($value | ConvertTo-Json -Depth 8) + "`n"), $utf8)
}

# Read-Complete returns the file's complete lines, leaving a trailing partial
# line for the next read. The file is open for writing by the mixer.
function Read-Complete($path) {
    if (-not (Test-Path -LiteralPath $path)) { return @() }
    $fs = New-Object IO.FileStream($path, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::ReadWrite)
    try {
        $sr = New-Object IO.StreamReader($fs, $utf8)
        $text = $sr.ReadToEnd()
    } finally { $fs.Close() }
    if ($text.Length -eq 0) { return @() }
    $lines = $text -split "`n"
    if (-not $text.EndsWith("`n")) {
        if ($lines.Count -le 1) { return @() }
        $lines = $lines[0..($lines.Count - 2)]
    }
    return @($lines | ForEach-Object { $_.TrimEnd("`r") } | Where-Object { $_ -ne "" })
}

function Start-Run($name, $flags) {
    $tmp = Join-Path $out "tmp-$name"
    New-Item -ItemType Directory -Path $tmp | Out-Null
    $log = Join-Path $out "$name.jsonl"
    $err = Join-Path $out "$name.stderr"
    $psi = New-Object System.Diagnostics.ProcessStartInfo
    $psi.FileName = $env:ComSpec
    $psi.Arguments = "/d /c """"$Mixer"" $flags > ""$log"" 2> ""$err"""""
    $psi.UseShellExecute = $false
    $psi.RedirectStandardInput = $true
    $psi.CreateNoWindow = $true
    $psi.EnvironmentVariables["TEMP"] = $tmp
    $psi.EnvironmentVariables["TMP"] = $tmp
    $p = [System.Diagnostics.Process]::Start($psi)
    $p.StandardInput.AutoFlush = $true
    $run = [ordered]@{
        name = $name; flags = $flags; log = $log; err = $err; tmp = $tmp
        process = $p; cursor = 0; checks = @(); console = @(); mixer_pid = $null
        started = (Get-Date).ToString("o")
    }
    $deadline = (Get-Date).AddSeconds(10)
    while (-not $run.mixer_pid -and (Get-Date) -lt $deadline) {
        $child = Get-CimInstance Win32_Process -Filter "ParentProcessId=$($p.Id)" | Where-Object { $_.Name -like "dfx2-mixer*" }
        if ($child) { $run.mixer_pid = [int]$child.ProcessId } else { Start-Sleep -Milliseconds 100 }
    }
    Write-Host "== $name ($flags) mixer pid $($run.mixer_pid)"
    return $run
}

function Send($run, $command) {
    $run.console += [ordered]@{ time = (Get-Date).ToString("o"); command = $command }
    Write-Host "   > $command"
    $run.process.StandardInput.WriteLine($command)
}

function Check($run, $name, $ok, $detail) {
    $run.checks += [ordered]@{ check = $name; ok = [bool]$ok; detail = "$detail" }
    if ($ok) { Write-Host "   ok   $name" } else { Write-Host "   FAIL $name -- $detail" -ForegroundColor Red }
}

# Wait-Record returns the first record from index From (default: after the
# last match) satisfying the predicate, or $null after Timeout seconds.
function Wait-Record($run, [scriptblock]$predicate, $timeout = 15, $from = -1) {
    if ($from -lt 0) { $from = $run.cursor }
    $deadline = (Get-Date).AddSeconds($timeout)
    while ($true) {
        $lines = @(Read-Complete $run.log)   # one line would otherwise unroll to a string
        for ($i = $from; $i -lt $lines.Count; $i++) {
            try { $e = $lines[$i] | ConvertFrom-Json } catch { continue }
            if (& $predicate $e) { $run.cursor = $i + 1; return $e }
        }
        $from = $lines.Count
        if ((Get-Date) -gt $deadline) { return $null }
        Start-Sleep -Milliseconds 100
    }
}

function Ids($page) { return (@($page.channels | ForEach-Object { $_.id }) -join "") }

function Wait-Page($run, $window, [scriptblock]$pred, $what, $timeout = 15) {
    $match = { param($e) $e.event -eq "page" -and $e.window -eq $window -and (& $pred $e.page) }.GetNewClosure()
    $e = Wait-Record $run $match $timeout
    Check $run "$what (page line from $window)" ($e -ne $null) $(if ($e) { "order " + (Ids $e.page) + "; " + $e.page.commands } else { "no such page line within ${timeout}s" })
    return $e
}

function Fader($page, $id) { return ($page.channels | Where-Object { $_.id -eq $id } | Select-Object -First 1).fader }

function Probe($url) {
    try {
        $r = Invoke-WebRequest -Uri $url -UseBasicParsing -TimeoutSec 5
        return [ordered]@{ status = [int]$r.StatusCode; body = $r.Content }
    } catch { return [ordered]@{ status = 0; error = "$($_.Exception.Message)" } }
}

# Finish waits for the mixer to exit and checks the exit, the return record,
# stderr, browser descendants and temporary storage.
function Finish($run, $expectCanceled = 0) {
    $p = $run.process
    if (-not $p.WaitForExit(30000)) {
        Check $run "process exits after its last window closes" $false "still running after 30 s; sending quit"
        Send $run "quit"
        if (-not $p.WaitForExit(15000)) { & taskkill /PID $run.mixer_pid /T /F | Out-Null; $p.WaitForExit() }
    }
    $run.exit_code = $p.ExitCode
    Check $run "exit code 0" ($p.ExitCode -eq 0) "exit $($p.ExitCode)"
    $lines = @(Read-Complete $run.log)
    $records = @($lines | ForEach-Object { try { $_ | ConvertFrom-Json } catch { $null } } | Where-Object { $_ })
    $ret = $records | Where-Object { $_.event -eq "return" } | Select-Object -Last 1
    Check $run "return record with commands_conserved" ($ret -and $ret.commands_conserved -eq $true) $(if ($ret) { ($ret.commands | ConvertTo-Json -Compress) } else { "no return record" })
    if ($ret) {
        $run.commands = $ret.commands
        Check $run "canceled count is $expectCanceled" ($ret.commands.canceled -eq $expectCanceled) "canceled $($ret.commands.canceled)"
    }
    $edits = @($records | Where-Object { $_.event -eq "edit" }).Count
    $cmds = @($records | Where-Object { $_.event -eq "command" }).Count
    $run.edit_records = $edits; $run.command_records = $cmds
    $diag = @(); $other = @()
    if (Test-Path -LiteralPath $run.err) {
        foreach ($l in (Get-Content -LiteralPath $run.err)) {
            $t = $l.Trim()
            if ($t -eq "") { continue }
            if ($t.Contains("dfx2 shutdown outstanding")) { $diag += $t } else { $other += $t }
        }
    }
    $run.shutdown_diagnostics = $diag; $run.stderr = $other
    Check $run "stderr empty apart from the shutdown diagnostic" ($other.Count -eq 0) "$($other.Count) other line(s), $($diag.Count) diagnostic(s)"
    $deadline = (Get-Date).AddSeconds(10)
    do {
        $up = @(Get-CimInstance Win32_Process -Filter "Name='msedgewebview2.exe'" | Where-Object { $_.ParentProcessId -eq $run.mixer_pid })
        if ($up.Count -eq 0) { break }
        Start-Sleep -Milliseconds 250
    } while ((Get-Date) -lt $deadline)
    Check $run "no browser process survived the mixer" ($up.Count -eq 0) "$($up.Count) msedgewebview2 process(es) with the mixer as parent"
    $left = @(Get-ChildItem -LiteralPath $run.tmp -Force | ForEach-Object { $_.Name })
    $run.leftovers = $left
    Check $run "no temporary storage left" ($left.Count -eq 0) ($left -join ", ")
    if ($left.Count -eq 0) { Remove-Item -LiteralPath $run.tmp -Force }
    $run.finished = (Get-Date).ToString("o")
    $run.Remove("process")
    return $run
}

function Common($run, $mode) {
    $a = Wait-Record $run { param($e) $e.event -eq "opened" -and $e.window -eq "A" -and -not $e.kind } 20
    Check $run "native window A opened" ($a -ne $null) ""
    $b = Wait-Record $run { param($e) $e.event -eq "opened" -and $e.window -eq "B" -and $e.kind -eq "browser" -and $e.mode -eq $mode } 20
    Check $run "browser view B opened in mode $mode" ($b -ne $null) ""
    Wait-Page $run "B" { param($pg) (Ids $pg) -eq "abcdef" } "six channels a-f" 30 | Out-Null
    Send $run "gain a 0.25"
    Wait-Page $run "B" { param($pg) [math]::Abs((Fader $pg "a") - 0.25) -lt 1e-9 } "channel a's fader at 0.25 after an external gain" | Out-Null
    Send $run "reorder"
    Wait-Page $run "B" { param($pg) (Ids $pg) -eq "fedcba" } "reversed order after reorder" | Out-Null
    Send $run "remove c"
    Wait-Page $run "B" { param($pg) (Ids $pg) -eq "fedba" } "channel c gone after remove c" | Out-Null
    Send $run "insert c"
    Wait-Page $run "B" { param($pg) (Ids $pg) -eq "fedbac" } "channel c last after insert c" | Out-Null
    Send $run "delay 1500ms"
    Wait-Page $run "B" { param($pg) $pg.commands -match "device delay 1500 ms" } "commands text shows the 1500 ms delay" | Out-Null
    $lines = @(Read-Complete $run.log)
    $n = @($lines | Where-Object { $_ -match '"event":"(edit|command)"' }).Count
    Check $run "external changes and reconciliation submitted no edit or command" ($n -eq 0) "$n edit/command record(s) so far"
}

function Run-Mode($mode) {
    $run = Start-Run $mode "-browser $mode -audition -paused"
    $run.mode = $mode
    Common $run $mode
    switch ($mode) {
        "inprocess" {
            Send $run "close B"
            $c = Wait-Record $run { param($e) $e.event -eq "closed" -and $e.window -eq "B" -and $e.kind -eq "browser" }
            Check $run "browser view B closed first" ($c -ne $null) ""
            Send $run "gain b 0.75"
            $x = Wait-Record $run { param($e) $e.event -eq "external" -and $e.command -eq "gain b 0.75" } 5
            Check $run "the model still takes external changes with only the native window open" ($x -ne $null) ""
            Send $run "close A"
        }
        "loopback" {
            Send $run "close A"
            $c = Wait-Record $run { param($e) $e.event -eq "closed" -and $e.window -eq "A" }
            Check $run "native window A closed first" ($c -ne $null) ""
            Send $run "gain b 0.75"
            Wait-Page $run "B" { param($pg) [math]::Abs((Fader $pg "b") - 0.75) -lt 1e-9 } "browser view B still follows the model after the native close" | Out-Null
            Send $run "close B"
        }
        "attach" {
            $fs = Wait-Record $run { param($e) $e.event -eq "fixture-server" } 5 0
            Check $run "fixture-server line" ($fs -ne $null) $(if ($fs) { $fs.url } else { "" })
            $url = $fs.url
            $run.fixture_url = $url
            Send $run "close B"
            $c = Wait-Record $run { param($e) $e.event -eq "closed" -and $e.window -eq "B" -and $e.kind -eq "browser" }
            Check $run "attached view B closed" ($c -ne $null) ""
            $pr = Probe "$url/api/state?view=probe"
            $okBody = $false
            if ($pr.status -eq 200) { try { $okBody = ((($pr.body | ConvertFrom-Json).channels).Count -eq 6) } catch { $okBody = $false } }
            Check $run "fixture server answers 200 after its attached view closed" ($pr.status -eq 200 -and $okBody) ("status " + $pr.status)
            $run.probe_after_close = $pr.status
            Send $run "browser"
            $d = Wait-Record $run { param($e) $e.event -eq "opened" -and $e.window -eq "C" -and $e.kind -eq "browser" -and $e.mode -eq "attach" } 20
            Check $run "view C opened in attach mode" ($d -ne $null) ""
            Wait-Page $run "C" { param($pg) (Ids $pg) -eq "fedbac" -and [math]::Abs((Fader $pg "a") - 0.25) -lt 1e-9 } "view C shows the same model" 30 | Out-Null
            $servers = @((Read-Complete $run.log) | Where-Object { $_ -match '"event":"fixture-server"' }).Count
            Check $run "one fixture server for both attached views" ($servers -eq 1) "$servers fixture-server line(s)"
            Send $run "close A"
            Send $run "close C"
        }
    }
    $run = Finish $run
    if ($mode -eq "attach") {
        $pr = Probe "$($run.fixture_url)/api/state?view=probe"
        Check $run "fixture server stopped with the application" ($pr.status -eq 0) ("status " + $pr.status + " " + $pr.error)
        $run.probe_after_exit = $pr
    }
    return $run
}

function Run-Resident() {
    $run = Start-Run "resident" "-browser inprocess -resident -audition -paused"
    $run.mode = "inprocess"
    $a = Wait-Record $run { param($e) $e.event -eq "opened" -and $e.window -eq "A" -and -not $e.kind } 20
    Check $run "native window A opened" ($a -ne $null) ""
    $b = Wait-Record $run { param($e) $e.event -eq "opened" -and $e.window -eq "B" -and $e.kind -eq "browser" } 20
    Check $run "browser view B opened" ($b -ne $null) ""
    Wait-Page $run "B" { param($pg) (Ids $pg) -eq "abcdef" } "six channels a-f" 30 | Out-Null
    Send $run "close B"
    $c = Wait-Record $run { param($e) $e.event -eq "closed" -and $e.window -eq "B" }
    Check $run "browser view B closed" ($c -ne $null) ""
    Send $run "close A"
    $c = Wait-Record $run { param($e) $e.event -eq "closed" -and $e.window -eq "A" }
    Check $run "native window A closed" ($c -ne $null) ""
    Start-Sleep -Seconds 3
    Check $run "process stays with zero windows" (-not $run.process.HasExited) ""
    Send $run "open"
    $c = Wait-Record $run { param($e) $e.event -eq "opened" -and $e.window -eq "C" -and -not $e.kind } 20
    Check $run "open brings native window C" ($c -ne $null) ""
    Send $run "browser"
    $d = Wait-Record $run { param($e) $e.event -eq "opened" -and $e.window -eq "D" -and $e.kind -eq "browser" } 20
    Check $run "browser brings view D" ($d -ne $null) ""
    Wait-Page $run "D" { param($pg) (Ids $pg) -eq "abcdef" } "new page lines from D" 30 | Out-Null
    Send $run "gain a 0.4"
    Wait-Page $run "D" { param($pg) [math]::Abs((Fader $pg "a") - 0.4) -lt 1e-9 } "view D follows the shared model" | Out-Null
    Send $run "quit"
    return (Finish $run)
}

$runs = @()
foreach ($mode in @("inprocess", "loopback", "attach")) { $runs += Run-Mode $mode }
$runs += Run-Resident

foreach ($r in $runs) { Write-Json (Join-Path $out "$($r.name).result.json") $r }
$all = @($runs | ForEach-Object { $_.checks } )
$failed = @($all | Where-Object { -not $_.ok })
$summary = [ordered]@{
    mixer = $Mixer
    mixer_sha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $Mixer).Hash.ToLower()
    revision = (& git rev-parse HEAD).Trim()
    worktree_dirty = ((& git status --porcelain --untracked-files=no | Measure-Object).Count -gt 0)
    runs = @($runs | ForEach-Object { [ordered]@{ name = $_.name; exit_code = $_.exit_code; checks = $_.checks.Count; failed = @($_.checks | Where-Object { -not $_.ok }).Count; diagnostics = $_.shutdown_diagnostics.Count } })
    checks = $all.Count
    failed = $failed.Count
    finished = (Get-Date).ToString("o")
}
Write-Json (Join-Path $out "summary.json") $summary
Write-Host "== $($all.Count) checks, $($failed.Count) failed"
if ($failed.Count -gt 0) { exit 1 }
