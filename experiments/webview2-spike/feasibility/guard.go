// Package feasibility holds the portable pieces of the webview integration
// feasibility spike (docs/future/webview-feasibility-spike.md): an owned
// loopback endpoint's access check, a small probe page and handler, and
// bounded deletion of experiment-owned directories. The Linux and Windows
// hosts share it; native credential installation, request enforcement and
// error pages stay in each host.
//
// This is experiment code. It is not a proposed dfx2 API.
package feasibility

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"
	"sync"
)

// CookieName is the endpoint credential's cookie. It is never logged with
// its value.
const CookieName = "dfx2-endpoint"

// NewToken returns 32 random bytes, URL-safe base64.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Counters separate admission from handler invocation, so a refusal can be
// shown to happen before the application handler ran.
type Counters struct {
	lock sync.Mutex
	v    CounterSnapshot // guarded by lock
}

// CounterSnapshot is a copy of Counters.
type CounterSnapshot struct {
	Admitted int            `json:"admitted"`
	Refused  map[string]int `json:"refused"` // by reason
	Handled  int            `json:"handled"`
}

func (c *Counters) refuse(reason string) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.v.Refused == nil {
		c.v.Refused = map[string]int{}
	}
	c.v.Refused[reason]++
}

func (c *Counters) add(admitted, handled int) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.v.Admitted += admitted
	c.v.Handled += handled
}

// Snapshot copies the counters.
func (c *Counters) Snapshot() CounterSnapshot {
	c.lock.Lock()
	defer c.lock.Unlock()
	out := CounterSnapshot{Admitted: c.v.Admitted, Handled: c.v.Handled, Refused: map[string]int{}}
	for k, v := range c.v.Refused {
		out.Refused[k] = v
	}
	return out
}

// Guard admits a request to next only if it carries the endpoint's
// credential cookie, names the exact assigned Host, and, when it carries an
// Origin, names the exact endpoint origin. A missing Origin is allowed,
// because ordinary document and resource requests omit it, but it never
// substitutes for the credential. Refusals are 403 and never reach next.
// When csp is non-empty it is added to every admitted response as its own
// header; a handler's policy is added alongside, so both apply.
func Guard(next http.Handler, token, host, origin, csp string, c *Counters) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reason := ""
		cookie, err := r.Cookie(CookieName)
		switch {
		case err != nil:
			reason = "no-credential"
		case subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(token)) != 1:
			reason = "wrong-credential"
		case !strings.EqualFold(r.Host, host):
			reason = "host"
		case r.Header.Get("Origin") != "" && r.Header.Get("Origin") != origin:
			reason = "origin"
		}
		if reason != "" {
			c.refuse(reason)
			http.Error(w, "forbidden\n", http.StatusForbidden)
			return
		}
		c.add(1, 0)
		if csp != "" {
			w.Header().Add("Content-Security-Policy", csp)
		}
		stripCredential(r) // the credential is the access layer's, not the application's
		next.ServeHTTP(w, r)
	})
}

// stripCredential removes the endpoint credential from the request's
// cookies and keeps the application's own.
func stripCredential(r *http.Request) {
	var keep []string
	for _, c := range r.Cookies() {
		if c.Name != CookieName {
			keep = append(keep, c.String())
		}
	}
	r.Header.Del("Cookie")
	if len(keep) > 0 {
		r.Header.Set("Cookie", strings.Join(keep, "; "))
	}
}

// Count wraps a handler to count invocations.
func Count(next http.Handler, c *Counters) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.add(0, 1)
		next.ServeHTTP(w, r)
	})
}

// OwnedCSP is the policy an owned endpoint can add for same-origin resources
// and requests, no subframes and same-origin forms. CSP does not govern
// top-level navigation or popups; the host refuses those natively.
const OwnedCSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; font-src 'self'; media-src 'self'; " +
	"object-src 'none'; frame-src 'none'; child-src 'none'; worker-src 'none'; frame-ancestors 'none'; form-action 'self'; base-uri 'self'"
