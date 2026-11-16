package platform

// BrowserAudit is a snapshot of a browser attachment's observable behavior,
// for internal auditions and diagnostics. Both native hosts report the same
// shape, so one audition fixture drives either. It carries no credential or
// body.
type BrowserAudit struct {
	Runtime                 string
	Start                   string // the start and Return-to-start URL
	Signal                  int    // the reserved GC signal on Linux; zero elsewhere
	Navigated, Closing      bool
	Destroyed, Released     bool
	Requests, Reads, Copies int
	Asyncs                  int
	Allowed, Refused        int // top-level navigation decisions
	Popups, Permissions     int // refused popups, denied permission requests
	Downloads               int // canceled downloads and refused undisplayable responses
	Delivered               int // in-process responses handed to the engine
	ErrorPages, Actions     int
	Events                  []BrowserEvent // the most recent, oldest first
}

// BrowserEvent is one navigation-policy outcome. Seq numbers a window's
// events from one up, so a reader of the bounded recent list can tell new
// events from those it has seen even after older ones have been dropped.
type BrowserEvent struct {
	Seq    uint64 `json:"seq"`
	Kind   string `json:"kind"`
	URI    string `json:"uri,omitempty"`
	Method string `json:"method,omitempty"`
	Retry  bool   `json:"retry,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// BrowserFocuser is implemented by an attachment whose engine needs to be
// told when its shell gained keyboard focus (WebView2). The core calls Focus
// on the UI thread when the backend reports the shell focused; a browser
// without the method is left alone.
type BrowserFocuser interface {
	Focus()
}
