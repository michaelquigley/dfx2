package dfx2

import (
	"context"

	"github.com/michaelquigley/dfx2/internal/appcore"
)

type App = appcore.App
type Mode = appcore.Mode
type Options = appcore.Options

const (
	WindowApplication   = appcore.WindowApplication
	ResidentApplication = appcore.ResidentApplication
)

var (
	ErrAlreadyRun          = appcore.ErrAlreadyRun
	ErrUnsupportedPlatform = appcore.ErrUnsupportedPlatform
	ErrWrongThread         = appcore.ErrWrongThread
	ErrClosed              = appcore.ErrClosed
	ErrBusy                = appcore.ErrBusy
)

// Run owns the UI loop until cooperative cleanup finishes. A second invocation,
// including one after a failed first invocation, returns ErrAlreadyRun.
func Run(ctx context.Context, options Options, setup func(*App) error) error {
	return appcore.Run(ctx, options, setup)
}
