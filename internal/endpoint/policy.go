package endpoint

import (
	"fmt"
	"net/http"
	"runtime/debug"
	"strconv"

	"github.com/michaelquigley/dfx2/internal/platform"
)

// ContentSecurityPolicy is the framework baseline for every owned document
// and response: scripts, styles, assets and connections stay on the assigned
// origin; no objects, child frames, workers (service workers included) or
// external form targets. Handlers may add their own policies, which browsers
// enforce in addition, so they can tighten it but never relax or remove it.
const ContentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; font-src 'self'; " +
	"connect-src 'self'; media-src 'self'; object-src 'none'; frame-src 'none'; child-src 'none'; worker-src 'none'; " +
	"manifest-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'self'"

// failure bodies are fixed and tiny; they never echo request data or stacks.
var failures = map[int]string{
	http.StatusBadRequest:                  "bad request\n",
	http.StatusForbidden:                   "forbidden\n",
	http.StatusRequestEntityTooLarge:       "request body too large\n",
	http.StatusRequestHeaderFieldsTooLarge: "request metadata too large\n",
	http.StatusInternalServerError:         "internal server error\n",
	http.StatusNotImplemented:              "not implemented\n",
	http.StatusBadGateway:                  "response could not be delivered\n",
	http.StatusServiceUnavailable:          "endpoint busy or closing\n",
}

func failureBody(status int) string {
	if body, ok := failures[status]; ok {
		return body
	}
	panic(fmt.Sprintf("endpoint: no fixed failure for status %d", status))
}

// policy adds the framework baseline. Every owned response carries it;
// handler policies add to it, and browsers enforce all of them.
func policy(w http.ResponseWriter) {
	w.Header().Add("Content-Security-Policy", ContentSecurityPolicy)
}

// writeFailure writes a fixed failure through a loopback ResponseWriter.
func writeFailure(w http.ResponseWriter, status int) {
	body := failureBody(status)
	policy(w)
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// failureResponse is a fixed failure for native delivery. Its strings are
// constants, so building it allocates only the small header slice.
func failureResponse(status int) platform.BrowserResponse {
	body := failureBody(status)
	return platform.BrowserResponse{Status: status, Header: []platform.Header{
		{Name: "Content-Type", Value: "text/plain; charset=utf-8"},
		{Name: "X-Content-Type-Options", Value: "nosniff"},
		{Name: "Cache-Control", Value: "no-store"},
		{Name: "Content-Security-Policy", Value: ContentSecurityPolicy},
	}, Body: []byte(body)}
}

// recoverHandler classifies a handler panic. http.ErrAbortHandler is a
// deliberate request abort, not an application failure; any other panic is
// returned as an error carrying its stack.
func recoverHandler(p any) (abort bool, err error) {
	if p == http.ErrAbortHandler {
		return true, nil
	}
	return false, fmt.Errorf("http handler panic: %v\n%s", p, debug.Stack())
}
