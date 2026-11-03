package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"

	"dfx2webview2spike/fixture"
)

// Keep the same handler and checks. Only labels and transport detection
// change: WebKit's registered scheme cannot use WebView2's HTTPS origin.
func fixtureHandler(logf func(string, ...any)) http.Handler {
	base := fixture.Handler(logf)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/", "/index.html", "/app.js":
			rec := httptest.NewRecorder()
			base.ServeHTTP(rec, r)
			body := bytes.ReplaceAll(rec.Body.Bytes(), []byte("WebView2"), []byte("WebKitGTK"))
			if r.URL.Path == "/app.js" {
				body = bytes.ReplaceAll(body,
					[]byte(`const mode = location.origin === INPROCESS_ORIGIN ? "inprocess" : "loopback";`),
					[]byte(`const mode = location.protocol === "dfx2:" ? "inprocess" : "loopback";`))
			}
			for k, vs := range rec.Header() {
				w.Header()[k] = vs
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(rec.Code)
			w.Write(body)
		default:
			base.ServeHTTP(w, r)
		}
	})
}
