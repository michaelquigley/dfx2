package platform

// The browser contract joins a BrowserContent shell to a private browser
// attachment. The backend owns the shell; a browser factory, chosen by the
// public webview package, owns the browser objects inside it; the core owns
// both lifetimes and routes their notifications by numeric identity. Browser
// packages import this contract, never the core, HTTP handlers or public
// content packages. Every method here runs on the UI thread unless it says
// otherwise, and no Go pointer passes into native ownership.

// BrowserParent describes a live BrowserContent shell to the matching browser
// factory. It never escapes through public API.
type BrowserParent struct {
	Window ID
	// Handle is the native parent: a GtkContainer on GTK, an HWND on Win32.
	// It is borrowed. The core destroys the shell only after the browser has
	// reported Released, so it stays valid for the browser's whole lifetime.
	Handle  uintptr
	Surface Surface // copied bounds at attachment time
}

type BrowserMode uint8

const (
	// BrowserInProcess serves the page from a backend-assigned origin whose
	// requests arrive through BrowserHost.Request.
	BrowserInProcess BrowserMode = iota + 1
	// BrowserLoopback loads an owned loopback server's origin, authenticated
	// by Credential.
	BrowserLoopback
	// BrowserAttach loads an independently owned URL.
	BrowserAttach
)

// BrowserCookie is a credential the browser installs as an HttpOnly session
// cookie, scoped to Origin, in the window's isolated session. Never log it.
type BrowserCookie struct {
	Name, Value string
	Origin      string // canonical scheme://host:port
}

// BrowserConfig is copied into the browser at creation.
type BrowserConfig struct {
	Mode BrowserMode
	// StartURL is the absolute first and Return-to-start URL for loopback and
	// attachment. In-process browsers start at "/" on their own origin.
	StartURL string
	// Origins are the canonical origins top-level navigation may reach. Owned
	// loopback lists its endpoint alone; in-process leaves it empty, meaning
	// the browser's own origin.
	Origins []string
	// Credential gates the first navigation: it must be installed first.
	// Loopback only.
	Credential *BrowserCookie
	// DevTools enables the engine's inspector, for auditions.
	DevTools bool
	// RequestMetadata is the in-process endpoint's request metadata limit,
	// set by the endpoint when it starts. A browser measures each native
	// request against it, by RequestRecordOverhead and RequestHeaderOverhead,
	// before building anything from it.
	RequestMetadata int
	// GCSignal is the real-time signal the application reserves for the
	// engine's garbage collector, where the engine needs one (Linux). The
	// first reservation is process-wide; others are ignored.
	GCSignal int
}

// BrowserFactory creates browser attachments for one platform. A factory is
// selected by the public webview package, never by common startup, so
// native-only applications do not load browser libraries.
type BrowserFactory interface {
	// New attaches a browser to the shell, beginning no navigation. On error no
	// native browser object remains and nothing is owed. On success Released
	// is owed exactly once, after Close.
	New(BrowserParent, BrowserConfig, BrowserHost) (Browser, error)
}

// Browser is one window's private browser attachment.
type Browser interface {
	// Origin is the canonical origin an in-process browser assigned, such as
	// a custom scheme or reserved host; empty in other modes. Stable after New.
	Origin() string
	// Start begins asynchronous initialization: isolated session, credential,
	// request confinement and policy, then the start page once host.Ready
	// allows it. It may call host methods reentrantly. Unexpected failures are
	// reported through host.Failed, never by blocking.
	Start()
	// Bounds forwards copied shell bounds.
	Bounds(Surface)
	// Respond installs a finite response for an in-process request that is
	// waiting for one. Body is borrowed for the call; the browser copies what
	// it keeps. False means the native request is already gone. Either way the
	// request's RequestReleased is still owed or already delivered.
	Respond(ID, BrowserResponse) bool
	// Close begins teardown and is called once. Pending native requests are
	// canceled, each still owing RequestReleased. Released follows, possibly
	// reentrantly, once no native object or callback uses the parent.
	Close()
	// Diagnostics summarizes outstanding native state, without credentials or
	// bodies. It is called from the supervisor goroutine while the UI thread may
	// be stalled: it must not block on the UI thread or call host methods.
	Diagnostics() string
}

// BrowserHost is the core's side of one attachment.
type BrowserHost interface {
	// Ready reports that the browser may begin its first navigation. False
	// means the window is closing: release instead of navigating.
	Ready() bool
	// Failed reports an unexpected initialization, native-host or
	// browser-process failure. It cancels and drains the application. A failed
	// page load or refused navigation is not such a failure.
	Failed(error)
	// Released reports that no native object or callback still uses the
	// parent. Every request has been released already.
	Released()
	// Cleanup runs work off the UI thread as owned work, after native release
	// of what it removes. Errors and recovered panics cancel the application
	// and remain in Run's result; only a directly returned FileDeletionResidue
	// with a path and cause is retained without cancellation. Call before Released.
	Cleanup(name string, work func() error) error

	// Request admits one in-process request. Unless the decision's ID is zero,
	// RequestReleased(ID) is owed once the native request is finished, whatever
	// the decision. Strings may view native memory; the host copies what it keeps.
	Request(BrowserRequest) RequestDecision
	// RequestBody delivers the next body bytes, at most ReadChunk. The slice is
	// borrowed for the call and may view native memory.
	RequestBody(ID, []byte) RequestDecision
	// RequestBodyEnd ends the body. A non-nil error means the native read
	// failed; the decision is then RequestAbort.
	RequestBodyEnd(ID, error) RequestDecision
	// RequestReleased reports that the browser no longer uses the request:
	// answered, refused, aborted or canceled.
	RequestReleased(ID)
}

// Request metadata is charged as the UTF-8 bytes of the method, URL and every
// header name and value, plus these per-record and per-occurrence overheads.
// A browser and the endpoint measure with the same rule.
const (
	RequestRecordOverhead = 64
	RequestHeaderOverhead = 64
)

// Header is one header field occurrence, in order.
type Header struct{ Name, Value string }

// BrowserRequest is copied from a native request before admission.
type BrowserRequest struct {
	Method string
	URL    string // absolute, as the browser presented it
	Header []Header
	// BodyLength is the declared body size: 0 none, -1 unknown.
	BodyLength int64
	// MetadataSize is the browser's own measurement of the native request's
	// metadata. Past BrowserConfig.RequestMetadata the browser may stop
	// counting, and then supplies no Header; the endpoint uses the larger of
	// this and its own measurement, so such a request is refused as oversized.
	MetadataSize int64
}

type RequestAction uint8

const (
	// RequestRead asks for body bytes: RequestBody, at most ReadChunk bytes at
	// a time, then RequestBodyEnd.
	RequestRead RequestAction = iota + 1
	// RequestWait means the handler is running; Respond follows unless the
	// request is canceled first.
	RequestWait
	// RequestRespond answers at once with Response, a fixed failure.
	RequestRespond
	// RequestAbort fails the request at the browser API without a response.
	RequestAbort
)

type RequestDecision struct {
	ID        ID
	Action    RequestAction
	ReadChunk int
	Response  BrowserResponse
}

// BrowserResponse is a complete finite response.
type BrowserResponse struct {
	Status int
	Header []Header
	Body   []byte
}
