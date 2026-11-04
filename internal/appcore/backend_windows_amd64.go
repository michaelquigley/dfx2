package appcore

import (
	"github.com/michaelquigley/dfx2/internal/platform"
	"github.com/michaelquigley/dfx2/internal/platform/win32"
)

func newBackend() (platform.Backend, error) { return &win32.Backend{}, nil }
