package adapt

import (
	"net/http"
)

// buffer is a finite http.ResponseWriter. It implements only the three
// ResponseWriter methods: no Flusher, Hijacker, Pusher or io.ReaderFrom, so
// a handler (or http.ResponseController) that asks for streaming learns it
// isn't available instead of being silently buffered.
type buffer struct {
	header      http.Header
	sent        http.Header // the header as of WriteHeader, as net/http snapshots it
	status      int
	body        []byte
	limit       int
	overflow    bool
	wroteHeader bool
}

func newBuffer(limit int) *buffer { return &buffer{header: http.Header{}, limit: limit} }

func (b *buffer) Header() http.Header { return b.header }

func (b *buffer) WriteHeader(code int) {
	if b.wroteHeader {
		return // net/http logs "superfluous WriteHeader"; the first status wins
	}
	if code < 100 || code > 999 {
		panic("adapt: invalid WriteHeader code") // as net/http does
	}
	if code < 200 {
		return // informational: nothing to buffer, the final status follows
	}
	b.wroteHeader = true
	b.status = code
	b.sent = b.header.Clone()
}

func (b *buffer) Write(p []byte) (int, error) {
	if !b.wroteHeader {
		if _, ok := b.header["Content-Type"]; !ok && len(p) > 0 {
			// net/http sniffs from the first write when the handler set no
			// type; do the same so both modes deliver the same type.
			b.header.Set("Content-Type", http.DetectContentType(p))
		}
		b.WriteHeader(http.StatusOK)
	}
	if b.overflow {
		return 0, ErrBodyTooLarge
	}
	if len(b.body)+len(p) > b.limit {
		b.overflow = true
		return 0, ErrBodyTooLarge
	}
	b.body = append(b.body, p...)
	return len(p), nil
}

func (b *buffer) finish() Response {
	if !b.wroteHeader {
		b.WriteHeader(http.StatusOK)
	}
	return Response{Status: b.status, Reason: http.StatusText(b.status), Header: b.sent, Body: b.body}
}
