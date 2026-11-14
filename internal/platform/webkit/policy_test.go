package webkit

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNavigationMethodWithholdsRetryFromUnknownForms(t *testing.T) {
	for _, tc := range []struct {
		method string
		kind   int32
		want   string
		retry  bool
	}{
		{"GET", navigationLinkClicked, "GET", true},
		{"post", navigationFormSubmitted, "POST", false},
		{"", navigationLinkClicked, "GET", true}, // an in-process link or load
		{"", 5, "GET", true},                     // other: a load or Return to start
		{"", navigationFormSubmitted, "", false}, // an in-process form: unknown method
		{"", navigationFormResubmit, "", false},
	} {
		got := navigationMethod(tc.method, tc.kind)
		if got != tc.want || retryable(got) != tc.retry {
			t.Errorf("%q/%d: %q retry %t", tc.method, tc.kind, got, retryable(got))
		}
	}
}

func TestNavigationsKeepEachURIsFinalMethod(t *testing.T) {
	var n navigations
	n.record("http://h/edit", "POST")
	n.record("http://h/done", "GET") // a 303 hop's target
	n.record("http://h/frame", "GET")
	n.record("http://h/again", "POST") // a 307 hop's target keeps POST
	if n.method("http://h/done") != "GET" || n.method("http://h/again") != "POST" || n.method("http://h/edit") != "POST" {
		t.Fatalf("%+v", n)
	}
	if n.method("http://h/never") != "" {
		t.Fatal("unknown navigation has a method")
	}
	n.record("http://h/edit", "GET")
	if n.method("http://h/edit") != "GET" || len(n.uris) != 4 {
		t.Fatalf("rerecord: %+v", n)
	}
	for i := range 40 {
		n.record("http://h/"+strings.Repeat("x", i), "GET")
	}
	if len(n.uris) != navigationMemory || n.method("http://h/again") != "" {
		t.Fatal("memory unbounded or stale entries kept")
	}
}

func TestAllowedComparesParsedOrigins(t *testing.T) {
	origins := []string{"http://127.0.0.1:4000", Origin}
	for uri, want := range map[string]bool{
		"http://127.0.0.1:4000/x":          true,
		"HTTP://127.0.0.1:4000/":           true,
		"dfx2://app/api":                   true,
		"http://127.0.0.1:4001/":           false,
		"http://127.0.0.1:4000.evil.test/": false,
		"http://localhost:4000/":           false,
		"dfx2-error:retry/x":               false,
		"about:blank":                      false,
		"http://user@127.0.0.1:4000/":      false,
	} {
		if allowed(uri, origins) != want {
			t.Errorf("%s: %t", uri, !want)
		}
	}
}

func TestFilterRulesConfineOwnedWindowsAndRefuseFrames(t *testing.T) {
	var owned []map[string]any
	if err := json.Unmarshal([]byte(filterRules("http://127.0.0.1:4000")), &owned); err != nil {
		t.Fatal(err)
	}
	if len(owned) != 3 || owned[0]["action"].(map[string]any)["type"] != "block" ||
		owned[1]["trigger"].(map[string]any)["url-filter"] != `^http://127\.0\.0\.1:4000/` ||
		owned[2]["trigger"].(map[string]any)["load-context"].([]any)[0] != "child-frame" {
		t.Fatalf("owned rules %v", owned)
	}
	var attached []map[string]any
	json.Unmarshal([]byte(filterRules("")), &attached)
	if len(attached) != 1 || attached[0]["trigger"].(map[string]any)["resource-type"].([]any)[0] != "document" {
		t.Fatalf("attachment rules %v", attached)
	}
}

func TestErrorPageEscapesAndOffersRetryOnlyForGET(t *testing.T) {
	page := errorPage(`http://h/"><script>alert(1)</script>`, "GET", "n1")
	if strings.Contains(page, "<script") || !strings.Contains(page, "dfx2-error:retry/n1") || !strings.Contains(page, "dfx2-error:start/n1") {
		t.Fatal(page)
	}
	for _, method := range []string{"POST", ""} {
		if p := errorPage("http://h/", method, "n2"); strings.Contains(p, "retry/") || !strings.Contains(p, "start/n2") {
			t.Fatalf("%q: %s", method, p)
		}
	}
	for uri, want := range map[string]bool{"dfx2-error:retry/abc": true, "dfx2-error:start/abc": true,
		"dfx2-error:other/abc": false, "dfx2-error:retry/": false, "dfx2-error:retry": false, "http://h/": false} {
		if _, _, ok := errorAction(uri); ok != want {
			t.Errorf("%s: %t", uri, ok)
		}
	}
	if a, b := nonce(), nonce(); a == b || len(a) != 32 {
		t.Fatal("nonces")
	}
}
