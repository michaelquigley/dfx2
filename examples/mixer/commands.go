package main

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"
)

// queueLimit bounds the example's command FIFO.
const queueLimit = 64

var (
	errQueueFull    = errors.New("queue full")
	errShuttingDown = errors.New("shutting down")
)

// Command is one user edit on its way to the synthetic device. Values are
// absolute, so a later command never depends on an earlier one succeeding.
type Command struct {
	Seq     uint64
	Window  string
	Channel string
	Control string // "gain" or "mute"
	Gain    float64
	Mute    bool
	at      time.Time // acceptance, for latency observations
}

func (c Command) Value() any {
	if c.Control == "gain" {
		return c.Gain
	}
	return c.Mute
}

func (c Command) ValueText() string {
	if c.Control == "gain" {
		return strconv.FormatFloat(c.Gain, 'f', 2, 64)
	}
	if c.Mute {
		return "on"
	}
	return "off"
}

// Record is one command's state change. Every submission gets a sequence
// number; it is then accepted or rejected, and an accepted one is later
// applied, rejected by the device or canceled at shutdown, exactly once.
type Record struct {
	Seq     uint64
	State   string // accepted, applied, rejected or canceled
	Command Command
	Reason  string
	Latency time.Duration // acceptance to resolution
}

// commands is the bounded FIFO between the views and the device worker.
// Submission never waits: a full queue rejects the command, visibly. The
// queue never drops or merges edits.
type commands struct {
	mu     sync.Mutex
	next   uint64
	queue  chan Command
	closed bool
	model  *model
	record func(Record)
	// gate holds a command before the device sees it: the injected device
	// delay in the program, a channel in tests. It returns an error only when
	// ctx is canceled.
	gate func(context.Context, Command) error
	// waiting, when set, runs each time the device delay starts waiting with
	// a positive remainder; tests use it as a barrier.
	waiting func()
}

func newCommands(m *model, record func(Record)) *commands {
	q := &commands{queue: make(chan Command, queueLimit), model: m, record: record}
	q.gate = q.delay
	return q
}

// delay holds a command for the device delay, measured from when the device
// took it. Changing the delay applies to the command already held, so setting
// it to zero releases an overloaded queue at once.
func (q *commands) delay(ctx context.Context, _ Command) error {
	start := time.Now()
	for {
		d, changed := q.model.delay()
		remaining := d - time.Since(start)
		if remaining <= 0 {
			return ctx.Err()
		}
		t := time.NewTimer(remaining)
		if q.waiting != nil {
			q.waiting()
		}
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
			return ctx.Err()
		case <-changed:
			t.Stop()
		}
	}
}

// Submit assigns cmd the next sequence number and queues it without waiting.
// The pending value is set and the acceptance recorded before the worker can
// receive it, so records for one command are always in order.
func (q *commands) Submit(cmd Command) (uint64, error) {
	q.mu.Lock()
	q.next++
	cmd.Seq = q.next
	var err error
	switch {
	case q.closed:
		err = errShuttingDown
	case len(q.queue) == cap(q.queue): // only submitters send, under q.mu
		err = errQueueFull
	}
	if err != nil {
		q.record(Record{Seq: cmd.Seq, State: "rejected", Command: cmd, Reason: err.Error()})
		q.mu.Unlock()
		q.model.refuse(cmd, err)
		return cmd.Seq, err
	}
	cmd.at = time.Now()
	q.model.accept(cmd)
	q.record(Record{Seq: cmd.Seq, State: "accepted", Command: cmd})
	q.queue <- cmd
	q.mu.Unlock()
	q.model.publish(nil) // every window shows the pending value
	return cmd.Seq, nil
}

// run is the ordered command worker, owned by the application. Commands reach
// the device one at a time in acceptance order. Cancellation ends it: the
// command being held and every queued one are canceled, never applied, and
// later submissions are rejected.
func (q *commands) run(ctx context.Context) error {
	var held *Command
	defer func() { q.shutdown(held) }()
	for {
		select {
		case <-ctx.Done():
			return nil
		case cmd := <-q.queue:
			held = &cmd
			if q.gate(ctx, cmd) != nil {
				return nil
			}
			held = nil
			state, reason := q.model.deliver(cmd)
			q.record(Record{Seq: cmd.Seq, State: state, Command: cmd, Reason: reason, Latency: time.Since(cmd.at)})
		}
	}
}

func (q *commands) shutdown(held *Command) {
	q.mu.Lock()
	q.closed = true
	var canceled []Command
	if held != nil {
		canceled = append(canceled, *held)
	}
	for drained := false; !drained; {
		select {
		case cmd := <-q.queue:
			canceled = append(canceled, cmd)
		default:
			drained = true
		}
	}
	for _, cmd := range canceled {
		q.record(Record{Seq: cmd.Seq, State: "canceled", Command: cmd, Reason: "shutdown before the device received it", Latency: time.Since(cmd.at)})
	}
	q.mu.Unlock()
	q.model.cancel(canceled)
}
