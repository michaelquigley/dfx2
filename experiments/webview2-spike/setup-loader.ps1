# Fetches the pinned WebView2 SDK, verifies it, and puts the x64
# WebView2Loader.dll beside the spike's executable. Nothing it writes is
# tracked by git (see .gitignore).
#
#   .\setup-loader.ps1            # into this folder, beside dfx2-webview2-spike.exe
#   .\setup-loader.ps1 -Dest DIR  # somewhere else
#
# The SDK package also holds WebView2.idl, which the bindings test reads:
#   $env:WEBVIEW2_IDL = "$PWD\.sdk\1.0.4129.50\WebView2.idl"; go test -run IDL -v .
param(
    [string]$Dest = $PSScriptRoot
)
$ErrorActionPreference = 'Stop'

$version = '1.0.4129.50'
$url = "https://api.nuget.org/v3-flatcontainer/microsoft.web.webview2/$version/microsoft.web.webview2.$version.nupkg"
# Pinned from the download made on 2026-11-03 (see docs/future/webview.md).
$nupkgSHA256 = 'D3934F482D484B89FB4825DF720C710664E1143A1E90F7B3A60794EF33F473D2'
$loaderSHA256 = 'A9A09232C25805323D4CFB3FC8F545A190A9C8A99C93262EA99D0B88DF99EC90'

$sdk = Join-Path $PSScriptRoot ".sdk\$version"
$pkg = Join-Path $PSScriptRoot ".sdk\microsoft.web.webview2.$version.zip"
New-Item -ItemType Directory -Force (Split-Path $pkg) | Out-Null

if (-not (Test-Path $pkg)) {
    Write-Host "downloading $url"
    Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile $pkg
}
$got = (Get-FileHash $pkg -Algorithm SHA256).Hash
if ($got -ne $nupkgSHA256) {
    Remove-Item $pkg
    throw "SDK package checksum $got does not match the pinned $nupkgSHA256 (deleted; nothing installed)"
}

if (Test-Path $sdk) { Remove-Item -Recurse -Force $sdk }
Expand-Archive -Path $pkg -DestinationPath $sdk

$loader = Join-Path $sdk 'build\native\x64\WebView2Loader.dll'
$got = (Get-FileHash $loader -Algorithm SHA256).Hash
if ($got -ne $loaderSHA256) {
    throw "x64 WebView2Loader.dll checksum $got does not match the pinned $loaderSHA256"
}
$sig = Get-AuthenticodeSignature $loader
if ($sig.Status -ne 'Valid') {
    throw "WebView2Loader.dll signature is $($sig.Status)"
}

Copy-Item $loader (Join-Path $Dest 'WebView2Loader.dll') -Force
Write-Host "WebView2Loader.dll $version (x64), sha256 $loaderSHA256"
Write-Host "  signed by: $($sig.SignerCertificate.Subject)"
Write-Host "  installed: $(Join-Path $Dest 'WebView2Loader.dll')"
Write-Host "  IDL:       $(Join-Path $sdk 'WebView2.idl')"
