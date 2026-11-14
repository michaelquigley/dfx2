// Package webkit hosts browser attachments in WebKitGTK 4.1 on Linux/amd64,
// through purego, inside the GTK backend's main loop. Libraries load on the
// first attachment, never at application startup, so native-only applications
// do not load WebKit or JavaScriptCore.
//
// The Go process creates no JavaScriptCore VM: it binds no JavaScript
// evaluation, result conversion or script-message API, whose first use would
// install JavaScriptCore's fault handlers in this process. Browser JavaScript
// runs only in WebKit's child processes.
//
// This file holds the portable policy decisions, testable on any platform.
package webkit

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"html"
	"regexp"
	"strings"

	"github.com/michaelquigley/dfx2/internal/origin"
)

// Origin is the in-process origin. Each window has its own web context, so
// the same origin never reaches another window's endpoint or storage.
const Origin = "dfx2://app"

const scheme = "dfx2"

// errorScheme carries error-page actions. The browser intercepts them before
// any load; the markup supplies an action and a nonce, never a destination.
const errorScheme = "dfx2-error:"

// WebKitNavigationType values.
const (
	navigationLinkClicked   = 0
	navigationFormSubmitted = 1
	navigationFormResubmit  = 4
)

// navigationMethod classifies a top-level navigation's method for Retry.
// Custom schemes report no HTTP method: a form submission or resubmission is
// then unknown, which withholds Retry, while other in-process navigations are
// GETs. An explicit method is upper-cased.
func navigationMethod(method string, navigationType int32) string {
	if method != "" {
		return strings.ToUpper(method)
	}
	if navigationType == navigationFormSubmitted || navigationType == navigationFormResubmit {
		return ""
	}
	return "GET"
}

// retryable reports whether Retry may repeat a failed navigation: only a
// known GET, judged by its final attempted method across redirects.
func retryable(method string) bool { return method == "GET" }

// navigations remembers the method of recent allowed navigations by URI.
// load-failed reports only the failing main-frame URI, and decide-policy also
// sees subframe navigations, so a per-URI record keeps an allowed subframe
// navigation from rewriting the main frame's method. Redirect hops record
// their own targets, so the failing URI carries its final method.
type navigations struct {
	uris    []string
	methods []string
}

const navigationMemory = 16

func (n *navigations) record(uri, method string) {
	for i, u := range n.uris {
		if u == uri {
			n.uris = append(n.uris[:i], n.uris[i+1:]...)
			n.methods = append(n.methods[:i], n.methods[i+1:]...)
			break
		}
	}
	if len(n.uris) == navigationMemory {
		n.uris, n.methods = n.uris[1:], n.methods[1:]
	}
	n.uris = append(n.uris, uri)
	n.methods = append(n.methods, method)
}

// method returns the recorded method of uri; unknown is "".
func (n *navigations) method(uri string) string {
	for i := len(n.uris) - 1; i >= 0; i-- {
		if n.uris[i] == uri {
			return n.methods[i]
		}
	}
	return ""
}

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

// filterRules returns WebKit content-blocker rules. Owned windows block every
// load outside their origin before a request leaves the web process; every
// window blocks child-frame documents. Attachments keep their own subresource
// policy: only the frame rule applies to them.
func filterRules(owned string) string {
	var rules []map[string]any
	if owned != "" {
		rules = append(rules,
			map[string]any{"trigger": map[string]any{"url-filter": ".*"}, "action": map[string]any{"type": "block"}},
			map[string]any{"trigger": map[string]any{"url-filter": "^" + regexp.QuoteMeta(owned) + "/"},
				"action": map[string]any{"type": "ignore-previous-rules"}})
	}
	rules = append(rules, map[string]any{
		"trigger": map[string]any{"url-filter": ".*", "resource-type": []string{"document"}, "load-context": []string{"child-frame"}},
		"action":  map[string]any{"type": "block"}})
	b, _ := json.Marshal(rules)
	return string(b)
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
		retry = `<a class="b" id="retry" href="` + errorScheme + `retry/` + nonce + `">Retry</a> `
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
		`<p>` + retry + `<a class="b" id="start" href="` + errorScheme + `start/` + nonce + `">Return to start</a></p>`
}

// errorAction parses an error-page action URI.
func errorAction(uri string) (action, nonce string, ok bool) {
	rest, found := strings.CutPrefix(uri, errorScheme)
	if !found {
		return "", "", false
	}
	action, nonce, ok = strings.Cut(rest, "/")
	return action, nonce, ok && (action == "retry" || action == "start") && nonce != ""
}
