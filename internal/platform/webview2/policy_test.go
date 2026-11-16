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

// A response counts for the current navigation only when its URI's latest
// document request belongs to the current generation: a redirect's status, a
// fetch of the same URL and an older navigation's response cannot turn a
// failed load into a success.
func TestNavigationCorrelation(t *testing.T) {
	var nav navigation
	const a, b = "http://127.0.0.1:1/a", "http://127.0.0.1:1/b"
	nav.start(1, a, false)
	nav.document(a, "post")
	if nav.method != "POST" {
		t.Fatalf("method %q", nav.method)
	}
	if !nav.response(a) || nav.completed(false, 0, false) != loaded {
		t.Fatal("an answered document request is an ordinary page")
	}
	// The 303 redirects to b, whose request fails: the redirect's status must
	// not count.
	nav.start(1, b, true)
	if nav.responded || nav.method != "POST" {
		t.Fatalf("redirect hop kept the response (%t) or lost the method (%q)", nav.responded, nav.method)
	}
	nav.document(b, "GET")
	if nav.method != "GET" || nav.response(a) || nav.responded {
		t.Fatal("the earlier hop's response counted for the redirect target")
	}
	if nav.completed(false, 7, false) != loadFailed {
		t.Fatal("unanswered document request not a failure")
	}
	// A fetch of b is not a document request; a later navigation to b must not
	// be credited with an earlier generation's document request either.
	nav.start(2, b, false)
	if nav.response(b) {
		t.Fatal("an older generation's document request answered the new navigation")
	}
	nav.document(b, "GET")
	if !nav.response(b) {
		t.Fatal("the current document request's response not counted")
	}
	// Cancellation and closing are not failures.
	nav.start(3, a, false)
	if nav.completed(false, webErrorCanceled, false) != loadStopped || nav.completed(false, 3, true) != loadStopped {
		t.Fatal("cancellation classified as a failure")
	}
	if nav.completed(true, 0, false) != loaded {
		t.Fatal("success not loaded")
	}
}

func TestNavigationDocumentMemoryIsBounded(t *testing.T) {
	var nav navigation
	nav.start(1, "http://h/0", false)
	for i := range 100 {
		nav.document("http://h/"+strings.Repeat("x", i), "GET")
	}
	if len(nav.documents) > documentMemory {
		t.Fatalf("%d document requests remembered", len(nav.documents))
	}
}
