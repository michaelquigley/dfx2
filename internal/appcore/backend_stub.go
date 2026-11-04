//go:build (!linux && !windows) || !amd64

package appcore

import "github.com/michaelquigley/dfx2/internal/platform"

// Replaced with target-specific selectors as native backends land.
func newBackend() (platform.Backend, error) { return nil, ErrUnsupportedPlatform }
