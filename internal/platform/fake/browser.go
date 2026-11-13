package fake

import (
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/michaelquigley/dfx2/internal/platform"
)

// BrowserFactory creates controllable browsers. Configure it before use.
// Hooks run on the UI thread inside the call they name and may call host
// methods reentrantly. Tests drive later native callbacks through
// Backend.Call, so that they arrive on the UI thread outside dispatch turns.
type BrowserFactory struct {
	NewError error
	// Origin is the in-process origin browsers assign; default
	// "fake://browser".
	Origin  string
	OnNew   func(*Browser)
	OnStart func(*Browser)
	OnClose func(*Browser)
	// DelayRelease keeps a closed browser's native requests and its Released
	// notification pending until Release is called.
	DelayRelease bool
	// ReleaseOnRespond releases a request reentrantly from inside Respond, as
	// a native stream freed at once would.
	ReleaseOnRespond bool

	mu       sync.Mutex
	browsers []*Browser
	created  chan *Browser
}

// Created delivers each browser New returns, in order.
func (f *BrowserFactory) Created() <-chan *Browser {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.created == nil {
		f.created = make(chan *Browser, 64)
	}
	return f.created
}

func (f *BrowserFactory) New(parent platform.BrowserParent, config platform.BrowserConfig, host platform.BrowserHost) (platform.Browser, error) {
	if f.NewError != nil {
		return nil, f.NewError
	}
	origin := f.Origin
	if origin == "" {
		origin = "fake://browser"
	}
	b := &Browser{factory: f, Parent: parent, Config: config, Host: host, origin: origin, requests: make(map[platform.ID]bool),
		responses: make(map[platform.ID]platform.BrowserResponse)}
	if config.Credential != nil {
		c := *config.Credential
		b.Config.Credential = &c
	}
	b.Config.Origins = slices.Clone(config.Origins)
	f.Created() // allocate the channel under the lock
	f.mu.Lock()
	f.browsers = append(f.browsers, b)
	created := f.created
	f.mu.Unlock()
	if f.OnNew != nil {
		f.OnNew(b)
	}
	created <- b
	return b, nil
}

// Browser is one fake attachment. Its exported fields are fixed at New.
type Browser struct {
	factory *BrowserFactory
	Parent  platform.BrowserParent
	Config  platform.BrowserConfig
	Host    platform.BrowserHost
	origin  string

	mu         sync.Mutex
	started    bool
	navigated  bool
	closed     bool
	released   bool
	requests   map[platform.ID]bool // live native requests
	responses  map[platform.ID]platform.BrowserResponse
	bounds     []platform.Surface
	violations []string
}

func (b *Browser) Origin() string { return b.origin }

func (b *Browser) violate(format string, args ...any) {
	b.mu.Lock()
	b.violations = append(b.violations, fmt.Sprintf(format, args...))
	b.mu.Unlock()
}

func (b *Browser) Start() {
	b.mu.Lock()
	if b.started {
		b.violations = append(b.violations, "Start called twice")
	}
	b.started = true
	b.mu.Unlock()
	if b.factory.OnStart != nil {
		b.factory.OnStart(b)
	}
}

func (b *Browser) Bounds(s platform.Surface) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		b.violations = append(b.violations, "bounds after Close")
	}
	b.bounds = append(b.bounds, s)
}

// Respond records a copy of the response for a live request.
func (b *Browser) Respond(id platform.ID, r platform.BrowserResponse) bool {
	b.mu.Lock()
	live := b.requests[id]
	if _, again := b.responses[id]; again {
		b.violations = append(b.violations, fmt.Sprintf("request %d answered twice", id))
	}
	if b.closed {
		b.violations = append(b.violations, fmt.Sprintf("request %d answered after Close", id))
	}
	if live {
		r.Header = slices.Clone(r.Header)
		r.Body = slices.Clone(r.Body)
		b.responses[id] = r
	}
	b.mu.Unlock()
	if live && b.factory.ReleaseOnRespond {
		b.Finish(id)
	}
	return live
}

// Close cancels live requests and releases at once unless DelayRelease.
func (b *Browser) Close() {
	b.mu.Lock()
	if b.closed {
		b.violations = append(b.violations, "Close called twice")
	}
	b.closed = true
	b.mu.Unlock()
	if b.factory.OnClose != nil {
		b.factory.OnClose(b)
	}
	if !b.factory.DelayRelease {
		b.Release()
	}
}

// Release cancels every live request, then reports Released. UI thread.
func (b *Browser) Release() {
	b.mu.Lock()
	if b.released {
		b.mu.Unlock()
		return
	}
	b.released = true
	var ids []platform.ID
	for id := range b.requests {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	b.mu.Unlock()
	for _, id := range ids {
		b.Finish(id)
	}
	b.Host.Released()
}

func (b *Browser) Diagnostics() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return fmt.Sprintf("fake browser closed=%t released=%t native-requests=%d", b.closed, b.released, len(b.requests))
}

// Ready asks the host whether to navigate, as a browser finishing its
// initialization would. UI thread.
func (b *Browser) Ready() bool {
	ok := b.Host.Ready()
	b.mu.Lock()
	defer b.mu.Unlock()
	if ok {
		if b.closed {
			b.violations = append(b.violations, "navigation after Close")
		}
		b.navigated = true
	}
	return ok
}

// Request delivers a native request; a nonzero decision ID stays live until
// Finish. UI thread.
func (b *Browser) Request(r platform.BrowserRequest) platform.RequestDecision {
	d := b.Host.Request(r)
	if d.ID != 0 {
		b.mu.Lock()
		if b.requests[d.ID] {
			b.violations = append(b.violations, fmt.Sprintf("request ID %d reused while live", d.ID))
		}
		b.requests[d.ID] = true
		b.mu.Unlock()
	}
	return d
}

func (b *Browser) Body(id platform.ID, data []byte) platform.RequestDecision {
	return b.Host.RequestBody(id, data)
}

func (b *Browser) BodyEnd(id platform.ID, err error) platform.RequestDecision {
	return b.Host.RequestBodyEnd(id, err)
}

// Finish releases one live request, as the native request object ending.
func (b *Browser) Finish(id platform.ID) {
	b.mu.Lock()
	live := b.requests[id]
	delete(b.requests, id)
	b.mu.Unlock()
	if live {
		b.Host.RequestReleased(id)
	}
}

// Response returns the recorded response of a request.
func (b *Browser) Response(id platform.ID) (platform.BrowserResponse, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, ok := b.responses[id]
	return r, ok
}

// State reports lifecycle flags and live native request count.
func (b *Browser) State() (started, navigated, closed, released bool, live int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.started, b.navigated, b.closed, b.released, len(b.requests)
}

// Bounded returns forwarded bounds.
func (b *Browser) Bounded() []platform.Surface {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.bounds)
}

// Violations lists contract misuse observed by this browser.
func (b *Browser) Violations() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.violations)
}

// ErrFake is a convenient injected failure.
var ErrFake = errors.New("fake browser failure")
