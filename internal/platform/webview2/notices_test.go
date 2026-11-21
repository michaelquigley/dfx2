package webview2

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A shipped Windows executable carries the loader and, byte for byte, both of
// its redistribution notices: built here as a user would build one, from the
// example that links this package. Cross-compilation proves what the linker
// keeps, not that the program runs.
func TestWindowsExecutableCarriesLoaderNotices(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a Windows executable")
	}
	exe := filepath.Join(t.TempDir(), "webview.exe")
	cmd := exec.Command("go", "build", "-o", exe, "github.com/michaelquigley/dfx2/examples/webview")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=windows", "GOARCH=amd64")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	binary, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"WebView2Loader.dll", "LICENSE.txt", "NOTICE.txt"} {
		want, err := os.ReadFile(filepath.Join("sdk", name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(binary, want) {
			t.Errorf("the Windows executable does not carry sdk/%s", name)
		}
	}
}
