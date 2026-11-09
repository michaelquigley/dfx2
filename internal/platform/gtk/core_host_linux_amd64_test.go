package gtk

import (
	"sync/atomic"
	"testing"

	"github.com/michaelquigley/dfx2/internal/platform"
)

// PaintCoreHost bypasses native startup and widget creation. Present, paint,
// Pump, Destroy and final release remain the real GTK backend, over injected
// calls. The external test package can join it to appcore without an import cycle.
type PaintCoreHost struct {
	*Backend
	id       platform.ID
	Cairo    *fakeCairo
	Unrefs   int
	Released bool
}

func NewPaintCoreHost(t *testing.T, status int32) *PaintCoreHost {
	t.Helper()
	replaceNative(t)
	h := &PaintCoreHost{Backend: &Backend{}}
	h.Cairo = installCairo(t)
	var source atomic.Uint32
	n.idleAdd = func(int32, uintptr, uintptr, uintptr) uint32 { return source.Add(1) }
	n.removeSource = func(uint32) int32 { return 1 }
	n.disconnect = func(uintptr, uint64) {}
	n.unref = func(uintptr) { h.Unrefs++ }
	n.release = func(uintptr) { h.Released = true }
	n.destroy = func(widget uintptr) { h.destroyed(h.id, widget) }
	n.setSourceSurface = func(uintptr, uintptr, float64, float64) {}
	n.paint = func(uintptr) {}
	n.contextStatus = func(uintptr) int32 { return status }
	var draw, painted bool
	n.queueDraw = func(uintptr) { draw = true }
	n.iteration = func(uintptr, int32) int32 {
		if draw {
			draw = false
			h.paint(h.id, 99)
			painted = true
		} else {
			// A later ordinary close makes the old behavior finish with nil,
			// rather than relying on a hang guard to detect the missing error.
			if painted {
				h.closeRequested(h.id)
			}
			h.dispatch()
		}
		return 1
	}
	return h
}

func (h *PaintCoreHost) Open(sink func(platform.Event)) error {
	h.sink = sink
	h.windows = make(map[platform.ID]*window)
	h.token = uintptr(nextToken.Add(1))
	routes.Store(h.token, route{backend: h.Backend})
	return nil
}

func (*PaintCoreHost) IsOwner() bool { return true }

func (h *PaintCoreHost) Create(id platform.ID, options platform.WindowOptions) error {
	h.id = id
	w := &window{widget: 11, area: 12, token: uintptr(nextToken.Add(1))}
	h.mu.Lock()
	h.windows[id] = w
	h.mu.Unlock()
	routes.Store(w.token, route{backend: h.Backend, window: id})
	h.sink(platform.Event{Kind: platform.SurfaceChanged, Window: id,
		Surface: platform.Surface{Width: options.Width, Height: options.Height, Scale: 1, Visible: true}})
	return nil
}

func (h *PaintCoreHost) ImagesRemaining() int { return len(h.Cairo.buffers) }
