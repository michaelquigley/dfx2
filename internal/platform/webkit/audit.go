package webkit

// Audit is a snapshot of an attachment's observable behavior, for internal
// auditions and diagnostics. It carries no credential or body.
type Audit struct {
	Runtime                 string
	Start                   string // the start and Return-to-start URL
	Signal                  int
	Navigated, Closing      bool
	Destroyed, Released     bool
	Requests, Reads, Copies int
	Asyncs                  int
	Allowed, Refused        int // top-level navigation decisions
	Popups, Permissions     int // refused popups, denied permission requests
	Downloads               int // canceled downloads and refused undisplayable responses
	Delivered               int // in-process responses handed to WebKit
	ErrorPages, Actions     int
	Events                  []Event // the most recent, oldest first
}

// Event is one navigation-policy outcome.
type Event struct {
	Kind   string `json:"kind"`
	URI    string `json:"uri,omitempty"`
	Method string `json:"method,omitempty"`
	Retry  bool   `json:"retry,omitempty"`
	Detail string `json:"detail,omitempty"`
}
