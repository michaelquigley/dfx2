package webview2

import (
	"os"
	"time"

	"github.com/michaelquigley/dfx2/internal/platform"
)

// Post-release removal of framework-owned directories (a window's profile,
// the extracted loader): bounded attempts, then a named residue. Called only
// after native users have released the files: the browser process has exited,
// the environment is released, the loader is unloaded.
const (
	removalAttempts = 20
	removalDelay    = 100 * time.Millisecond
)

var defaultRemoveAll = os.RemoveAll
var removeAll = defaultRemoveAll

func removeAfterRelease(dir string) error {
	return removeAfterReleaseWith(dir, removalAttempts, removalDelay)
}

func removeAfterReleaseWith(dir string, attempts int, delay time.Duration) error {
	var err error
	for attempt := range attempts {
		if attempt > 0 {
			time.Sleep(delay)
		}
		if err = removeAll(dir); err == nil {
			return nil
		}
	}
	return &platform.FileDeletionResidue{Path: dir, Err: err}
}
