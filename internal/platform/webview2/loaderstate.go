package webview2

import (
	"fmt"
	"sync"
)

// loaderState is the process-wide loader: extracted, verified and loaded by
// the first attachment, kept while any attachment lives, unloaded by the last
// one's release and then removed, with the same bounded post-release policy
// as profiles. A loader a live browser needs is owned work, never residue. A
// later attachment extracts a fresh copy. UI thread.
type loaderState struct {
	mu      sync.Mutex
	users   int
	lib     uintptr
	dir     string
	path    string
	version string
}

var loader = &loaderState{}

// loaderDigest is the digest the embedded loader must have; tests substitute
// a blob of their own.
var loaderDigest = loaderSHA256

// acquire loads the loader for one more user and reports the runtime version
// it found. On error nothing extracted or loaded remains for this user.
func (l *loaderState) acquire() (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.users > 0 {
		l.users++
		return l.version, nil
	}
	dir, path, err := extract(n.loaderBlob(), loaderDigest)
	if err != nil {
		return "", err
	}
	// An absolute path: LoadLibrary takes exactly this file, never one found
	// on the search path.
	lib, err := n.loadLibrary(path)
	if err != nil {
		removeAll(dir)
		return "", fmt.Errorf("loading %s: %w", path, err)
	}
	version, err := n.runtimeVersion(lib)
	if err != nil {
		n.freeLibrary(lib)
		removeAll(dir)
		return "", err
	}
	l.lib, l.dir, l.path, l.version, l.users = lib, dir, path, version, 1
	return version, nil
}

// release drops one user. The last unloads the loader and removes its
// directory through cleanup, off the UI thread with bounded attempts.
func (l *loaderState) release(cleanup func(name string, work func() error) error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.users == 0 {
		return
	}
	l.users--
	if l.users > 0 {
		return
	}
	lib, dir := l.lib, l.dir
	l.lib, l.dir, l.path = 0, "", ""
	err := n.freeLibrary(lib)
	cleanup("WebView2 loader "+dir, func() error {
		if err != nil {
			return fmt.Errorf("unloading %s: %w", loaderName, err)
		}
		return removeAfterRelease(dir)
	})
}
