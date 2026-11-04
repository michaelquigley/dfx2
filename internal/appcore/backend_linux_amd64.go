package appcore

import "github.com/michaelquigley/dfx2/internal/platform"

// Stage 1 is deliberately headless. The GTK selector lands in stage 2.
func newBackend() (platform.Backend, error) { return nil, ErrUnsupportedPlatform }
