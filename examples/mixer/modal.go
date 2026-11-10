package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/michaelquigley/dfx2"
	"github.com/michaelquigley/dfx2/internal/appcore"
	"github.com/michaelquigley/dfx2/ui"
)

// titleCounter applies posted title updates at 10 Hz from a window-owned task.
// Each applied update is one public callback that ran on the UI thread, so its
// count shows whether posted callbacks advance during a size/move loop.
type titleCounter struct {
	name    string
	w       *ui.Window
	applied atomic.Uint64
	busy    atomic.Uint64
	paused  atomic.Bool
}

func (m *mixer) startTitles() error {
	for i, w := range m.initial {
		c := &titleCounter{name: fmt.Sprintf("%c", 'A'+i), w: w}
		m.titles = append(m.titles, c)
		if err := w.Go("titles-"+c.name, c.run); err != nil {
			return err
		}
	}
	return nil
}

func (c *titleCounter) run(ctx context.Context) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if c.paused.Load() {
				continue
			}
			err := c.w.Post(func() {
				// The title keeps its window-name prefix, which the driver finds.
				if c.w.SetTitle(fmt.Sprintf("dfx2 mixer %s · %d", c.name, c.applied.Load()+1)) == nil {
					c.applied.Add(1)
				}
			})
			switch {
			case errors.Is(err, dfx2.ErrBusy):
				c.busy.Add(1)
			case errors.Is(err, dfx2.ErrClosed):
				return nil
			}
		}
	}
}

// progressBound is how long one window may show no new title update or frame
// before the signal script fails.
const progressBound = 10 * time.Second

// windowProgress reads a window's applied title updates and rendered frames.
type windowProgress struct {
	name string
	read func() (applied, frames uint64)
}

// awaitProgress reports each window once per round, only after both of its
// counters have risen past the values last reported for it. A window that stops
// rising for bound ends the wait with an error naming what stalled.
func awaitProgress(ctx context.Context, windows []windowProgress, rounds int, bound time.Duration, report func(name string, applied, frames uint64)) error {
	last := make([][2]uint64, len(windows))
	for i, w := range windows {
		last[i][0], last[i][1] = w.read()
	}
	for range rounds {
		for i, w := range windows {
			start := time.Now()
			for {
				applied, frames := w.read()
				var stalled []string
				if applied <= last[i][0] {
					stalled = append(stalled, "title")
				}
				if frames <= last[i][1] {
					stalled = append(stalled, "frame")
				}
				if len(stalled) == 0 {
					last[i] = [2]uint64{applied, frames}
					report(w.name, applied, frames)
					break
				}
				if time.Since(start) >= bound {
					return fmt.Errorf("window %s: no %s progress within %s (applied %d, frames %d)", w.name, strings.Join(stalled, " or "), bound, applied, frames)
				}
				if err := sleep(ctx, 5*time.Millisecond); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (m *mixer) modalActive() bool { return appcore.Inspect(m.a).Native.ModalActive }

// waitModal waits for an established size/move loop, whoever starts it.
func (m *mixer) waitModal(ctx context.Context, want bool, bound time.Duration) error {
	deadline := time.Now().Add(bound)
	for m.modalActive() != want {
		if time.Now().After(deadline) {
			return fmt.Errorf("size/move loop active=%t not reached within %s", want, bound)
		}
		if err := sleep(ctx, 10*time.Millisecond); err != nil {
			return err
		}
	}
	return nil
}

func (m *mixer) framesOf(w *appcore.Window) uint64 {
	s, _ := appcore.InspectContent(w)
	return s.Frames
}

// watch samples whole seconds of an active size/move loop. A partial last
// second is discarded: the loop may end at any point within it.
func (m *mixer) watch(ctx context.Context, cores []*appcore.Window, limit int) ([][]uint64, [][]uint64, error) {
	frames, titles := make([][]uint64, len(cores)), make([][]uint64, len(cores))
	for len(frames[0]) < limit {
		before := make([][2]uint64, len(cores))
		for i, w := range cores {
			before[i] = [2]uint64{m.framesOf(w), m.titles[i].applied.Load()}
		}
		if err := sleep(ctx, time.Second); err != nil {
			return nil, nil, err
		}
		if !m.modalActive() {
			break
		}
		line := []any{"second", len(frames[0]) + 1}
		for i, w := range cores {
			f, n := m.framesOf(w)-before[i][0], m.titles[i].applied.Load()-before[i][1]
			frames[i], titles[i] = append(frames[i], f), append(titles[i], n)
			line = append(line, "frames_"+m.titles[i].name, f, "titles_"+m.titles[i].name, n)
		}
		m.o.event("modal-second", line...)
	}
	return frames, titles, nil
}

func smallest(values []uint64) uint64 {
	if len(values) == 0 {
		return 0
	}
	least := values[0]
	for _, v := range values {
		least = min(least, v)
	}
	return least
}

func total(values []uint64) (sum uint64) {
	for _, v := range values {
		sum += v
	}
	return sum
}

// settleAfterModal stops every producer and measures the settled application.
func (m *mixer) settleAfterModal(ctx context.Context) error {
	for _, c := range m.titles {
		c.paused.Store(true)
	}
	return m.pauseAndSettle(ctx)
}

func (m *mixer) runModal(ctx context.Context) error {
	cores := appcore.ContentWindows(m.a)
	if len(cores) != 2 {
		return fmt.Errorf("modal scripts need two windows, have %d", len(cores))
	}
	if err := sleep(ctx, time.Second); err != nil {
		return err
	}
	m.record("modal-before")
	request := func(mode string, seconds int) {
		m.o.event("drag-request", "window", "A", "mode", mode, "seconds", seconds)
	}
	a, b := m.initial[0], m.initial[1]
	switch m.c.script {
	case "modal-move", "modal-size":
		mode := map[string]string{"modal-move": "move", "modal-size": "size"}[m.c.script]
		request(mode, 13)
		if err := m.waitModal(ctx, true, m.c.modalWait); err != nil {
			return err
		}
		start := appcore.Inspect(m.a)
		frames, titles, err := m.watch(ctx, cores, 120)
		if err != nil {
			return err
		}
		end := appcore.Inspect(m.a)
		m.o.event("modal-summary", "mode", mode, "seconds", len(frames[0]),
			"least_frames_A", smallest(frames[0]), "least_frames_B", smallest(frames[1]),
			"least_titles_A", smallest(titles[0]), "least_titles_B", smallest(titles[1]),
			"frames_A", total(frames[0]), "frames_B", total(frames[1]), "titles_A", total(titles[0]), "titles_B", total(titles[1]),
			"busy_A", m.titles[0].busy.Load(), "busy_B", m.titles[1].busy.Load(),
			"modal_turns", end.Native.ModalTurns-start.Native.ModalTurns, "modal_timers", end.Native.ModalTimers-start.Native.ModalTimers,
			"modal_refusals", end.Native.ModalRefusals-start.Native.ModalRefusals)
		if err := m.waitModal(ctx, false, 10*time.Second); err != nil {
			return err
		}
		if err := m.settleAfterModal(ctx); err != nil {
			return err
		}
		a.Close()
		b.Close()
	case "modal-idle":
		// Holding still inside the loop: once producers stop and meters settle,
		// no timer, wake or frame may run while the loop stays established.
		request("hold", 18)
		if err := m.waitModal(ctx, true, m.c.modalWait); err != nil {
			return err
		}
		if err := m.settleAfterModal(ctx); err != nil {
			return err
		}
		before := appcore.Inspect(m.a)
		if err := sleep(ctx, 3*time.Second); err != nil {
			return err
		}
		after := appcore.Inspect(m.a)
		m.o.event("modal-quiet", "seconds", 3, "modal_active", after.Native.ModalActive, "frames", after.Frames-before.Frames,
			"modal_timers", after.Native.ModalTimers-before.Native.ModalTimers, "modal_turns", after.Native.ModalTurns-before.Native.ModalTurns,
			"wakes", after.Native.Wakes-before.Native.Wakes, "dispatches", after.Native.Dispatches-before.Native.Dispatches)
		// Quiet is not stuck: a post still runs, and the feed still renders.
		ran := make(chan struct{})
		posted := time.Now()
		if err := m.a.Post(func() { close(ran) }); err != nil {
			return err
		}
		select {
		case <-ran:
		case <-time.After(2 * time.Second):
			return errors.New("posted callback did not run inside the idle size/move loop")
		case <-ctx.Done():
			return ctx.Err()
		}
		m.o.event("modal-post-after-idle", "latency_ms", time.Since(posted).Milliseconds(), "modal_active", m.modalActive())
		first := m.framesOf(cores[0])
		m.model.setRunning(true)
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
		m.o.event("modal-resume", "frames", m.framesOf(cores[0])-first, "modal_active", m.modalActive())
		m.model.setRunning(false)
		if err := m.waitModal(ctx, false, 30*time.Second); err != nil {
			return err
		}
		a.Close()
		b.Close()
	case "modal-close-self", "modal-close-other":
		// Close from a worker while the loop runs; the next modal turn tears
		// the window down from inside the loop.
		request("move", 8)
		if err := m.waitModal(ctx, true, m.c.modalWait); err != nil {
			return err
		}
		if err := sleep(ctx, 3*time.Second); err != nil {
			return err
		}
		target, other, otherCore := a, b, cores[1]
		if m.c.script == "modal-close-other" {
			target, other, otherCore = b, a, cores[0]
		}
		closed := time.Now()
		target.Close()
		select {
		case <-target.Done():
		case <-time.After(10 * time.Second):
			return errors.New("closed window did not drain within 10s of its close during a size/move loop")
		case <-ctx.Done():
			return ctx.Err()
		}
		doneMS := time.Since(closed).Milliseconds()
		active := m.modalActive()
		first := m.framesOf(otherCore)
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
		m.o.event("modal-closed", "done_ms", doneMS, "modal_active_at_done", active,
			"remaining_frames", m.framesOf(otherCore)-first, "modal_active", m.modalActive())
		if err := m.waitModal(ctx, false, 15*time.Second); err != nil {
			return err
		}
		if err := m.settleAfterModal(ctx); err != nil {
			return err
		}
		other.Close()
	case "modal-quit":
		request("move", 8)
		if err := m.waitModal(ctx, true, m.c.modalWait); err != nil {
			return err
		}
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
		m.record("modal-quit-sent")
		m.a.Quit()
	}
	return nil
}
