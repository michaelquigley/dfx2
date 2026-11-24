package webview2

import (
	"testing"
	"unsafe"
)

func TestStreamCloneAllocationFailure(t *testing.T) {
	_, baseline := liveObjects()
	releases := 0
	ptr := newStreamObject([]byte("ab"), func() { releases++ })
	original := (*comObject)(unsafe.Add(unsafe.Pointer(nil), ptr))
	body := lookupStream(original).body
	if hr := streamSeekEntry(original, 1, seekSet, nil); hr != uintptr(sOK) {
		t.Fatalf("seek: %#x", hr)
	}

	alloc := allocateCOMObject
	defer func() { allocateCOMObject = alloc }()
	allocateCOMObject = func() *comObject { return nil }
	out := uintptr(42)
	hr := streamCloneEntry(original, &out)
	allocateCOMObject = alloc
	if hr != 0x8007000e || out != 0 {
		t.Fatalf("failed Clone returned HRESULT %#x, output %#x", hr, out)
	}
	if _, streams := liveObjects(); streams != baseline+1 || body.streams != 1 {
		t.Fatalf("failed Clone left %d live stream objects and %d body references", streams-baseline, body.streams)
	}
	readStreamByte(t, original, 'b')
	comRelease(original)
	if _, streams := liveObjects(); streams != baseline || releases != 1 {
		t.Fatalf("after Release: %d live stream objects, %d body releases", streams-baseline, releases)
	}
}

func TestStreamCloneHasIndependentPositionAndSharesBody(t *testing.T) {
	_, baseline := liveObjects()
	releases := 0
	ptr := newStreamObject([]byte("abc"), func() { releases++ })
	original := (*comObject)(unsafe.Add(unsafe.Pointer(nil), ptr))
	readStreamByte(t, original, 'a')
	var out uintptr
	if hr := streamCloneEntry(original, &out); hr != uintptr(sOK) || out == 0 {
		t.Fatalf("Clone returned HRESULT %#x, output %#x", hr, out)
	}
	clone := (*comObject)(unsafe.Add(unsafe.Pointer(nil), out))
	readStreamByte(t, clone, 'b')
	readStreamByte(t, original, 'b')
	comRelease(original)
	if releases != 0 {
		t.Fatal("original released the body while its clone was live")
	}
	readStreamByte(t, clone, 'c')
	comRelease(clone)
	if _, streams := liveObjects(); streams != baseline || releases != 1 {
		t.Fatalf("after both releases: %d live stream objects, %d body releases", streams-baseline, releases)
	}
}

func readStreamByte(t *testing.T, stream *comObject, want byte) {
	t.Helper()
	buf, _, err := pCoTaskMemAlloc.Call(1)
	if buf == 0 {
		t.Fatalf("allocating Read destination: %v", err)
	}
	defer pCoTaskMemFree.Call(buf)
	var n uint32
	hr := streamReadEntry(stream, buf, 1, &n)
	if b := *(*byte)(unsafe.Add(unsafe.Pointer(nil), buf)); hr != uintptr(sOK) || n != 1 || b != want {
		t.Fatalf("Read returned HRESULT %#x, %d bytes, %q; want %q", hr, n, b, want)
	}
}

func TestHandlerAllocationFailureKeepsRegistryEmpty(t *testing.T) {
	baseline, _ := liveObjects()
	alloc := allocateCOMObject
	defer func() { allocateCOMObject = alloc }()
	allocateCOMObject = func() *comObject { return nil }
	h := newHandlerObject("test", iidEnvironmentCompleted, func(uintptr, uintptr) hresult {
		t.Fatal("failed allocation invoked")
		return sOK
	}, nil)
	if h != nil {
		t.Fatal("failed allocation returned a handler")
	}
	if handlers, _ := liveObjects(); handlers != baseline {
		t.Fatalf("handler registry grew from %d to %d", baseline, handlers)
	}
}

func TestResponseStreamAllocationFailureKeepsRegistryEmpty(t *testing.T) {
	_, baseline := liveObjects()
	alloc := allocateCOMObject
	defer func() { allocateCOMObject = alloc }()
	allocateCOMObject = func() *comObject { return nil }
	released := 0
	ptr := newStreamObject([]byte("response"), func() { released++ })
	if ptr != 0 {
		t.Fatal("failed allocation returned a stream")
	}
	if _, streams := liveObjects(); streams != baseline || released != 0 {
		t.Fatalf("failed allocation: streams %d (baseline %d), releases %d", streams, baseline, released)
	}
}
