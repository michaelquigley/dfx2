param(
    [string]$OutDir = "evidence/synthetic-mixer/windows/stage-4/run"
)

# Run from the repository root on Windows, in a connected interactive desktop
# session, after the operator transfers the committed revision. See README.md
# beside this script. Hands off the mouse and keyboard. No result here is
# supplied by cross-building.
$ErrorActionPreference = "Stop"
if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw "This audition requires actual Windows execution."
}
$env:CGO_ENABLED = "0"
[Console]::InputEncoding = New-Object Text.UTF8Encoding($false)
$out = [IO.Path]::GetFullPath($OutDir)
$gates = Join-Path $out "gates"
$scratch = Join-Path $env:TEMP "dfx2-mixer-audition"
New-Item -ItemType Directory -Force $out, $gates, $scratch | Out-Null
$utf8 = New-Object Text.UTF8Encoding($false)
[IO.File]::WriteAllText((Join-Path $out "revision.txt"), ((& git rev-parse HEAD | Out-String) + (& git status --short | Out-String)), $utf8)

function Invoke-Gate([string]$Name, [string[]]$GoArgs) {
    # Retain native stderr and judge the actual exit code, as audition.ps1 does.
    $savedPreference = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        $text = (& go @GoArgs 2>&1 | Out-String)
        $code = $LASTEXITCODE
    } finally { $ErrorActionPreference = $savedPreference }
    [IO.File]::WriteAllText((Join-Path $gates ($Name + ".log")), ("go " + ($GoArgs -join " ") + "`r`nexit " + $code + "`r`n" + $text), $utf8)
    if ($code -ne 0) { throw "go $GoArgs failed ($code); see $gates\$Name.log" }
}

# Stage 4's own repeated gates: the example's fixtures, the UI core path and
# the shared text system, on this host.
Invoke-Gate "mixer-repeated" @("test", "./examples/mixer", "-count=100", "-timeout=600s")
Invoke-Gate "ui-repeated" @("test", "./ui", "-count=50", "-timeout=600s")
Invoke-Gate "text-repeated" @("test", "./internal/text/...", "-count=50", "-timeout=600s")
Invoke-Gate "vet-controlsdrive" @("vet", "-C", "evidence/synthetic-mixer/windows/stage-4/controlsdrive", ".")

# The stage 2 matrix, unchanged: tests, vet, builds, every presentation and
# size/move case, and the nested foundation lifecycle matrix.
& powershell.exe -NoProfile -ExecutionPolicy Bypass -File evidence\synthetic-mixer\windows\audition.ps1 -OutDir (Join-Path $out "matrix")
if ($LASTEXITCODE -ne 0) { throw "Regression matrix failed (exit $LASTEXITCODE); see $out\matrix" }

# The stage 4 controls and command audition, then its two text-cache runs.
$mixer = Join-Path $scratch "mixer.exe"
$drive = Join-Path $scratch "controlsdrive4.exe"
& go build -o $mixer ./examples/mixer
if ($LASTEXITCODE -ne 0) { throw "go build mixer failed" }
& go build -C evidence/synthetic-mixer/windows/stage-4/controlsdrive -o $drive .
if ($LASTEXITCODE -ne 0) { throw "go build controlsdrive failed" }
$controls = Join-Path $out "controls"
New-Item -ItemType Directory -Force $controls | Out-Null
& $drive -fixture $mixer -out $controls
$code = $LASTEXITCODE
if ($code -ne 0) {
    & $drive -release
    throw "Stage 4 audition failed (exit $code); see $controls\summary.json"
}
Write-Host "Stage 4 automated audition passed. The manual audition in README.md is still required."
