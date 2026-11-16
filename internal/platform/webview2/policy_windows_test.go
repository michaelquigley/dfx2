package webview2

import "testing"

// Literal Windows paths exercise the Windows filepath implementation.
func TestWithinWindowsPaths(t *testing.T) {
	for _, tc := range []struct {
		path, dir string
		want      bool
	}{
		{`C:\Temp\dfx2-webview2-profile-1\EBWebView\Default`, `C:\Temp\dfx2-webview2-profile-1`, true},
		{`c:\temp\DFX2-WEBVIEW2-PROFILE-1\EBWebView`, `C:\Temp\dfx2-webview2-profile-1`, true},
		{`C:\Temp\dfx2-webview2-profile-1`, `C:\Temp\dfx2-webview2-profile-1`, true},
		{`C:\Temp\dfx2-webview2-profile-10\EBWebView`, `C:\Temp\dfx2-webview2-profile-1`, false},
		{`C:\Users\x\AppData\Local\app\EBWebView`, `C:\Temp\dfx2-webview2-profile-1`, false},
	} {
		if got := within(tc.path, tc.dir); got != tc.want {
			t.Errorf("within(%q, %q) = %t", tc.path, tc.dir, got)
		}
	}
}
