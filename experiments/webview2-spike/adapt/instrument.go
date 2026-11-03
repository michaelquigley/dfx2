package adapt

import (
	"net/http"
	"sync"
	"time"
)

// Instrument logs loopback requests in the dispatcher's vocabulary (ids,
// start, done or canceled, outstanding), so the two modes' logs read the
// same. Loopback ids are their own sequence.
func Instrument(h http.Handler, logf Logf) http.Handler {
	var (
		mu          sync.Mutex
		next        uint64
		outstanding int
	)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		next++
		id := next
		outstanding++
		n := outstanding
		mu.Unlock()
		logf("req %d start %s %s from %s (%d B body, outstanding %d)", id, r.Method, r.RequestURI, r.RemoteAddr, max(r.ContentLength, 0), n)

		sw := &statusWriter{ResponseWriter: w}
		start := time.Now()
		h.ServeHTTP(sw, r)
		el := time.Since(start).Round(time.Millisecond)

		mu.Lock()
		outstanding--
		n = outstanding
		mu.Unlock()
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		if err := r.Context().Err(); err != nil {
			logf("req %d handler canceled (%v), returned %d in %s (outstanding %d)", id, err, sw.status, el, n)
			return
		}
		logf("req %d handler done %d in %s (outstanding %d)", id, sw.status, el, n)
	})
}

// statusWriter records the status. It deliberately forwards Unwrap so
// http.ResponseController reaches the real loopback writer: loopback is
// ordinary net/http and keeps its streaming abilities.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 && code >= 200 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(p []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(p)
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }
