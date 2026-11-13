package platform

import "fmt"

// FileDeletionResidue identifies files left after native users have released
// them and bounded deletion attempts have finished. Return it directly from
// BrowserHost.Cleanup, with a nonempty path and underlying deletion error, to
// retain the error without canceling other windows. It never represents live
// native ownership, another cleanup failure, or a panic.
type FileDeletionResidue struct {
	Path string
	Err  error
}

func (e *FileDeletionResidue) Error() string {
	return fmt.Sprintf("could not delete %s: %v", e.Path, e.Err)
}

func (e *FileDeletionResidue) Unwrap() error { return e.Err }
