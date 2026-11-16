package webkit

import "github.com/michaelquigley/dfx2/internal/platform"

// Audit is the attachment's observable behavior, in the shape every native
// host reports, so one audition fixture drives either.
type Audit = platform.BrowserAudit

// Event is one navigation-policy outcome.
type Event = platform.BrowserEvent
