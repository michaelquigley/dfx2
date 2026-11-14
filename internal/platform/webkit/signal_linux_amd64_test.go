package webkit

import (
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// preserve restores signals' kernel dispositions and Go's ignore state after a
// test, so repeated runs in one process start alike.
func preserve(t *testing.T, sigs ...int) {
	t.Helper()
	for _, sig := range sigs {
		saved, err := sigaction(sig)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			// Only Notify clears os/signal's ignored state; Reset then drops
			// the subscription before the raw disposition is restored.
			signal.Notify(make(chan os.Signal, 1), syscall.Signal(sig))
			signal.Reset(syscall.Signal(sig))
			syscall.RawSyscall6(syscall.SYS_RT_SIGACTION, uintptr(sig), uintptr(unsafe.Pointer(&saved)), 0, 8, 0, 0)
		})
	}
}

// The reservation's refusals and its process-wide consistency, with the
// JavaScriptCore entry point replaced. These tests change real dispositions
// of real-time signals in the test process only.
func TestReservationRefusesVisiblyAndStaysConsistent(t *testing.T) {
	preserve(t, 61, 63)
	saved := w
	t.Cleanup(func() { w = saved })
	reservation.Lock()
	savedSignal, savedFailed := reservation.signal, reservation.failed
	reservation.signal, reservation.failed = 0, nil
	reservation.Unlock()
	t.Cleanup(func() {
		reservation.Lock()
		reservation.signal, reservation.failed = savedSignal, savedFailed
		reservation.Unlock()
	})
	w.rtmin = func() int32 { return 35 }
	w.rtmax = func() int32 { return 64 }
	var configured []int32
	w.configureGC = func(sig int32) bool { configured = append(configured, sig); return true }
	refused := func(sig int, want string) {
		t.Helper()
		if err := reserve(sig); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("signal %d: %v, want %q", sig, err, want)
		}
	}
	refused(0, "need Options.LinuxGCSignal")
	refused(34, "outside libc's real-time range")
	refused(65, "outside libc's real-time range")
	t.Setenv("JSC_SIGNAL_FOR_GC", "50")
	refused(61, "conflicts")
	t.Setenv("JSC_SIGNAL_FOR_GC", "")
	// Another component installed a disposition behind Go's back.
	if err := setDefault(63); err != nil {
		t.Fatal(err)
	}
	refused(63, "not owned by the Go runtime")
	missing := w.configureGC
	w.configureGC = nil
	refused(61, "unavailable")
	w.configureGC = missing
	if len(configured) != 0 {
		t.Fatal("a refused reservation reached JavaScriptCore")
	}
	if err := reserve(61); err != nil || len(configured) != 1 || configured[0] != 61 {
		t.Fatalf("reservation: %v %v", err, configured)
	}
	if a, err := sigaction(61); err != nil || a.handler != 0 {
		t.Fatalf("reserved signal not released from Go: %+v %v", a, err)
	}
	if err := reserve(61); err != nil || len(configured) != 1 {
		t.Fatalf("a repeated reservation reconfigured: %v %v", err, configured)
	}
	refused(62, "inconsistent")
}

func TestRefusedConfigurationIsLatched(t *testing.T) {
	preserve(t, 59)
	saved := w
	t.Cleanup(func() { w = saved })
	reservation.Lock()
	savedSignal, savedFailed := reservation.signal, reservation.failed
	reservation.signal, reservation.failed = 0, nil
	reservation.Unlock()
	t.Cleanup(func() {
		reservation.Lock()
		reservation.signal, reservation.failed = savedSignal, savedFailed
		reservation.Unlock()
	})
	w.rtmin = func() int32 { return 35 }
	w.rtmax = func() int32 { return 64 }
	w.configureGC = func(int32) bool { return false }
	if err := reserve(59); err == nil || !strings.Contains(err.Error(), "refused GC signal 59") {
		t.Fatal(err)
	}
	// Go no longer owns 59, so nothing may retry with any signal.
	w.configureGC = func(int32) bool { return true }
	if err := reserve(58); err == nil || !strings.Contains(err.Error(), "refused GC signal 59") {
		t.Fatalf("failure not latched: %v", err)
	}
}
