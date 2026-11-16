//go:build windows && amd64

package main

// The request-body threading probe (-feasibility -feas-body inspect|worker).
//
// Production reads an intercepted request's body stream synchronously on the
// UI thread. This probe establishes what the incoming stream is and whether
// it can be read somewhere else:
//
//   - On the UI thread, before anything reads it, each incoming stream is
//     queried for IAgileObject and IMarshal (and its marshaler's class),
//     ISequentialStream, Stat and its current position. None of those move
//     the stream.
//   - In "inspect" mode the body is then read on the UI thread, as production
//     does, and the read is recorded.
//   - In "worker" mode a stream that advertises IAgileObject is handed as is
//     to a goroutine locked to its own OS thread, initialized with
//     CoInitializeEx(COINIT_MULTITHREADED), which reads it in bounded chunks,
//     releases it, and uninitializes. A stream without the marker is passed
//     through CoMarshalInterThreadInterfaceInStream instead and unmarshaled
//     on that thread; whether such a read really executes off the UI is
//     decided by the stall option, which blocks the UI thread (no message
//     pumping) right after the worker starts and records whether the read
//     finished meanwhile. Event arguments, the deferral and every WebView2
//     call stay on the UI thread; the worker posts its result back.
//   - A host-implemented agile IStream whose Read blocks on a gate gives the
//     scheduling and close evidence independently of what the browser's
//     streams are: the worker holds in Read while the UI keeps going; a
//     close settles native interception on the UI thread, retains the
//     worker, its stream and buffer, and the window's release waits for the
//     read to return, once.
//
// Cancellation never pretends a read returned: a read in flight at close
// keeps its window from releasing until it does.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"dfx2webview2spike/adapt"
)

var (
	pCoMarshalInterThreadInterfaceInStream = ole32.NewProc("CoMarshalInterThreadInterfaceInStream")
	pCoGetInterfaceAndReleaseStream        = ole32.NewProc("CoGetInterfaceAndReleaseStream")

	iidIStream                 = mustGUID("0000000c-0000-0000-c000-000000000046")
	iidISequentialStream       = mustGUID("0c733a30-2a1c-11ce-ade5-00aa0044773d")
	clsidFreeThreadedMarshaler = mustGUID("0000033a-0000-0000-c000-000000000046") // CLSID_InProcFreeMarshaler
)

const (
	streamSeek  = 5  // IStream::Seek
	streamStat  = 12 // IStream::Stat
	streamSlots = 14

	marshalGetUnmarshalClass = 3 // IMarshal::GetUnmarshalClass
	mshctxInproc             = 3 // MSHCTX_INPROC
	statNoName               = 1 // STATFLAG_NONAME
	seekCurrent              = 1 // STREAM_SEEK_CUR
	coinitMultithreaded      = 0

	stgInvalidFunction hresult = 0x80030001
	stgAccessDenied    hresult = 0x80030005
	eNotImpl           hresult = 0x80004001
	stgTypeStream              = 2

	timerTicks = 4 // the controller window's UI progress counter while reads are in flight
	bodyChunk  = 64 << 10
)

// streamFacts is what the UI thread learns about an incoming stream without
// consuming or repositioning it.
type streamFacts struct {
	Pointer               string `json:"pointer"`
	Agile                 string `json:"agile"`              // IAgileObject QueryInterface
	Marshal               string `json:"marshal"`            // IMarshal QueryInterface
	UnmarshalClass        string `json:"unmarshal_class"`    // IMarshal::GetUnmarshalClass for MSHCTX_INPROC, when IMarshal exists
	UnmarshalClassHR      string `json:"unmarshal_class_hr"` // its HRESULT
	FreeThreadedMarshaler bool   `json:"free_threaded_marshaler"`
	Sequential            string `json:"sequential_stream"` // ISequentialStream QueryInterface
	Stat                  string `json:"stat"`              // IStream::Stat(STATFLAG_NONAME)
	StatSize              uint64 `json:"stat_size"`
	StatType              uint32 `json:"stat_type"`
	Position              int64  `json:"position"` // Seek(0, STREAM_SEEK_CUR)
	PositionHR            string `json:"position_hr"`
}

// statstg is STATSTG on x64: 80 bytes.
type statstg struct {
	name              uintptr
	kind              uint32
	_                 uint32
	size              uint64
	mtime, ctime, atm uint64
	mode, locks       uint32
	clsid             guid
	stateBits, _      uint32
}

func inspectStream(s *com) streamFacts {
	f := streamFacts{Pointer: fmt.Sprintf("%#x", s.ptr())}
	agile, hr := s.queryInterface(&iidIAgileObject)
	agile.release()
	f.Agile = hr.String()
	seq, hr := s.queryInterface(&iidISequentialStream)
	seq.release()
	f.Sequential = hr.String()
	marshal, hr := s.queryInterface(&iidIMarshal)
	f.Marshal = hr.String()
	if marshal != nil {
		var cls guid
		hr := marshal.call(marshalGetUnmarshalClass, uintptr(unsafe.Pointer(&iidIStream)), s.ptr(), mshctxInproc, 0, 0, uintptr(unsafe.Pointer(&cls)))
		f.UnmarshalClassHR = hr.String()
		if !hr.failed() {
			f.UnmarshalClass = guidString(cls)
			f.FreeThreadedMarshaler = cls == clsidFreeThreadedMarshaler
		}
		marshal.release()
	}
	var st statstg
	hr = s.call(streamStat, uintptr(unsafe.Pointer(&st)), statNoName)
	f.Stat = hr.String()
	if !hr.failed() {
		f.StatSize, f.StatType = st.size, st.kind
		if st.name != 0 {
			pCoTaskMemFree.Call(st.name)
		}
	}
	var pos uint64
	hr = s.call(streamSeek, 0, seekCurrent, uintptr(unsafe.Pointer(&pos)))
	f.PositionHR = hr.String()
	f.Position = int64(pos)
	return f
}

// bodyRead is one request body being read by a worker, or by the UI thread
// in inspect mode. It lives in its window's reads map until the read has
// returned and its result was handled on the UI thread.
type bodyRead struct {
	id      int
	w       *fwin
	in      adapt.Request
	kind    string
	stallMS int

	args, deferral *com // native interception, UI thread only; nil for a modeled read
	modeled        bool
	branch         string // "agile", "marshal", "ui", "modeled-agile"
	facts          streamFacts
	stream         *com    // handed to the worker: the raw agile pointer, or the marshal stream
	original       uintptr // the incoming pointer, to compare with what unmarshals
	gate           *gateStream

	start    time.Time
	ticksAt  uint64
	done     atomic.Int32 // the worker finished its read loop
	doneAt   atomic.Int64 // UnixNano
	stallHit bool         // the read finished while the UI thread was blocked

	settled     int // native interception settlements: exactly one for a real read
	closedEarly bool
	closedAt    time.Time
}

type bodyResult struct {
	thread      uint32
	coinit      hresult
	unmarshal   hresult
	samePointer bool
	bytes       []byte
	tooLarge    bool
	err         error
	chunks      int
	firstMS     float64
	totalMS     float64
	maxGapMS    float64
}

// probeBody replaces the UI-thread body read when -feas-body is set. It is
// called on the UI thread inside WebResourceRequested with the request's
// headers already copied; it takes the deferral and starts the handler.
func (f *feas) probeBody(w *fwin, args, req *com, in adapt.Request) hresult {
	var body *com
	if req.call(reqGetContent, uintptr(unsafe.Pointer(&body))) != sOK || body == nil {
		return f.startHandler(w, args, in, nil)
	}
	kind, stall := "", 0
	if u, err := url.Parse(in.URL); err == nil {
		kind = u.Query().Get("kind")
		stall, _ = strconv.Atoi(u.Query().Get("stall"))
	}
	facts := inspectStream(body)
	f.nextRead++
	r := &bodyRead{id: f.nextRead, w: w, in: in, kind: kind, stallMS: stall, facts: facts, original: body.ptr(), start: time.Now(), ticksAt: f.ticks}
	f.event("body-stream", "window", w.label, "id", r.id, "method", in.Method, "uri", in.URL, "kind", kind, "mode", f.c.feas.bodyMode,
		"content_type", header(in, "Content-Type"), "content_length", header(in, "Content-Length"), "facts", facts, "ui_thread", f.thread, "closing", w.closing)

	if f.c.feas.bodyMode != "worker" {
		// inspect: the production path, recorded.
		r.branch = "ui"
		res := bodyResult{thread: currentThread()}
		res.bytes, res.tooLarge, res.err = readChunks(body, adapt.Limit, &res)
		body.release()
		r.done.Store(1)
		r.doneAt.Store(time.Now().UnixNano())
		f.recordRead(r, res, "body-read")
		if res.err != nil {
			f.respond(w, args, synthetic(http.StatusBadRequest, "request body unreadable: "+res.err.Error()))
			return sOK
		}
		in.Body, in.BodyTooLarge = res.bytes, res.tooLarge
		return f.startHandler(w, args, in, nil)
	}

	var def *com
	if hr := args.call(wrrGetDeferral, uintptr(unsafe.Pointer(&def))); hr.failed() {
		body.release()
		return sOK
	}
	args.addRef()
	r.args, r.deferral = args, def
	switch {
	case facts.Agile == "S_OK":
		// Documented sufficient condition: callable from any apartment. The
		// reference get_Content gave us goes to the worker as is.
		r.branch, r.stream = "agile", body
	default:
		// Not advertised agile: hand it over through COM's own marshaling,
		// and let the stall test say where the read runs.
		var ms *com
		hr := hresult(callProc(pCoMarshalInterThreadInterfaceInStream, uintptr(unsafe.Pointer(&iidIStream)), body.ptr(), uintptr(unsafe.Pointer(&ms))))
		body.release()
		if hr.failed() || ms == nil {
			f.event("body-marshal-failed", "window", w.label, "id", r.id, "hresult", hr.String())
			r.branch = "ui"
			// Nothing to read off the UI: refuse rather than read the stream
			// whose only reference was just released. Production would decide
			// its policy from this probe's record.
			f.settle(w, 0, &pending{args: args, deferral: def, what: in.Method + " " + in.URL, start: r.start}, synthetic(http.StatusNotImplemented, "request body stream cannot be read off the UI thread"), "refused")
			r.settled++
			f.event("body-read", "window", w.label, "id", r.id, "kind", kind, "branch", r.branch, "error", "marshal failed: "+hr.String())
			return sOK
		}
		r.branch, r.stream = "marshal", ms
	}
	w.reads[r.id] = r
	f.startWorker(r)
	if stall > 0 {
		// Block this thread without pumping: a read dispatched back to this
		// apartment cannot complete until the stall ends.
		t := time.Now()
		time.Sleep(time.Duration(stall) * time.Millisecond)
		r.stallHit = r.done.Load() == 1
		f.event("ui-stall", "window", w.label, "id", r.id, "kind", kind, "stall_ms", stall, "actual_ms", time.Since(t).Milliseconds(), "read_done_during_stall", r.stallHit)
	}
	return sOK
}

func header(in adapt.Request, name string) string {
	for _, h := range in.Header {
		if http.CanonicalHeaderKey(h[0]) == name {
			return h[1]
		}
	}
	return ""
}

// startHandler takes the deferral and starts the handler: the tail of the
// ordinary interception path, for a request whose body is in hand.
func (f *feas) startHandler(w *fwin, args *com, in adapt.Request, start *time.Time) hresult {
	var def *com
	if hr := args.call(wrrGetDeferral, uintptr(unsafe.Pointer(&def))); hr.failed() {
		return sOK
	}
	args.addRef()
	t := time.Now()
	if start != nil {
		t = *start
	}
	id, ok := w.dispatcher.Start(in)
	p := &pending{args: args, deferral: def, what: in.Method + " " + in.URL, start: t}
	if !ok {
		f.settle(w, 0, p, synthetic(http.StatusServiceUnavailable, "window closing"), "refused")
		return sOK
	}
	w.pending[id] = p
	return sOK
}

// startWorker reads r.stream on a goroutine locked to its own OS thread, a
// multithreaded-apartment member for the duration. The worker touches only
// the stream (Read, Release) and COM initialization; it posts its result to
// the UI thread, which owns everything else.
func (f *feas) startWorker(r *bodyRead) {
	f.workers.Add(1)
	f.startTicks()
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		res := bodyResult{thread: currentThread()}
		res.coinit = hresult(callProc(pCoInitializeEx, 0, coinitMultithreaded))
		f.event("body-worker", "window", r.w.label, "id", r.id, "branch", r.branch, "thread", res.thread, "coinit", res.coinit.String())
		stream := r.stream
		if r.branch == "marshal" {
			var p *com
			res.unmarshal = hresult(callProc(pCoGetInterfaceAndReleaseStream, stream.ptr(), uintptr(unsafe.Pointer(&iidIStream)), uintptr(unsafe.Pointer(&p))))
			stream = nil
			if !res.unmarshal.failed() && p != nil {
				stream = p
				res.samePointer = p.ptr() == r.original
			} else {
				res.err = res.unmarshal.err("CoGetInterfaceAndReleaseStream")
			}
		}
		if stream != nil {
			res.bytes, res.tooLarge, res.err = readChunks(stream, adapt.Limit, &res)
			stream.release() // the worker's reference: before CoUninitialize
		}
		r.done.Store(1)
		r.doneAt.Store(time.Now().UnixNano())
		if res.coinit == sOK || res.coinit == sFalse {
			pCoUninitialize.Call()
		}
		f.workers.Add(-1)
		if !f.post(func() { f.readDone(r, res) }) {
			f.event("body-read-orphaned", "window", r.w.label, "id", r.id, "bytes", len(res.bytes))
		}
	}()
}

// readChunks reads up to limit+1 bytes in bounded chunks, timing each Read.
func readChunks(s *com, limit int, rec *bodyResult) (b []byte, tooLarge bool, err error) {
	buf := make([]byte, bodyChunk)
	start := time.Now()
	for {
		want := min(len(buf), limit+1-len(b))
		var n uint32
		t := time.Now()
		hr := s.call(streamRead, uintptr(unsafe.Pointer(&buf[0])), uintptr(want), uintptr(unsafe.Pointer(&n)))
		gap := ms(time.Since(t))
		rec.chunks++
		if rec.chunks == 1 {
			rec.firstMS = gap
		}
		rec.maxGapMS = max(rec.maxGapMS, gap)
		rec.totalMS = ms(time.Since(start))
		if hr.failed() {
			return b, false, hr.err("IStream::Read")
		}
		b = append(b, buf[:n]...)
		if len(b) > limit {
			return b, true, nil
		}
		if n == 0 || hr == sFalse {
			return b, false, nil
		}
	}
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// readDone runs on the UI thread with the worker's result. A read whose
// window closed meanwhile is dropped here, exactly once, and lets the
// window's release proceed.
func (f *feas) readDone(r *bodyRead, res bodyResult) {
	w := r.w
	delete(w.reads, r.id)
	f.stopTicksIfIdle()
	if r.closedEarly || w.closing {
		f.recordRead(r, res, "body-read-after-close")
		if !r.modeled && r.settled != 1 {
			f.event("error", "window", w.label, "error", fmt.Sprintf("read %d settled %d times", r.id, r.settled))
		}
		f.maybeRelease(w)
		return
	}
	f.recordRead(r, res, "body-read")
	if r.modeled {
		return
	}
	p := &pending{args: r.args, deferral: r.deferral, what: r.in.Method + " " + r.in.URL, start: r.start}
	if res.err != nil {
		f.settle(w, 0, p, synthetic(http.StatusBadRequest, "request body unreadable: "+res.err.Error()), "unreadable")
		r.settled++
		return
	}
	r.in.Body, r.in.BodyTooLarge = res.bytes, res.tooLarge
	id, ok := w.dispatcher.Start(r.in)
	if !ok {
		f.settle(w, 0, p, synthetic(http.StatusServiceUnavailable, "window closing"), "refused")
		r.settled++
		return
	}
	w.pending[id] = p
	r.settled++ // settled later with its response; counted as this read's one settlement
}

func (f *feas) recordRead(r *bodyRead, res bodyResult, name string) {
	sum := sha256.Sum256(res.bytes)
	kv := []any{"window", r.w.label, "id", r.id, "kind", r.kind, "branch", r.branch, "modeled", r.modeled,
		"reader_thread", res.thread, "ui_thread", f.thread, "off_ui", res.thread != f.thread,
		"coinit", res.coinit.String(), "bytes", len(res.bytes), "too_large", res.tooLarge, "sha256", hex.EncodeToString(sum[:]),
		"chunks", res.chunks, "first_read_ms", res.firstMS, "total_read_ms", res.totalMS, "max_read_gap_ms", res.maxGapMS,
		"ticks_during", f.ticks - r.ticksAt, "stall_ms", r.stallMS, "read_done_during_stall", r.stallHit,
		"took_ms", ms(time.Since(r.start)), "error", errString(res.err), "closed_early", r.closedEarly, "settled", r.settled}
	if r.branch == "marshal" {
		kv = append(kv, "unmarshal", res.unmarshal.String(), "same_pointer", res.samePointer)
	}
	if r.gate != nil {
		kv = append(kv, "read_callback_threads", r.gate.threads(), "gate_releases", r.gate.released.Load())
	}
	if r.closedEarly {
		kv = append(kv, "after_close_ms", ms(time.Since(r.closedAt)))
	}
	f.event(name, kv...)
}

// closeReads settles native interception for every read in flight when its
// window closes, on the UI thread, and keeps the reads themselves: their
// workers still own their streams and buffers until Read returns.
func (f *feas) closeReads(w *fwin) {
	for _, r := range w.reads {
		if r.closedEarly {
			continue
		}
		r.closedEarly, r.closedAt = true, time.Now()
		if !r.modeled {
			f.settle(w, 0, &pending{args: r.args, deferral: r.deferral, what: r.in.Method + " " + r.in.URL, start: r.start}, synthetic(http.StatusServiceUnavailable, "window closing"), "settled on close")
			r.settled++
		}
		f.event("read-retained-at-close", "window", w.label, "id", r.id, "kind", r.kind, "branch", r.branch, "modeled", r.modeled, "settled", r.settled, "read_done", r.done.Load() == 1)
	}
}

// UI progress ticks: a 10 ms timer on the controller window while any read
// is in flight. The count only moves when this thread pumps messages.
func (f *feas) startTicks() {
	if !f.ticking {
		f.ticking = true
		pSetTimer.Call(f.ctrl, timerTicks, 10, 0)
	}
}

func (f *feas) stopTicksIfIdle() {
	for _, w := range f.windows {
		if len(w.reads) > 0 {
			return
		}
	}
	if f.ticking {
		f.ticking = false
		pKillTimer.Call(f.ctrl, timerTicks)
	}
}

// ---------------------------------------------------------------- gate stream

// gateStream is a host-implemented agile, read-only IStream whose Read blocks
// until the gate opens. It is the modeled stand-in for an incoming stream
// that does not return promptly: the scheduling and close evidence it gives
// says nothing about the browser's own streams.
type gateStream struct {
	obj      *cbObject
	id       uintptr
	data     []byte
	mu       sync.Mutex
	pos      int64
	gate     chan struct{}
	reads    []uint32 // the thread each Read ran on
	released atomic.Int32
	label    string
}

var (
	gateVtbl     [streamSlots]uintptr
	gateInitOnce sync.Once
	gateMu       sync.Mutex
	gateByID     = map[uintptr]*gateStream{}
	gateNextID   uintptr
)

func initGate() {
	gateInitOnce.Do(func() {
		gateVtbl = [streamSlots]uintptr{
			syscall.NewCallback(gateQueryInterface), syscall.NewCallback(gateAddRef), syscall.NewCallback(gateRelease),
			syscall.NewCallback(gateRead), syscall.NewCallback(gateWrite), syscall.NewCallback(gateSeek),
			syscall.NewCallback(gateInvalid1), syscall.NewCallback(gateCopyTo), syscall.NewCallback(gateOK1), syscall.NewCallback(gateOK0),
			syscall.NewCallback(gateInvalid3), syscall.NewCallback(gateInvalid3), syscall.NewCallback(gateStat), syscall.NewCallback(gateClone)}
	})
}

// newGateStream makes a stream holding one reference, the caller's.
func newGateStream(label string, n int) *gateStream {
	initGate()
	data := make([]byte, n)
	for i := range data {
		data[i] = byte(i*31 + 7)
	}
	p, _, _ := pCoTaskMemAlloc.Call(unsafe.Sizeof(cbObject{}))
	if p == 0 {
		panic("CoTaskMemAlloc failed")
	}
	obj := (*cbObject)(unsafe.Add(unsafe.Pointer(nil), p))
	gateMu.Lock()
	gateNextID++
	g := &gateStream{obj: obj, id: gateNextID, data: data, gate: make(chan struct{}), label: label}
	gateByID[g.id] = g
	gateMu.Unlock()
	*obj = cbObject{vtbl: uintptr(unsafe.Pointer(&gateVtbl[0])), refs: 1, id: g.id}
	return g
}

func (g *gateStream) com() *com { return asCOM(uintptr(unsafe.Pointer(g.obj))) }

func (g *gateStream) threads() []uint32 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]uint32(nil), g.reads...)
}

func lookupGate(this *cbObject) *gateStream {
	gateMu.Lock()
	defer gateMu.Unlock()
	return gateByID[this.id]
}

func liveGates() int {
	gateMu.Lock()
	defer gateMu.Unlock()
	return len(gateByID)
}

func gateQueryInterface(this *cbObject, riid *guid, ppv *uintptr) uintptr {
	if ppv == nil {
		return ePointer
	}
	if *riid == iidIUnknown || *riid == iidISequentialStream || *riid == iidIStream || *riid == iidIAgileObject {
		atomic.AddInt32(&this.refs, 1)
		*ppv = uintptr(unsafe.Pointer(this))
		return sOK
	}
	*ppv = 0
	return eNoInterface
}

func gateAddRef(this *cbObject) uintptr { return uintptr(atomic.AddInt32(&this.refs, 1)) }

func gateRelease(this *cbObject) uintptr {
	n := atomic.AddInt32(&this.refs, -1)
	if n == 0 {
		g := lookupGate(this)
		gateMu.Lock()
		delete(gateByID, this.id)
		gateMu.Unlock()
		pCoTaskMemFree.Call(uintptr(unsafe.Pointer(this)))
		if g != nil {
			count := g.released.Add(1)
			if f := feasActive; f != nil {
				f.event("gate-stream-released", "window", g.label, "thread", currentThread(), "final_releases", count)
			}
		}
	}
	return uintptr(n)
}

// gateRead blocks on the gate, then serves the data; it records the thread
// it runs on, which is the reader's only if no proxy stands between.
func gateRead(this *cbObject, pv uintptr, cb uint32, pcbRead *uint32) uintptr {
	g := lookupGate(this)
	if g == nil || pv == 0 {
		return uintptr(ePointer)
	}
	t := currentThread()
	g.mu.Lock()
	g.reads = append(g.reads, t)
	g.mu.Unlock()
	<-g.gate
	g.mu.Lock()
	defer g.mu.Unlock()
	remaining := int64(len(g.data)) - g.pos
	count := min(int64(cb), max(remaining, 0))
	if count > 0 {
		copy(unsafe.Slice((*byte)(unsafe.Add(unsafe.Pointer(nil), pv)), count), g.data[g.pos:g.pos+count])
		g.pos += count
	}
	if pcbRead != nil {
		*pcbRead = uint32(count)
	}
	if count < int64(cb) {
		return uintptr(sFalse)
	}
	return uintptr(sOK)
}

func gateWrite(this *cbObject, pv uintptr, cb uint32, pcbWritten *uint32) uintptr {
	if pcbWritten != nil {
		*pcbWritten = 0
	}
	return uintptr(stgAccessDenied)
}

func gateSeek(this *cbObject, move uintptr, origin uint32, newPos *uint64) uintptr {
	g := lookupGate(this)
	if g == nil {
		return uintptr(ePointer)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	base := int64(0)
	switch origin {
	case 0:
	case 1:
		base = g.pos
	case 2:
		base = int64(len(g.data))
	default:
		return uintptr(stgInvalidFunction)
	}
	target := base + int64(move)
	if target < 0 {
		return uintptr(stgInvalidFunction)
	}
	g.pos = target
	if newPos != nil {
		*newPos = uint64(target)
	}
	return uintptr(sOK)
}

func gateInvalid1(this *cbObject, a uintptr) uintptr       { return uintptr(stgInvalidFunction) }
func gateInvalid3(this *cbObject, a, b, c uintptr) uintptr { return uintptr(stgInvalidFunction) }
func gateOK1(this *cbObject, a uintptr) uintptr            { return uintptr(sOK) }
func gateOK0(this *cbObject) uintptr                       { return uintptr(sOK) }
func gateCopyTo(this *cbObject, a, b, c, d uintptr) uintptr {
	return uintptr(eNotImpl)
}

func gateStat(this *cbObject, pstat *statstg, flags uint32) uintptr {
	g := lookupGate(this)
	if g == nil || pstat == nil {
		return uintptr(ePointer)
	}
	*pstat = statstg{kind: stgTypeStream, size: uint64(len(g.data))}
	return uintptr(sOK)
}

func gateClone(this *cbObject, ppstm *uintptr) uintptr {
	if ppstm != nil {
		*ppstm = 0
	}
	return uintptr(eNotImpl)
}

// gateRead starts a worker on a new gate stream for window w: the modeled
// read. The stream's one reference is the worker's.
func (f *feas) gateRead(w *fwin, n int) {
	g := newGateStream(w.label, n)
	f.nextRead++
	r := &bodyRead{id: f.nextRead, w: w, kind: "gate", modeled: true, branch: "modeled-agile", gate: g, stream: g.com(), start: time.Now(), ticksAt: f.ticks}
	r.facts = inspectStream(g.com())
	w.reads[r.id] = r
	f.gates = append(f.gates, g)
	f.event("gate-read-started", "window", w.label, "id", r.id, "bytes", n, "facts", r.facts, "ui_thread", f.thread)
	f.startWorker(r)
}

// gateRelease opens every gate: the held reads return.
func (f *feas) gateRelease() {
	n := 0
	for _, g := range f.gates {
		select {
		case <-g.gate:
		default:
			close(g.gate)
			n++
		}
	}
	f.event("gate-released", "opened", n, "ui_thread", f.thread)
}
