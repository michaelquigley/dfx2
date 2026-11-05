package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/michaelquigley/dfx2"
	"github.com/michaelquigley/dfx2/internal/appcore"
	"github.com/michaelquigley/dfx2/ui"
)

// Bounded native audition scripts. They run as an application task and drive
// the UI thread only through Post; their waits are wall-clock pacing for real
// desktop behavior, not test ordering.

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func (m *mixer) openOnUI(ctx context.Context) (*ui.Window, error) {
	var w *ui.Window
	err := m.call(ctx, func() (err error) { w, err = m.open(); return err })
	return w, err
}

func (m *mixer) record(event string, extra ...any) {
	values := status(m.a)
	if ws := appcore.ContentWindows(m.a); len(ws) > 0 {
		if s, ok := appcore.InspectContent(ws[0]); ok {
			values = append(values, "first_frames", s.Frames, "first_surface",
				fmt.Sprintf("%dx%d@%g visible=%t", s.Surface.Width, s.Surface.Height, s.Surface.Scale, s.Surface.Visible))
		}
	}
	m.o.event(event, append(values, extra...)...)
}

// settle waits until no window has had a live animation deadline for 250 ms.
// A rendering frame clears its deadline at frame start, so one zero sample
// can be a frame in flight rather than a settled window.
func (m *mixer) settle(ctx context.Context) error {
	deadline := time.Now().Add(5 * time.Second)
	var since time.Time
	for {
		s := appcore.Inspect(m.a)
		switch {
		case s.Animating != 0:
			since = time.Time{}
		case since.IsZero():
			since = time.Now()
		case time.Since(since) >= 250*time.Millisecond:
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("animation did not settle within 5s")
		}
		if err := sleep(ctx, 5*time.Millisecond); err != nil {
			return err
		}
	}
}

// quiet measures a settled interval: framework frames, deadline timers and
// application wakes should not grow; OS exposure paints may.
func (m *mixer) quiet(ctx context.Context, d time.Duration) error {
	before := appcore.Inspect(m.a)
	if err := sleep(ctx, d); err != nil {
		return err
	}
	after := appcore.Inspect(m.a)
	m.o.event("quiet", "seconds", d.Seconds(),
		"frames", after.Frames-before.Frames, "frame_timers", after.FrameTimers-before.FrameTimers,
		"wakes", after.Native.Wakes-before.Native.Wakes, "dispatches", after.Native.Dispatches-before.Native.Dispatches,
		"presents", after.Native.Presents-before.Native.Presents, "paints", after.Native.Paints-before.Native.Paints,
		"animating", after.Animating)
	return nil
}

func (m *mixer) snapshot(ctx context.Context, name string) error {
	if m.c.snapshots == "" {
		return nil
	}
	path, err := filepath.Abs(filepath.Join(m.c.snapshots, name+".png"))
	if err != nil {
		return err
	}
	err = m.call(ctx, func() error {
		ws := appcore.ContentWindows(m.a)
		if len(ws) == 0 {
			return errors.New("no content window to snapshot")
		}
		return appcore.AuditionSnapshot(ws[0], path)
	})
	if err == nil {
		m.o.event("snapshot", "path", path)
	}
	return err
}

func (m *mixer) onFirst(ctx context.Context, f func(*appcore.Window) error) error {
	return m.call(ctx, func() error {
		ws := appcore.ContentWindows(m.a)
		if len(ws) == 0 {
			return errors.New("no content window")
		}
		return f(ws[0])
	})
}

func (m *mixer) pauseAndSettle(ctx context.Context) error {
	m.src.setRunning(false)
	if err := m.settle(ctx); err != nil {
		return err
	}
	m.record("settled")
	return m.quiet(ctx, 5*time.Second)
}

func (m *mixer) runScript(ctx context.Context) error {
	switch m.c.script {
	case "animate":
		w := m.initial[0]
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
		m.record("animating")
		if err := m.snapshot(ctx, "animate-running"); err != nil {
			return err
		}
		if err := m.pauseAndSettle(ctx); err != nil {
			return err
		}
		if err := m.snapshot(ctx, "animate-settled"); err != nil {
			return err
		}
		w.Close()
	case "resize":
		w := m.initial[0]
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
		for i, size := range [][2]int{{760, 320}, {380, 240}, {980, 420}, {560, 260}} {
			if err := m.onFirst(ctx, func(w *appcore.Window) error { return appcore.AuditionResize(w, size[0], size[1]) }); err != nil {
				return err
			}
			if err := sleep(ctx, 700*time.Millisecond); err != nil {
				return err
			}
			m.record("resized", "requested", fmt.Sprintf("%dx%d", size[0], size[1]))
			if err := m.snapshot(ctx, fmt.Sprintf("resize-%d-%dx%d", i, size[0], size[1])); err != nil {
				return err
			}
		}
		if err := m.pauseAndSettle(ctx); err != nil {
			return err
		}
		w.Close()
	case "minimize":
		w := m.initial[0]
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
		m.record("before-minimize")
		if err := m.onFirst(ctx, func(w *appcore.Window) error { return appcore.AuditionIconify(w, true) }); err != nil {
			return err
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
		m.record("minimized")
		if err := sleep(ctx, 1500*time.Millisecond); err != nil {
			return err
		}
		m.record("minimized-later")
		if err := m.onFirst(ctx, func(w *appcore.Window) error { return appcore.AuditionIconify(w, false) }); err != nil {
			return err
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
		m.record("restored")
		if err := m.pauseAndSettle(ctx); err != nil {
			return err
		}
		w.Close()
	case "close-sample":
		w := m.initial[0]
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
		m.record("sampling")
		// Close between two invalidations while the 30 Hz sampler also
		// publishes; invalidation keeps racing the closing window afterwards.
		var accepted, refused int
		start := time.Now()
		closed := false
		for time.Since(start) < 1500*time.Millisecond {
			if !closed && time.Since(start) > time.Second {
				w.Close()
				closed = true
			}
			switch err := w.Invalidate(); {
			case err == nil:
				accepted++
			case errors.Is(err, dfx2.ErrClosed):
				refused++
			default:
				return err
			}
			time.Sleep(time.Millisecond)
		}
		m.o.event("close-sample", "accepted", accepted, "refused_after_close", refused)
	case "reopen":
		for i := range 5 {
			w, err := m.openOnUI(ctx)
			if err != nil {
				return err
			}
			if err := sleep(ctx, 400*time.Millisecond); err != nil {
				return err
			}
			w.Close()
			select {
			case <-w.Done():
			case <-ctx.Done():
				return ctx.Err()
			}
			m.record("reopen-cycle", "cycle", i+1)
		}
		m.a.Quit()
	case "two":
		a, b := m.initial[0], m.initial[1]
		if err := sleep(ctx, 1500*time.Millisecond); err != nil {
			return err
		}
		m.record("two-running")
		a.Close()
		<-a.Done()
		before := appcore.Inspect(m.a).Frames
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
		m.record("one-running", "frames_after_close", appcore.Inspect(m.a).Frames-before)
		if err := m.pauseAndSettle(ctx); err != nil {
			return err
		}
		b.Close()
	}
	return nil
}
