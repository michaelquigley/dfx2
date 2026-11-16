package webview2

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/michaelquigley/dfx2/internal/platform"
)

func digest(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestExtractVerifiesTheEmbeddedAndWrittenLoader(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("TMP", os.Getenv("TMPDIR"))
	t.Setenv("TEMP", os.Getenv("TMPDIR"))
	blob := []byte("not really a dll")
	if _, _, err := extract(blob, loaderSHA256); err == nil || !strings.Contains(err.Error(), "not the pinned") {
		t.Fatalf("mismatched blob extracted: %v", err)
	}
	if entries, _ := os.ReadDir(os.TempDir()); len(entries) != 0 {
		t.Fatalf("a refused blob left %d entries", len(entries))
	}
	dir, path, err := extract(blob, digest(blob))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(filepath.Base(dir), loaderPrefix) || filepath.Base(path) != loaderName || filepath.Dir(path) != dir || !filepath.IsAbs(path) {
		t.Fatalf("extracted to %s / %s", dir, path)
	}
	if written, _ := os.ReadFile(path); string(written) != string(blob) {
		t.Fatal("extracted bytes differ")
	}
	os.RemoveAll(dir)
}

func TestEmbeddedLoaderPinIsTheSDKFile(t *testing.T) {
	// The file beside this package is what Windows builds embed; its digest
	// is the pin documented in loader.go and the spike's README.
	b, err := os.ReadFile(filepath.Join("sdk", loaderName))
	if err != nil {
		t.Skip("sdk/WebView2Loader.dll not present:", err)
	}
	if got := digest(b); got != loaderSHA256 {
		t.Fatalf("sdk/%s digest %s, pin %s", loaderName, got, loaderSHA256)
	}
	for _, name := range []string{"LICENSE.txt", "NOTICE.txt"} {
		if _, err := os.Stat(filepath.Join("sdk", name)); err != nil {
			t.Errorf("redistribution notice missing: %v", err)
		}
	}
}

func TestCleanupResidueIsNamedAfterBoundedAttempts(t *testing.T) {
	saved := removeAll
	t.Cleanup(func() { removeAll = saved })
	attempts := 0
	removeAll = func(string) error { attempts++; return errors.New("busy") }
	err := removeAfterReleaseWith(`C:\x\dfx2-webview2-profile-1`, 3, 0)
	var residue *platform.FileDeletionResidue
	if !errors.As(err, &residue) || residue.Path != `C:\x\dfx2-webview2-profile-1` || attempts != 3 {
		t.Fatalf("%v after %d attempts", err, attempts)
	}
	attempts = 0
	removeAll = func(string) error { attempts++; return nil }
	if err := removeAfterReleaseWith("x", 3, 0); err != nil || attempts != 1 {
		t.Fatalf("%v after %d attempts", err, attempts)
	}
}
