// Package platform defines the private, policy-free native backend boundary.
// Implementations must not depend on appcore, the public API, or content packages.
package platform

type ID uint64

// Counters supports internal native auditions without exposing native handles
// or instrumentation on the public App/Window API.
type Counters struct {
	Wakes, Dispatches uint64
	Windows, Retired  int
}

type WindowOptions struct {
	Title         string
	Width, Height int
}

type Kind uint8

const (
	Wake Kind = iota
	CloseRequested
	Destroyed
	Completed
)

// Event contains numeric routing IDs and Go-owned values, never borrowed native
// memory. Completion IDs settle operations admitted by the core before starting
// native work. Duplicate or stale notifications must be harmless.
type Event struct {
	Kind       Kind
	Window     ID
	Completion ID
	Err        error
}

// Backend owns one dispatch target, independent of its content windows.
// Except Wake, all methods and event delivery run on the owner thread.
// Native callbacks use permanent trampolines and numeric routing IDs.
type Backend interface {
	// Open establishes the owner thread and dispatch target. On error it must
	// release partial initialization itself. Libraries load here, not in init.
	Open(func(Event)) error
	IsOwner() bool
	// Create may deliver events reentrantly. On error no native window remains.
	// On success exactly one Destroyed event is owed, possibly during Create.
	Create(ID, WindowOptions) error
	// Destroy disconnects interactive callbacks and starts teardown. It may
	// deliver events reentrantly. Even on error, Destroyed is still owed;
	// an error is not permission to forget a live native object.
	Destroy(ID) error
	SetTitle(ID, string) error
	// Wake is nonblocking, safe from any goroutine and never delivers inline.
	// It queues a Wake event or returns an error without queuing one.
	Wake() error
	// Pump processes one native dispatch turn, blocking when idle. On failure
	// the target and routing remain valid: the core cancels and keeps pumping.
	Pump() error
	// Release stops queued sources before dropping routing. Called only after
	// all owners, completions and background wake retries have drained.
	Release() error
}
