package main

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestLevelDeterministic(t *testing.T) {
	for n := range uint64(1000) {
		v := levelAt(n)
		if v != levelAt(n) || v <= 0 || v > 1 {
			t.Fatalf("levelAt(%d) = %v, again %v", n, v, levelAt(n))
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); !cond(); {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// A UI that doesn't take updates gets one wakeup, however many ticks pass,
// and then reads the latest level, not the first.
func TestFeedCoalesces(t *testing.T) {
	var wakes atomic.Int32
	f := newMeterFeed(time.Millisecond, func() bool { wakes.Add(1); return true })
	f.start(true)
	defer f.stop()
	waitFor(t, "ticks", func() bool { return f.ticks.Load() >= 20 })
	if n := wakes.Load(); n != 1 {
		t.Fatalf("%d wakeups for %d ticks before a take, want 1", n, f.ticks.Load())
	}
	f.setRunning(false)
	level, tick, seq := f.take()
	if seq != f.ticks.Load() || tick != seq || level != levelAt(tick) {
		t.Fatalf("take = level %v tick %d seq %d after %d ticks", level, tick, seq, f.ticks.Load())
	}
	f.setRunning(true)
	waitFor(t, "a wakeup after take", func() bool { return wakes.Load() == 2 })
}

// A wakeup that can't be queued doesn't leave the feed stuck pending.
func TestFeedRetriesFailedWake(t *testing.T) {
	var calls atomic.Int32
	f := newMeterFeed(time.Millisecond, func() bool { return calls.Add(1) > 1 })
	f.start(true)
	defer f.stop()
	waitFor(t, "a second wake attempt", func() bool { return calls.Load() >= 2 })
	if f.failed.Load() != 1 || f.posts.Load() != 1 {
		t.Fatalf("failed %d, posts %d; want 1 and 1", f.failed.Load(), f.posts.Load())
	}
}

// Paused, the worker neither ticks nor wakes the UI; resumed, it does both.
func TestFeedPause(t *testing.T) {
	var wakes atomic.Int32
	f := newMeterFeed(time.Millisecond, func() bool { wakes.Add(1); return true })
	f.start(false)
	defer f.stop()
	time.Sleep(30 * time.Millisecond)
	if f.ticks.Load() != 0 || wakes.Load() != 0 {
		t.Fatalf("started paused: %d ticks, %d wakes", f.ticks.Load(), wakes.Load())
	}
	f.setRunning(true)
	waitFor(t, "ticks", func() bool { return f.ticks.Load() >= 5 })
	f.setRunning(false) // returns once the worker has the request: no tick after
	f.take()
	ticks, w := f.ticks.Load(), wakes.Load()
	time.Sleep(30 * time.Millisecond)
	if f.ticks.Load() != ticks || wakes.Load() != w {
		t.Fatalf("paused, ticks went %d→%d and wakes %d→%d", ticks, f.ticks.Load(), w, wakes.Load())
	}
}

// Once stop returns, the worker is gone and never wakes the UI again; stop
// and setRunning after it return at once.
func TestFeedStop(t *testing.T) {
	var wakes, after atomic.Int32
	var stopped atomic.Bool
	f := newMeterFeed(time.Millisecond, func() bool {
		if stopped.Load() {
			after.Add(1)
		}
		wakes.Add(1)
		return true
	})
	f.start(true)
	waitFor(t, "a wakeup", func() bool { return wakes.Load() >= 1 })
	f.take() // keep it posting
	f.stop()
	stopped.Store(true)
	f.take()
	time.Sleep(20 * time.Millisecond)
	if after.Load() != 0 {
		t.Fatalf("%d wakeups after stop returned", after.Load())
	}
	done := make(chan struct{})
	go func() { f.stop(); f.setRunning(true); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stop or setRunning blocked after stop")
	}
}
