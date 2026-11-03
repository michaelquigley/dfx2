module dfx2webkitgtkspike

go 1.26

require (
	dfx2webview2spike v0.0.0
	github.com/ebitengine/purego v0.11.1
)

// Share the experimental handler/adapter, not the Windows host.
replace dfx2webview2spike => ../webview2-spike
