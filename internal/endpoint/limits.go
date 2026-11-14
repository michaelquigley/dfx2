// Package endpoint adapts window-owned http.Handler endpoints: the finite
// in-process adapter and the owned loopback server. It is portable: it knows
// no GTK, COM or browser types, and it never calls application code on the UI
// thread.
package endpoint

import (
	"errors"
	"fmt"
	"time"
	"unsafe"

	"github.com/michaelquigley/dfx2/internal/platform"
)

// Limits bound one in-process endpoint. Zero fields select defaults.
type Limits struct {
	RequestBody  int64 // bytes; default 1 MiB
	ResponseBody int64 // bytes; default 1 MiB
	Metadata     int   // request or response metadata bytes; default 64 KiB
	Requests     int   // admitted requests; default 16
	Buffers      int64 // aggregate adapter-owned bytes, native copies included; default 64 MiB
}

const (
	DefaultRequestBody  = 1 << 20
	DefaultResponseBody = 1 << 20
	DefaultMetadata     = 64 << 10
	DefaultRequests     = 16
	DefaultBuffers      = 64 << 20

	// ReadChunk bounds one native body read: the browser's read buffer is a
	// native copy charged to the request while reading.
	ReadChunk = 64 << 10
	// Refusals bounds fixed failure responses in native delivery for requests
	// that hold no admission. Their bytes are reserved out of Buffers.
	Refusals       = 16
	refusalReserve = 1024 // per refusal: fixed status, headers and body, measured by a test
	// recordOverhead is charged per request or response metadata record, and
	// headerOverhead per header occurrence, beyond their UTF-8 bytes: the
	// string, slice and map entries that hold one occurrence in Go.
	recordOverhead = platform.RequestRecordOverhead
	headerOverhead = platform.RequestHeaderOverhead
	// minMetadata leaves room for the fixed headers every response carries,
	// the security policy above all.
	minMetadata = refusalReserve
	maxBuffers  = 1 << 50
)

// Normalize applies defaults and rejects negative or inconsistent limits.
func (l Limits) Normalize() (Limits, error) {
	if l.RequestBody < 0 || l.ResponseBody < 0 || l.Metadata < 0 || l.Requests < 0 || l.Buffers < 0 {
		return l, errors.New("in-process limits must not be negative")
	}
	if l.RequestBody == 0 {
		l.RequestBody = DefaultRequestBody
	}
	if l.ResponseBody == 0 {
		l.ResponseBody = DefaultResponseBody
	}
	if l.Metadata == 0 {
		l.Metadata = DefaultMetadata
	}
	if l.Requests == 0 {
		l.Requests = DefaultRequests
	}
	if l.Buffers == 0 {
		l.Buffers = DefaultBuffers
	}
	// Bound every operand before any reservation arithmetic: with each byte
	// limit inside Buffers and Buffers at most 2^50, the worst case and every
	// admission comparison stay far from int64 overflow.
	if l.Buffers > maxBuffers {
		return l, fmt.Errorf("buffer limit %d exceeds %d", l.Buffers, int64(maxBuffers))
	}
	if l.RequestBody > l.Buffers || l.ResponseBody > l.Buffers || int64(l.Metadata) > l.Buffers {
		return l, fmt.Errorf("body and metadata limits must each fit within the buffer limit %d", l.Buffers)
	}
	if l.Metadata < minMetadata {
		return l, fmt.Errorf("metadata limit %d cannot carry the framework's own response headers (minimum %d)", l.Metadata, minMetadata)
	}
	if need := l.reserveRefusals() + l.reserveTransfer() + l.worstCase(); l.Buffers < need {
		return l, fmt.Errorf("buffer limit %d cannot hold one maximal request (%d bytes with the refusal and transfer reserves)", l.Buffers, need)
	}
	return l, nil
}

// urlAllowance reserves extra storage beyond the URL bytes already in metadata.
// Parsing can keep decoded components alongside the input and briefly rebuild
// escaped components (up to 3x their length). Origin canonicalization and
// RequestURI construction also make strings. Eight input lengths plus fixed
// URL/request structure overhead conservatively cover these phases; the phases
// do not overlap. Keep this allowance until the request is fully released.
func urlAllowance(length int64) int64 { return 8*length + 1024 }

// worstCase is the most one admitted request can reserve: metadata and URL
// representation storage, a full body plus its detection byte and native
// read buffer, and a full response
// and its metadata twice, as the Go result and the browser's copy.
func (l Limits) worstCase() int64 {
	return int64(l.Metadata) + urlAllowance(int64(l.Metadata)) + l.RequestBody + 1 + min(ReadChunk, l.RequestBody+1) + l.responseAllowance()
}

func (l Limits) responseAllowance() int64 { return 2 * (l.ResponseBody + int64(l.Metadata)) }

func (l Limits) reserveRefusals() int64 { return Refusals * refusalReserve }

// reserveTransfer covers the browser's transient header slice: the Go
// []platform.Header it builds, from native string views, for the duration of
// one synchronous Request call. Requests arrive one at a time on the UI
// thread, so at most one exists per endpoint, admitted or refused, beside the
// endpoint's own charged copies. Within the metadata limit each occurrence
// is charged at least RequestHeaderOverhead, which bounds its entries.
func (l Limits) reserveTransfer() int64 {
	return int64(l.Metadata) / headerOverhead * int64(unsafe.Sizeof(platform.Header{}))
}

// ServerLimits bound one owned loopback server. Zero fields select defaults.
type ServerLimits struct {
	HeaderBytes       int           // request header bytes; default 64 KiB
	ReadHeaderTimeout time.Duration // default 5 s
	IdleTimeout       time.Duration // default 30 s
	Connections       int           // live connections; default 32
	Handlers          int           // concurrent handlers; default 16
}

const (
	DefaultHeaderBytes       = 64 << 10
	DefaultReadHeaderTimeout = 5 * time.Second
	DefaultIdleTimeout       = 30 * time.Second
	DefaultConnections       = 32
	DefaultHandlers          = 16
)

// Normalize applies defaults and rejects negative limits.
func (l ServerLimits) Normalize() (ServerLimits, error) {
	if l.HeaderBytes < 0 || l.ReadHeaderTimeout < 0 || l.IdleTimeout < 0 || l.Connections < 0 || l.Handlers < 0 {
		return l, errors.New("loopback limits must not be negative")
	}
	if l.HeaderBytes == 0 {
		l.HeaderBytes = DefaultHeaderBytes
	}
	if l.ReadHeaderTimeout == 0 {
		l.ReadHeaderTimeout = DefaultReadHeaderTimeout
	}
	if l.IdleTimeout == 0 {
		l.IdleTimeout = DefaultIdleTimeout
	}
	if l.Connections == 0 {
		l.Connections = DefaultConnections
	}
	if l.Handlers == 0 {
		l.Handlers = DefaultHandlers
	}
	return l, nil
}
