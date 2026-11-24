package webview2

import (
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// COM objects the host implements: completion and event handlers, and the
// IStream that carries a response body.
//
// Every entry point is made once for the process: syscall callbacks are never
// freed. Objects route to their Go state by an id stored in the object, never
// by a Go pointer. Each object lives in CoTaskMemAlloc memory, outside the Go
// heap, so its address is stable and its reference count alone decides its
// life; native code never holds a pointer into Go memory. The shared vtables
// are package-level arrays, rooted for the life of the process.

type comObject struct {
	vtbl  uintptr
	refs  int32
	taken int32 // the engine took a reference of its own
	id    uintptr
}

type handlerObject struct {
	id      uintptr
	name    string
	iid     guid
	obj     *comObject
	invoke  func(a, b uintptr) hresult
	dropped func()
	invoked atomic.Bool
}

func (h *handlerObject) ptr() uintptr { return uintptr(unsafe.Pointer(h.obj)) }
func (h *handlerObject) release()     { comRelease(h.obj) }

// streamObject is one IStream over a shared body. Clones share the body and
// its release notification; each has its own position.
type streamObject struct {
	id   uintptr
	obj  *comObject
	mu   sync.Mutex
	pos  int64
	body *streamBody
}

type streamBody struct {
	data     []byte
	streams  int32 // objects sharing it
	released func()
}

var objects struct {
	mu       sync.Mutex
	next     uintptr
	handlers map[uintptr]*handlerObject
	streams  map[uintptr]*streamObject
	offUI    int
	uiThread uint32

	once        sync.Once
	handlerVtbl [4]uintptr
	streamVtbl  [streamSlots]uintptr
}

func initObjects() {
	objects.once.Do(func() {
		objects.handlers = make(map[uintptr]*handlerObject)
		objects.streams = make(map[uintptr]*streamObject)
		objects.handlerVtbl = [4]uintptr{
			syscall.NewCallback(handlerQueryInterface), syscall.NewCallback(comAddRef), syscall.NewCallback(comRelease), syscall.NewCallback(handlerInvokeEntry)}
		objects.streamVtbl = [streamSlots]uintptr{
			syscall.NewCallback(streamQueryInterface), syscall.NewCallback(comAddRef), syscall.NewCallback(comRelease),
			syscall.NewCallback(streamReadEntry), syscall.NewCallback(streamWriteEntry), syscall.NewCallback(streamSeekEntry),
			syscall.NewCallback(streamInvalid), syscall.NewCallback(streamCopyToEntry), syscall.NewCallback(streamOK), syscall.NewCallback(streamOK),
			syscall.NewCallback(streamInvalid3), syscall.NewCallback(streamInvalid3), syscall.NewCallback(streamStatEntry), syscall.NewCallback(streamCloneEntry)}
	})
}

var allocateCOMObject = func() *comObject {
	p, _, _ := pCoTaskMemAlloc.Call(unsafe.Sizeof(comObject{}))
	return (*comObject)(unsafe.Add(unsafe.Pointer(nil), p))
}

func allocObject(vtbl *uintptr) *comObject {
	obj := allocateCOMObject()
	if obj == nil {
		return nil
	}
	*obj = comObject{vtbl: uintptr(unsafe.Pointer(vtbl)), refs: 1}
	return obj
}

// newHandlerObject makes a handler holding one reference, the host's. The
// host releases it once the engine has taken its own (after the add_ or
// Create call returns). dropped runs if the engine releases the object without
// invoking it: the two cannot both settle one completion.
func newHandlerObject(name string, iid guid, invoke func(a, b uintptr) hresult, dropped func()) handler {
	initObjects()
	obj := allocObject(&objects.handlerVtbl[0])
	if obj == nil {
		return nil
	}
	objects.mu.Lock()
	objects.next++
	obj.id = objects.next
	h := &handlerObject{id: obj.id, obj: obj, name: name, iid: iid, invoke: invoke, dropped: dropped}
	objects.handlers[h.id] = h
	objects.mu.Unlock()
	return h
}

func lookupHandler(this *comObject) *handlerObject {
	objects.mu.Lock()
	defer objects.mu.Unlock()
	return objects.handlers[this.id]
}

func handlerQueryInterface(this *comObject, riid *guid, ppv *uintptr) uintptr {
	if ppv == nil {
		return uintptr(ePointer)
	}
	h := lookupHandler(this)
	if h != nil && (*riid == iidIUnknown || *riid == h.iid) {
		atomic.AddInt32(&this.refs, 1)
		atomic.StoreInt32(&this.taken, 1)
		*ppv = uintptr(unsafe.Pointer(this))
		return uintptr(sOK)
	}
	*ppv = 0
	return uintptr(eNoInterface)
}

func comAddRef(this *comObject) uintptr {
	atomic.StoreInt32(&this.taken, 1)
	return uintptr(atomic.AddInt32(&this.refs, 1))
}

func comRelease(this *comObject) uintptr {
	n := atomic.AddInt32(&this.refs, -1)
	if n != 0 {
		return uintptr(n)
	}
	taken := atomic.LoadInt32(&this.taken) == 1
	objects.mu.Lock()
	h := objects.handlers[this.id]
	s := objects.streams[this.id]
	delete(objects.handlers, this.id)
	delete(objects.streams, this.id)
	objects.mu.Unlock()
	pCoTaskMemFree.Call(uintptr(unsafe.Pointer(this)))
	switch {
	case h != nil && taken && !h.invoked.Load() && h.dropped != nil:
		h.dropped()
	case s != nil:
		s.body.release()
	}
	return 0
}

func (sb *streamBody) release() {
	if atomic.AddInt32(&sb.streams, -1) == 0 && sb.released != nil {
		sb.released()
	}
}

func handlerInvokeEntry(this *comObject, a, b uintptr) uintptr {
	h := lookupHandler(this)
	if h == nil {
		return uintptr(eFail)
	}
	h.invoked.Store(true)
	if t, _, _ := pGetCurrentThreadID.Call(); uint32(t) != objects.uiThread {
		objects.mu.Lock()
		objects.offUI++
		objects.mu.Unlock()
	}
	return uintptr(h.invoke(a, b))
}

// offUIInvocations counts handler invocations off the UI thread; WebView2
// calls back on the thread that created its objects, so any is a finding.
func offUIInvocations() int {
	objects.mu.Lock()
	defer objects.mu.Unlock()
	return objects.offUI
}

// liveObjects counts handler and stream objects the engine still references.
func liveObjects() (handlers, streams int) {
	objects.mu.Lock()
	defer objects.mu.Unlock()
	return len(objects.handlers), len(objects.streams)
}

// newStreamObject makes an agile, read-only IStream over body. The caller
// holds its one reference and passes it to the engine, which takes its own.
func newStreamObject(body []byte, released func()) uintptr {
	initObjects()
	s := newStream(&streamBody{data: body, released: released}, 0)
	if s == nil {
		return 0
	}
	return s.ptr()
}

func newStream(body *streamBody, pos int64) *streamObject {
	obj := allocObject(&objects.streamVtbl[0])
	if obj == nil {
		return nil
	}
	objects.mu.Lock()
	objects.next++
	obj.id = objects.next
	s := &streamObject{id: obj.id, obj: obj, pos: pos, body: body}
	atomic.AddInt32(&body.streams, 1)
	objects.streams[s.id] = s
	objects.mu.Unlock()
	return s
}

func (s *streamObject) ptr() uintptr { return uintptr(unsafe.Pointer(s.obj)) }

func lookupStream(this *comObject) *streamObject {
	objects.mu.Lock()
	defer objects.mu.Unlock()
	return objects.streams[this.id]
}

const (
	stgInvalidFunction hresult = 0x80030001
	stgAccessDenied    hresult = 0x80030005
	stgTypeStream              = 2
	statNoName                 = 1
)

// streamQueryInterface claims IAgileObject: the engine may then read the body
// from any thread without marshaling, and Read locks for that.
func streamQueryInterface(this *comObject, riid *guid, ppv *uintptr) uintptr {
	if ppv == nil {
		return uintptr(ePointer)
	}
	if *riid == iidIUnknown || *riid == iidISequentialStream || *riid == iidIStream || *riid == iidIAgileObject {
		atomic.AddInt32(&this.refs, 1)
		atomic.StoreInt32(&this.taken, 1)
		*ppv = uintptr(unsafe.Pointer(this))
		return uintptr(sOK)
	}
	*ppv = 0
	return uintptr(eNoInterface)
}

func streamReadEntry(this *comObject, pv uintptr, cb uint32, pcbRead *uint32) uintptr {
	s := lookupStream(this)
	if s == nil || pv == 0 {
		return uintptr(ePointer)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	remaining := int64(len(s.body.data)) - s.pos
	count := min(int64(cb), max(remaining, 0))
	if count > 0 {
		copy(unsafe.Slice((*byte)(unsafe.Add(unsafe.Pointer(nil), pv)), count), s.body.data[s.pos:s.pos+count])
		s.pos += count
	}
	if pcbRead != nil {
		*pcbRead = uint32(count)
	}
	if count < int64(cb) {
		return uintptr(sFalse)
	}
	return uintptr(sOK)
}

func streamWriteEntry(this *comObject, pv uintptr, cb uint32, pcbWritten *uint32) uintptr {
	if pcbWritten != nil {
		*pcbWritten = 0
	}
	return uintptr(stgAccessDenied)
}

const (
	seekSet = 0
	seekCur = 1
	seekEnd = 2
)

func streamSeekEntry(this *comObject, move uintptr, origin uint32, newPos *uint64) uintptr {
	s := lookupStream(this)
	if s == nil {
		return uintptr(ePointer)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	base := int64(0)
	switch origin {
	case seekSet:
	case seekCur:
		base = s.pos
	case seekEnd:
		base = int64(len(s.body.data))
	default:
		return uintptr(stgInvalidFunction)
	}
	target := base + int64(move)
	if target < 0 {
		return uintptr(stgInvalidFunction)
	}
	s.pos = target
	if newPos != nil {
		*newPos = uint64(target)
	}
	return uintptr(sOK)
}

func streamInvalid(this *comObject, a uintptr) uintptr        { return uintptr(stgInvalidFunction) }
func streamInvalid3(this *comObject, a, b, c uintptr) uintptr { return uintptr(stgInvalidFunction) }
func streamOK(this *comObject, a uintptr) uintptr             { return uintptr(sOK) }
func streamCopyToEntry(this *comObject, a, b, c, d uintptr) uintptr {
	return uintptr(eNotImpl)
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

func streamStatEntry(this *comObject, pstat *statstg, flags uint32) uintptr {
	s := lookupStream(this)
	if s == nil || pstat == nil {
		return uintptr(ePointer)
	}
	*pstat = statstg{kind: stgTypeStream, size: uint64(len(s.body.data))}
	return uintptr(sOK)
}

func streamCloneEntry(this *comObject, ppstm *uintptr) uintptr {
	if ppstm == nil {
		return uintptr(ePointer)
	}
	*ppstm = 0
	s := lookupStream(this)
	if s == nil {
		return uintptr(ePointer)
	}
	s.mu.Lock()
	pos := s.pos
	s.mu.Unlock()
	c := newStream(s.body, pos)
	if c == nil {
		return uintptr(eOutOfMemory)
	}
	atomic.StoreInt32(&c.obj.taken, 1)
	*ppstm = c.ptr()
	return uintptr(sOK)
}
