// Package appcore implements application policy without native dependencies.
package appcore

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/michaelquigley/dfx2/internal/platform"
)

var (
	ErrAlreadyRun          = errors.New("dfx2: Run already called")
	ErrUnsupportedPlatform = errors.New("dfx2: unsupported platform")
	ErrWrongThread         = errors.New("dfx2: operation requires UI thread")
	ErrClosed              = errors.New("dfx2: owner closing or closed")
	ErrBusy                = errors.New("dfx2: post queue full")
)

type Mode uint8

const (
	WindowApplication Mode = iota
	ResidentApplication
)

type Options struct {
	Mode Mode
	// Logf reports shutdown diagnostics and wake failures from the runtime's
	// background supervisor, independently of UI dispatch. It must be fast,
	// concurrency-safe with the caller's logging, and must not call UI methods.
	// Nil uses log.Printf. Panics in Logf are contained by the runtime.
	Logf func(string, ...any)
}

type WindowOptions struct {
	Title         string
	Width, Height int // Logical client dimensions; both must be positive.
}

type state uint8

const (
	starting state = iota
	open
	closing
	closed
)

func (s state) String() string { return [...]string{"starting", "open", "closing", "closed"}[s] }

type owner struct {
	id     platform.ID
	ctx    context.Context
	cancel context.CancelFunc
	state  state
	queue  []func()
	tasks  map[platform.ID]*task
}

type task struct {
	name     string
	finished bool
	err      error
}

// App owns the UI loop and its windows. Use the instance supplied to Run;
// the zero value is not an application.
type App struct {
	mu sync.Mutex
	owner
	backend          platform.Backend
	clock            clock
	options          Options
	parent           context.Context
	windows          map[platform.ID]*Window
	operations       map[platform.ID]*completion
	next             platform.ID
	setupDone        bool
	terminal         bool
	dispatchReleased bool
	wakePending      bool
	wakeFailures     int
	wakeErr          error
	wakeLog          bool
	failure          error
	cleanup          []error
	callerErr        error
	changed          chan struct{}
	stop             chan struct{}
	supervised       chan struct{}
	wakesStopped     chan struct{}
	inTurn           bool        // setup or a dispatch turn is on the UI thread's stack
	frameCursor      platform.ID // rotating frame order across content windows
	frameTimers      uint64      // animation deadline timers armed, for auditions
	inputLog         bool        // an input overflow awaits the supervisor's diagnostic
}

// Window owns one independent native window and its registered tasks.
// Use NewWindow; the zero value is not a window.
type Window struct {
	app *App
	owner
	creating       bool
	native         bool
	destroyStarted bool
	attachment     *attachment // UI content bridge; nil for ordinary windows
	releasing      bool        // native teardown done; content release in progress
	done           chan struct{}
}

type completion struct {
	window   platform.ID
	ready    bool
	err      error
	settle   func(isClosing bool) error
	settling bool
}

type timer interface {
	C() <-chan time.Time
	Stop()
}

// clock is monotonic: frame deadlines and animation use Now's monotonic reading.
type clock interface {
	NewTimer(time.Duration) timer
	Now() time.Time
}
type realClock struct{}
type realTimer struct{ *time.Timer }

func (realClock) NewTimer(d time.Duration) timer { return realTimer{time.NewTimer(d)} }
func (realClock) Now() time.Time                 { return time.Now() }
func (t realTimer) C() <-chan time.Time          { return t.Timer.C }
func (t realTimer) Stop()                        { t.Timer.Stop() }
