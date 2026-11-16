package webview2

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// The pinned loader: WebView2Loader.dll, x64, from Microsoft.Web.WebView2
// 1.0.4129.50 (build/native/x64), redistributed under the BSD-style license in
// sdk/LICENSE.txt with sdk/NOTICE.txt. It is embedded in the Windows files and
// extracted, verified and loaded on the first attachment. It finds and loads
// the system WebView2 Runtime; it does not bundle that runtime.
const (
	loaderName    = "WebView2Loader.dll"
	loaderVersion = "1.0.4129.50"
	loaderSHA256  = "a9a09232c25805323d4cfb3fc8f545a190a9c8a99c93262ea99d0b88df99ec90"
	loaderPrefix  = "dfx2-webview2-loader-"
)

// extract writes the embedded loader into a fresh application-private
// directory under the temporary directory and verifies the written file's
// digest before anything loads it. On error nothing extracted remains.
func extract(blob []byte, want string) (dir, path string, err error) {
	if sum := sha256.Sum256(blob); hex.EncodeToString(sum[:]) != want {
		return "", "", fmt.Errorf("embedded %s digest %x is not the pinned %s", loaderName, sum, want)
	}
	dir, err = os.MkdirTemp("", loaderPrefix)
	if err != nil {
		return "", "", fmt.Errorf("loader directory: %w", err)
	}
	path = filepath.Join(dir, loaderName)
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		os.RemoveAll(dir)
		return "", "", fmt.Errorf("extracting %s: %w", loaderName, err)
	}
	written, err := os.ReadFile(path)
	if err == nil {
		if sum := sha256.Sum256(written); hex.EncodeToString(sum[:]) != want {
			err = errors.New("the extracted file does not match the embedded loader")
		}
	}
	if err != nil {
		os.RemoveAll(dir)
		return "", "", fmt.Errorf("verifying %s: %w", path, err)
	}
	return dir, path, nil
}
