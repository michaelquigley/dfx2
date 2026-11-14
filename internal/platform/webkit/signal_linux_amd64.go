package webkit

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"unsafe"
)

// The approved Linux GC-signal mechanism. WebKit initializes JavaScriptCore at
// its first type initialization, and JavaScriptCore then installs its thread
// suspend/resume handler on its GC signal, SIGUSR1 by default. Before that,
// the application's explicitly reserved real-time signal is released from Go
// and handed to JavaScriptCore's private configuration entry point.
//
// The checks below are sanity checks. None can see a Go signal.Notify
// subscription: the kernel shows Go's handler on every catchable signal,
// subscribed or not. The application promises that it and its dependencies
// neither use the signal nor later subscribe to, ignore, reset or send it.
// This moves JavaScriptCore's handler; it does not make a stray signal safe.

// kernelSigaction is struct kernel_sigaction on linux/amd64.
type kernelSigaction struct {
	handler, flags, restorer uintptr
	mask                     uint64
}

func sigaction(sig int) (kernelSigaction, error) {
	var old kernelSigaction
	_, _, e := syscall.RawSyscall6(syscall.SYS_RT_SIGACTION, uintptr(sig), 0, uintptr(unsafe.Pointer(&old)), 8, 0, 0)
	if e != 0 {
		return old, e
	}
	return old, nil
}

func setDefault(sig int) error {
	var a kernelSigaction // SIG_DFL
	_, _, e := syscall.RawSyscall6(syscall.SYS_RT_SIGACTION, uintptr(sig), uintptr(unsafe.Pointer(&a)), 0, 8, 0, 0)
	if e != 0 {
		return e
	}
	return nil
}

func threadBlocked() uint64 {
	var old uint64
	syscall.RawSyscall6(syscall.SYS_RT_SIGPROCMASK, 0, 0, uintptr(unsafe.Pointer(&old)), 8, 0, 0)
	return old
}

// reservation is process-wide: the first successful one survives every
// browser window. A failure after Go released the signal is latched, because
// the process can no longer offer it to anyone.
var reservation struct {
	sync.Mutex
	signal int
	failed error
}

// reserve configures JavaScriptCore's GC signal once per process, before any
// WebKit type exists. Later calls must name the same signal.
func reserve(sig int) error {
	reservation.Lock()
	defer reservation.Unlock()
	if reservation.failed != nil {
		return reservation.failed
	}
	if reservation.signal != 0 {
		if sig != reservation.signal {
			return fmt.Errorf("GC signal reservation %d is inconsistent with this process's reservation %d", sig, reservation.signal)
		}
		return nil
	}
	if err := checkReservation(sig); err != nil {
		return err
	}
	if w.configureGC == nil {
		return fmt.Errorf("JavaScriptCore's %s entry point is unavailable in this runtime (WebKitGTK %s); browser windows cannot start", configureSymbol, version())
	}
	// From here Go no longer owns the signal; any failure is permanent.
	signal.Ignore(syscall.Signal(sig))
	if err := setDefault(sig); err != nil {
		reservation.failed = fmt.Errorf("GC signal %d: reset disposition: %w", sig, err)
		return reservation.failed
	}
	if !w.configureGC(int32(sig)) {
		reservation.failed = fmt.Errorf("JavaScriptCore refused GC signal %d", sig)
		return reservation.failed
	}
	reservation.signal = sig
	return nil
}

// checkReservation refuses a reservation before anything changes.
func checkReservation(sig int) error {
	if sig == 0 {
		return errors.New("no GC signal reserved: Linux browser windows need Options.LinuxGCSignal, a real-time signal the application reserves for JavaScriptCore")
	}
	if low, high := int(w.rtmin()), int(w.rtmax()); sig < low || sig > high {
		return fmt.Errorf("GC signal %d is outside libc's real-time range %d-%d", sig, low, high)
	}
	reserved, err := sigaction(sig)
	if err != nil {
		return fmt.Errorf("GC signal %d: %w", sig, err)
	}
	// SIGURG always carries the Go runtime's own handler.
	runtime, err := sigaction(int(syscall.SIGURG))
	if err != nil {
		return err
	}
	if reserved.handler != runtime.handler {
		return fmt.Errorf("GC signal %d is not owned by the Go runtime: another component installed a disposition", sig)
	}
	if threadBlocked()&(1<<(sig-1)) != 0 {
		return fmt.Errorf("GC signal %d is blocked on the UI thread", sig)
	}
	if signal.Ignored(syscall.Signal(sig)) {
		return fmt.Errorf("GC signal %d is ignored through os/signal", sig)
	}
	if env := os.Getenv("JSC_SIGNAL_FOR_GC"); env != "" && env != strconv.Itoa(sig) {
		return fmt.Errorf("JSC_SIGNAL_FOR_GC=%s conflicts with the reserved GC signal %d", env, sig)
	}
	return nil
}

// reservedSignal reports the process's reservation, for diagnostics.
func reservedSignal() int {
	reservation.Lock()
	defer reservation.Unlock()
	return reservation.signal
}
