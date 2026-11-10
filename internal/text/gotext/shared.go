package gotext

import (
	"errors"
	"sync"
)

// Families is the preferred font family list for the shared system-font
// engine. Internal examples may set it before the first UI frame; later
// changes have no effect.
var Families = []string{"sans-serif"}

var shared struct {
	sync.Mutex
	engine *Engine
	err    error
	done   bool
}

// Shared returns the process-wide system-font engine, indexing fonts on first
// use. Font discovery and loaded faces are owned here for the process
// lifetime, like the native toolkit libraries, and are deliberately separate
// from each window's bounded caches: windows hold only FontIDs and release
// their caches on close. A failed initialization is retained and reported to
// every caller. Use it from the UI thread only.
func Shared() (*Engine, error) {
	shared.Lock()
	defer shared.Unlock()
	if !shared.done {
		shared.done = true
		dir, err := DefaultCacheDir()
		if err == nil {
			shared.engine, err = NewSystemFonts(Families, dir)
		}
		shared.err = err
	}
	return shared.engine, shared.err
}

// Preset installs e as the shared engine instead of indexing system fonts, for
// internal fixtures that must not depend on host fonts. It must precede the
// first Shared call, and reports an error otherwise.
func Preset(e *Engine) error {
	shared.Lock()
	defer shared.Unlock()
	if shared.done {
		return errors.New("gotext: shared engine already initialized")
	}
	if e == nil {
		return errors.New("gotext: nil engine")
	}
	shared.done, shared.engine = true, e
	return nil
}
