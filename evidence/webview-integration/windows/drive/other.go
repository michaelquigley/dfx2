//go:build !windows

// Command drive is the Windows browser matrix for webview integration stage
// 3. It drives the real Win32 and WebView2 host and runs only on Windows.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "the Windows browser matrix runs only on windows/amd64")
	os.Exit(2)
}
