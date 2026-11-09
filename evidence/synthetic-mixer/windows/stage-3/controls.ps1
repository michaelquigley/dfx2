param(
    [string]$OutDir = "evidence/synthetic-mixer/windows/stage-3/run/controls"
)

# Run from the repository root on Windows, in a connected interactive desktop
# session. See README.md beside this script. Hands off the mouse and keyboard.
$ErrorActionPreference = "Stop"
if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw "This audition requires actual Windows execution."
}
$env:CGO_ENABLED = "0"
$out = [IO.Path]::GetFullPath($OutDir)
$scratch = Join-Path $env:TEMP "dfx2-mixer-audition"
New-Item -ItemType Directory -Force $out, $scratch | Out-Null
$mixer = Join-Path $scratch "mixer.exe"
$drive = Join-Path $scratch "controlsdrive.exe"
& go build -o $mixer ./examples/mixer
if ($LASTEXITCODE -ne 0) { throw "go build mixer failed" }
& go build -C evidence/synthetic-mixer/windows/stage-3/controlsdrive -o $drive .
if ($LASTEXITCODE -ne 0) { throw "go build controlsdrive failed" }
[IO.File]::WriteAllText((Join-Path $out "revision.txt"), ((& git rev-parse HEAD | Out-String) + (& git status --short | Out-String)))
& $drive -fixture $mixer -out $out
$code = $LASTEXITCODE
if ($code -ne 0) {
    & $drive -release
    throw "Controls audition failed (exit $code); see $out\summary.json"
}
Write-Host "Controls audition passed. The manual audition in README.md is still required."
