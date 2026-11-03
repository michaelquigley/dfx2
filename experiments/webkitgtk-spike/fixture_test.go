package main

import (
	"bytes"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"dfx2webview2spike/fixture"
)

// Catch drift in the shared fixture's origin expression: silently failing to
// transform it would make the large-body checks expect the wrong transport.
func TestFixtureTransportDetection(t *testing.T) {
	h := fixtureHandler(func(string, ...any) {})
	for _, path := range []string{"/", "/app.js"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || strings.Contains(w.Body.String(), "WebView2") || !strings.Contains(w.Body.String(), "WebKitGTK") {
			t.Fatalf("%s: labels/status", path)
		}
		if w.Header().Get("Content-Length") != strconv.Itoa(w.Body.Len()) {
			t.Fatal("wrong transformed length")
		}
		if path == "/app.js" && !strings.Contains(w.Body.String(), `const mode = location.protocol === "dfx2:" ? "inprocess" : "loopback";`) {
			t.Fatal("shared fixture changed; update transport detection")
		}
	}
}
func TestFixtureBinaryUnchanged(t *testing.T) {
	w := httptest.NewRecorder()
	fixtureHandler(func(string, ...any) {}).ServeHTTP(w, httptest.NewRequest("GET", "/api/binary", nil))
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), fixture.Binary) {
		t.Fatal("binary fixture changed")
	}
}
