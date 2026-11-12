package feasibility

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// Removal records one bounded deletion of an experiment-owned directory.
type Removal struct {
	Path     string        `json:"path"`
	Attempts int           `json:"attempts"`
	Elapsed  time.Duration `json:"elapsed_ns"`
	Removed  bool          `json:"removed"`
	Injected bool          `json:"injected,omitempty"`
	Err      string        `json:"error,omitempty"`
}

// ErrNotReleased reports a deletion attempted while native users may still
// hold the directory. Deletion failure is never permission to abandon them.
var ErrNotReleased = errors.New("feasibility: native users have not released the directory")

// RemoveAfterRelease deletes dir with up to attempts tries, interval apart,
// but only once released reports that every native user has let it go. On
// persistent failure it returns an error naming the residue; the caller must
// never reuse that directory. remove is os.RemoveAll unless a test injects a
// failure; the attempt count and timing are spike parameters, not defaults.
func RemoveAfterRelease(dir string, released bool, attempts int, interval time.Duration, remove func(string) error) (Removal, error) {
	r := Removal{Path: dir}
	if !released {
		return r, ErrNotReleased
	}
	if remove == nil {
		remove = os.RemoveAll
	}
	start := time.Now()
	var last error
	for r.Attempts < attempts {
		r.Attempts++
		if last = remove(dir); last == nil {
			if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
				r.Removed = true
				break
			}
			last = fmt.Errorf("%s still exists after removal", dir)
		}
		if r.Attempts < attempts {
			time.Sleep(interval)
		}
	}
	r.Elapsed = time.Since(start)
	if !r.Removed {
		r.Err = fmt.Sprintf("profile residue %s not deleted after %d attempts: %v", dir, r.Attempts, last)
		return r, errors.New(r.Err)
	}
	return r, nil
}
