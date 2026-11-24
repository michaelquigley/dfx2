package webview2

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestErrorPageAndActions(t *testing.T) {
	page := errorPage("http://127.0.0.1:1/x?<b>", "GET", "abc")
	if strings.Contains(page, "<script") || !strings.Contains(page, "&lt;b&gt;") || !strings.Contains(page, errorOrigin+"/retry/abc") || !strings.Contains(page, errorOrigin+"/start/abc") {
		t.Fatalf("GET error page: %s", page)
	}
	if p := errorPage("x", "POST", "n"); strings.Contains(p, "/retry/") || !strings.Contains(p, "/start/n") {
		t.Fatalf("POST error page offers Retry: %s", p)
	}
	if p := errorPage("x", "", "n"); strings.Contains(p, "/retry/") {
		t.Fatal("unknown method offers Retry")
	}
	for uri, want := range map[string][3]any{
		errorOrigin + "/retry/n1": {"retry", "n1", true}, errorOrigin + "/start/n2": {"start", "n2", true},
		errorOrigin + "/reload/n": {"", "", false}, errorOrigin + "/retry/": {"", "", false}, "https://other.invalid/retry/n": {"", "", false},
	} {
		action, n, ok := errorAction(uri)
		if ok != want[2].(bool) || ok && (action != want[0] || n != want[1]) {
			t.Errorf("%s: %q %q %t", uri, action, n, ok)
		}
	}
	if !errorPageURI("data:text/html;charset=utf-8;base64,PCF") || !errorPageURI("about:blank") || errorPageURI("https://dfx2.invalid/") {
		t.Fatal("error-page URI classification")
	}
}

func TestAllowedComparesCanonicalOrigins(t *testing.T) {
	origins := []string{Origin, "http://127.0.0.1:4000"}
	for uri, want := range map[string]bool{
		startURL: true, "https://dfx2.invalid:443/api?x=1": true, "HTTPS://DFX2.INVALID/": true,
		"http://127.0.0.1:4000/": true, "http://127.0.0.1:4001/": false, "http://127.0.0.1/": false, "https://dfx2.invalid.evil/": false,
		"http://dfx2.invalid/": false, errorOrigin + "/retry/x": false, "not a url": false, "data:text/html,x": false,
	} {
		if allowed(uri, origins) != want {
			t.Errorf("%s allowed=%t", uri, !want)
		}
	}
}

func TestWithinComparesPathsCaseInsensitively(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dfx2-webview2-profile-1")
	for _, tc := range []struct {
		path string
		want bool
	}{
		{filepath.Join(dir, "EBWebView", "Default"), true},
		{strings.ToUpper(filepath.Join(dir, "EBWebView")), true},
		{dir, true},
		{filepath.Join(root, "dfx2-webview2-profile-10", "EBWebView"), false},
		{filepath.Join(root, "other-app", "EBWebView"), false},
	} {
		if got := within(tc.path, dir); got != tc.want {
			t.Errorf("within(%q, %q) = %t", tc.path, dir, got)
		}
	}
}

// Completion evidence belongs to its NavigationId, never to a resource URL.
func TestNavigationCorrelation(t *testing.T) {
	var nav navigation
	const a, b = "http://127.0.0.1:1/a", "http://127.0.0.1:1/b"
	nav.start(1, a, false)
	nav.document(a, "post")
	if nav.method != "POST" {
		t.Fatalf("method %q", nav.method)
	}
	nav.start(1, b, true)
	if nav.method != "POST" {
		t.Fatal("redirect lost the method before its request")
	}
	nav.document(a, "PUT")
	if nav.method != "POST" {
		t.Fatal("another URI changed the current method")
	}
	nav.document(b, "GET")
	if nav.method != "GET" {
		t.Fatal("redirect request did not update the method")
	}
	nav.start(2, b, false)
	if nav.method != "" {
		t.Fatal("new navigation kept the old method")
	}
	for _, tc := range []struct {
		name         string
		success      bool
		status, http int32
		closing      bool
		want         loadOutcome
	}{
		{"success without HTTP", true, 0, 0, false, loaded},
		{"stopped page with response", false, 9, 200, false, loaded},
		{"404", false, 16, 404, false, loaded},
		{"500", false, 16, 500, false, loaded},
		{"no response", false, 3, 0, false, loadFailed},
		{"interim response", false, 3, 103, false, loadFailed},
		{"failed 303 target", false, 3, 303, false, loadFailed},
		{"failed 307 target", false, 3, 307, false, loadFailed},
		{"canceled", false, webErrorCanceled, 200, false, loadStopped},
		{"closing", false, 3, 200, true, loadStopped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := nav.completed(tc.success, tc.status, tc.http, tc.closing); got != tc.want {
				t.Fatalf("outcome %v, want %v", got, tc.want)
			}
		})
	}
}
