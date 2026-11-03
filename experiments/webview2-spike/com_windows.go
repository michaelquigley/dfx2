//go:build windows && amd64

package main

import (
	"fmt"
	"log"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// com is any COM interface pointer the host holds or borrows. It points at
// native memory owned by the object's implementation; its first word is the
// vtable. The array bound only sizes the type: slots are checked against
// the IDL (bindings.go), never against it.
type com struct{ vtbl *[64]uintptr }

// call invokes slot i with this first. Arguments that are pointers converted
// to uintptr at the call site stay valid: uintptrescapes moves their targets
// to the heap and keeps them alive for the call, as syscall's Proc.Call does.
//
//go:uintptrescapes
func (o *com) call(i int, args ...uintptr) hresult {
	a := make([]uintptr, 0, len(args)+1)
	a = append(a, uintptr(unsafe.Pointer(o)))
	a = append(a, args...)
	r, _, _ := syscall.SyscallN(o.vtbl[i], a...)
	return hresult(r)
}

func (o *com) addRef() { o.call(slotAddRef) }

// release drops one reference. Safe on nil, so teardown can release
// whatever it got.
func (o *com) release() {
	if o != nil {
		o.call(slotRelease)
	}
}

func (o *com) queryInterface(iid *guid) (*com, hresult) {
	var p *com
	hr := o.call(slotQueryInterface, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&p)))
	return p, hr
}

// asCOM turns a pointer argument received in a callback into a borrowed
// interface pointer. The caller AddRefs it to keep it past the callback.
func asCOM(p uintptr) *com {
	if p == 0 {
		return nil
	}
	return (*com)(unsafe.Add(unsafe.Pointer(nil), p))
}

func (o *com) ptr() uintptr { return uintptr(unsafe.Pointer(o)) }

// Callback objects: COM objects the host implements, one per handler
// registration, each with correct QueryInterface, AddRef and Release.
//
// The four entry points (QueryInterface, AddRef, Release, Invoke) are made
// once for the process: syscall callbacks are never freed. Objects route
// to their Go state by an id stored in the object, never by a Go pointer.
//
// Each object lives in CoTaskMemAlloc memory, outside the Go heap, so its
// address is stable and its reference count is the only thing deciding its
// life: native code never holds a pointer into Go memory. The shared vtable
// is a package-level array, which the GC roots for the life of the process.
// The object is freed when its count reaches zero.

type cbObject struct {
	vtbl uintptr
	refs int32
	_    int32
	id   uintptr
}

type callback struct {
	id     uintptr
	name   string
	iid    guid
	obj    *cbObject
	invoke func(a, b uintptr) hresult
}

var (
	cbVtbl     [4]uintptr
	cbInitOnce sync.Once

	cbMu     sync.Mutex
	cbByID   = map[uintptr]*callback{}
	cbNextID uintptr
	cbQIOnce = map[guid]bool{} // unexpected IIDs already logged
)

func initCallbacks() {
	cbInitOnce.Do(func() {
		cbVtbl[slotQueryInterface] = syscall.NewCallback(cbQueryInterface)
		cbVtbl[slotAddRef] = syscall.NewCallback(cbAddRef)
		cbVtbl[slotRelease] = syscall.NewCallback(cbRelease)
		cbVtbl[handlerInvoke] = syscall.NewCallback(cbInvoke)
	})
}

// newCallback makes a handler object holding one reference, the caller's.
// The caller releases it once WebView2 has taken its own (after the add_ or
// Create call returns).
func newCallback(name string, iid guid, invoke func(a, b uintptr) hresult) *callback {
	initCallbacks()
	p, _, _ := pCoTaskMemAlloc.Call(unsafe.Sizeof(cbObject{}))
	if p == 0 {
		panic("CoTaskMemAlloc failed")
	}
	obj := (*cbObject)(unsafe.Add(unsafe.Pointer(nil), p))
	cbMu.Lock()
	cbNextID++
	c := &callback{id: cbNextID, name: name, iid: iid, obj: obj, invoke: invoke}
	cbByID[c.id] = c
	cbMu.Unlock()
	*obj = cbObject{vtbl: uintptr(unsafe.Pointer(&cbVtbl[0])), refs: 1, id: c.id}
	return c
}

func (c *callback) ptr() uintptr { return uintptr(unsafe.Pointer(c.obj)) }

func (c *callback) release() { cbRelease(c.obj) }

func lookup(this *cbObject) *callback {
	cbMu.Lock()
	defer cbMu.Unlock()
	return cbByID[this.id]
}

func cbQueryInterface(this *cbObject, riid *guid, ppv *uintptr) uintptr {
	if ppv == nil {
		return ePointer
	}
	c := lookup(this)
	if c != nil && (*riid == iidIUnknown || *riid == c.iid) {
		atomic.AddInt32(&this.refs, 1)
		*ppv = uintptr(unsafe.Pointer(this))
		return sOK
	}
	*ppv = 0
	cbMu.Lock()
	first := !cbQIOnce[*riid]
	cbQIOnce[*riid] = true
	cbMu.Unlock()
	if first && c != nil {
		// Not claimed: in particular not IAgileObject or IMarshal. These
		// objects are called on the UI thread only.
		log.Printf("com: %s asked for %s: E_NOINTERFACE", c.name, guidString(*riid))
	}
	return eNoInterface
}

func cbAddRef(this *cbObject) uintptr {
	return uintptr(atomic.AddInt32(&this.refs, 1))
}

func cbRelease(this *cbObject) uintptr {
	n := atomic.AddInt32(&this.refs, -1)
	if n == 0 {
		cbMu.Lock()
		delete(cbByID, this.id)
		cbMu.Unlock()
		pCoTaskMemFree.Call(uintptr(unsafe.Pointer(this)))
	}
	return uintptr(n)
}

func cbInvoke(this *cbObject, a, b uintptr) (r uintptr) {
	c := lookup(this)
	if c == nil {
		return eFail
	}
	defer func() {
		if p := recover(); p != nil {
			log.Printf("com: %s panicked: %v", c.name, p)
			r = eFail
		}
	}()
	return uintptr(c.invoke(a, b))
}

// liveCallbacks lists the callback objects still referenced, for the exit
// report: after a clean close every one should be gone.
func liveCallbacks() []string {
	cbMu.Lock()
	defer cbMu.Unlock()
	var out []string
	for _, c := range cbByID {
		out = append(out, fmt.Sprintf("%s (refs %d)", c.name, atomic.LoadInt32(&c.obj.refs)))
	}
	sort.Strings(out)
	return out
}

// memStream copies b into a new memory IStream (SHCreateMemStream). The
// stream owns its copy, so no Go memory outlives the call. The caller owns
// the one reference returned.
func memStream(b []byte) (*com, error) {
	var p uintptr
	if len(b) > 0 {
		p = uintptr(unsafe.Pointer(&b[0]))
	}
	r, _, _ := pSHCreateMemStream.Call(p, uintptr(len(b)))
	if r == 0 {
		return nil, fmt.Errorf("SHCreateMemStream(%d bytes) failed", len(b))
	}
	return asCOM(r), nil
}

// readStream reads up to limit+1 bytes from an IStream, so the caller can
// tell "exactly limit" from "more than limit" without reading everything.
func readStream(s *com, limit int) (b []byte, tooLarge bool, err error) {
	buf := make([]byte, 64<<10)
	for {
		var n uint32
		want := min(len(buf), limit+1-len(b))
		hr := s.call(streamRead, uintptr(unsafe.Pointer(&buf[0])), uintptr(want), uintptr(unsafe.Pointer(&n)))
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
