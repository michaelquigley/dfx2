package appcore

import (
	"github.com/michaelquigley/dfx2/internal/platform"
	"github.com/michaelquigley/dfx2/internal/platform/gtk"
)

func newBackend() (platform.Backend, error) { return &gtk.Backend{}, nil }
