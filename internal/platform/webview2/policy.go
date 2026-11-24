// Package webview2 hosts browser attachments in the system WebView2 Runtime
// on Windows/amd64, through COM over syscall, inside the Win32 backend's
// message loop. The pinned WebView2 loader is embedded in the executable and
// extracted on the first attachment, never at application startup, so
// native-only applications load no browser library.
//
// The host evaluates no JavaScript and exposes no host objects or web
// messages to the page. Page JavaScript runs only in the WebView2 Runtime's
// own processes.
//
// This file holds the portable policy decisions, testable on any platform.
package webview2

import (
	"crypto/rand"
	"encoding/hex"
	"html"
	"strings"

	"github.com/michaelquigley/dfx2/internal/origin"
)

// Origin is the in-process origin. Every window names the same origin, and
// each window's own request interception routes it to that window's endpoint
// alone. The .invalid top-level domain can never resolve, so a request that
// escaped interception would fail rather than reach a network host.
const (
	originHost = "dfx2.invalid"
	// Origin is canonical, with the effective port, as the origin package and
	// the endpoint compare origins; the browser itself shows https://dfx2.invalid.
	Origin   = "https://" + originHost + ":443"
	startURL = "https://" + originHost + "/"
)

// errorOrigin is where the error page's actions point. The host cancels every
// navigation to it before any load and acts on what it recorded; the markup
// supplies an action and a nonce, never a destination.
const errorOrigin = "https://dfx2-error.invalid"

// retryable reports whether Retry may repeat a failed navigation: only a
// known GET, judged by the final attempted document request's method.
func retryable(method string) bool { return method == "GET" }

// allowed reports whether uri's origin is one of the canonical origins.
func allowed(uri string, origins []string) bool {
	o, err := origin.Of(uri)
	if err != nil {
		return false
	}
	for _, a := range origins {
		if a == o {
			return true
		}
	}
	return false
}

// errorPageURI reports whether a navigation is the host's own error document:
// NavigateToString navigates to a data: URI of the markup, which gets an
// opaque origin. It is admitted only while that document is pending.
func errorPageURI(uri string) bool {
	return strings.HasPrefix(uri, "data:text/html") || strings.HasPrefix(uri, "about:blank")
}

func nonce() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// errorPage is the framework's local load-failure document: no script, no
// credentials, the failing URI escaped. Retry appears only for a known GET.
func errorPage(failing, method, nonce string) string {
	retry := ""
	if retryable(method) {
		retry = `<a class="b" id="retry" href="` + errorOrigin + `/retry/` + nonce + `">Retry</a> `
	}
	note := "Nothing was submitted."
	if !retryable(method) {
		note = "The request that failed may have submitted a form; it was not repeated."
	}
	return `<!doctype html><meta charset="utf-8"><title>Page did not load</title>` +
		`<style>body{font:15px sans-serif;margin:3em;background:#1e1f24;color:#e8e6e3}` +
		`.b{display:inline-block;width:180px;height:44px;line-height:44px;margin-right:20px;text-align:center;` +
		`background:#4fa3f7;color:#000;text-decoration:none;border-radius:4px}</style>` +
		`<h1>This page did not load</h1><p><code>` + html.EscapeString(failing) + `</code></p><p>` + note + `</p>` +
		`<p>` + retry + `<a class="b" id="start" href="` + errorOrigin + `/start/` + nonce + `">Return to start</a></p>`
}

// errorAction parses an error-page action URI.
func errorAction(uri string) (action, nonce string, ok bool) {
	rest, found := strings.CutPrefix(uri, errorOrigin+"/")
	if !found {
		return "", "", false
	}
	action, nonce, ok = strings.Cut(rest, "/")
	return action, nonce, ok && (action == "retry" || action == "start") && nonce != ""
}

// navigation follows the current top-level navigation. Only its completion
// supplies response evidence: resource-response events have no NavigationId
// and cannot distinguish a document from another request to the same URI.
// The method comes from the intercepted document-context request.
type navigation struct {
	id     uint64 // WebView2's NavigationId
	uri    string
	method string // from the document request; "" until seen
}

// start begins a navigation or redirect hop. A redirect keeps the method of
// the navigation it continues until the next document request reports one.
func (n *navigation) start(id uint64, uri string, redirected bool) {
	n.id, n.uri = id, uri
	if !redirected {
		n.method = ""
	}
}

// document records the current document-context request's method.
func (n *navigation) document(uri, method string) {
	if uri == n.uri {
		n.method = strings.ToUpper(method)
	}
}

// loadOutcome classifies a completed top-level navigation.
type loadOutcome uint8

const (
	loaded      loadOutcome = iota // the page displays, whatever its HTTP status
	loadStopped                    // canceled by policy, superseded or closing
	loadFailed                     // no response: the error page follows
)

// completed uses only the matching NavigationCompleted event. Final HTTP
// responses display normally, including 404/500 and a page that called stop().
// A 1xx/3xx status is not evidence that the final document answered: WebView2
// can retain a redirect's status when its destination fails to load.
func (n *navigation) completed(success bool, status, httpStatus int32, closing bool) loadOutcome {
	switch {
	case success:
		return loaded
	case status == webErrorCanceled || closing:
		return loadStopped
	case httpStatus >= 200 && httpStatus < 300, httpStatus >= 400 && httpStatus < 600:
		return loaded
	}
	return loadFailed
}
