//go:build linux

package main

// Signal reservation, following up pass A. The first probe chose JSC's GC
// signal by inference: the highest real-time signal whose kernel disposition
// was still Go's handler and that was not in a list of known subscriptions.
// In a Go process every catchable signal carries Go's handler whether or not
// anything subscribed to it, so the kernel cannot show a subscription, and
// os/signal offers no way to list them. These variants reproduce what that
// inference does to an independent subscriber, and prototype the
// alternative: the application names a signal it reserves for JSC.
//
// Experiment only. Nothing here is a dfx2 API, and a reservation says
// nothing about the suspend handler's safety on goroutine stacks.

import (
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// receive raises sig at this process and reports whether ch got it within
// a second. Only call it while sig's disposition is Go's handler or SIG_IGN.
func receive(ch chan os.Signal, sig int) bool {
	for len(ch) > 0 {
		<-ch
	}
	syscall.Kill(os.Getpid(), syscall.Signal(sig))
	select {
	case <-ch:
		return true
	case <-time.After(time.Second):
		return false
	}
}

// reservationCheck is one sanity check on an application's reservation, and
// what passing it cannot establish.
type reservationCheck struct {
	Check   string `json:"check"`
	Passed  bool   `json:"passed"`
	Detail  string `json:"detail,omitempty"`
	Limited string `json:"cannot_establish,omitempty"`
}

// validateReservation applies the host's sanity checks to an application's
// reservation. None of them can see a Go signal.Notify subscription: the
// kernel shows Go's handler on every catchable signal, subscribed or not,
// and os/signal cannot list subscriptions. signal.Ignored only reports a
// prior Ignore.
func (p *probe) validateReservation(sig int, ds []disposition) []reservationCheck {
	d := find(ds, sig)
	env := os.Getenv("JSC_SIGNAL_FOR_GC")
	return []reservationCheck{
		{Check: "supplied", Passed: sig != 0, Detail: "an explicit reservation; the host infers nothing"},
		{Check: "within libc's real-time range", Passed: sig >= p.rtmin && sig <= p.rtmax,
			Detail: fmt.Sprintf("%d in [%d, %d]", sig, p.rtmin, p.rtmax), Limited: "that no other library uses it"},
		{Check: "kernel disposition is Go's handler", Passed: d.Owner == "go", Detail: "owner " + d.Owner,
			Limited: "that no Go subscriber exists: subscribed and unsubscribed signals look identical"},
		{Check: "not blocked on the initializing thread", Passed: !d.Blocked, Limited: "the masks of threads created later"},
		{Check: "not among the application's declared subscriptions", Passed: !slices.Contains(p.notified, syscall.Signal(sig)),
			Limited: "subscriptions the application did not declare"},
		{Check: "not ignored through os/signal", Passed: !signal.Ignored(syscall.Signal(sig))},
		{Check: "JSC_SIGNAL_FOR_GC unset or equal", Passed: env == "" || env == strconv.Itoa(sig), Detail: "JSC_SIGNAL_FOR_GC=" + env},
	}
}

// runReservationProbe is one disposable subprocess. Variants:
//
//	hidden-subscriber      an independent component subscribes to the signal the
//	                       inferred selector picks; selection, then release only
//	hidden-subscriber-jsc  the same through JSC initialization, then that signal is raised
//	reserved               the explicit path, with -signal-reserve N
//	reserved-violate-before  N already has an undeclared subscriber
//	reserved-violate-after   the application subscribes to N after initialization
//	reserved-foreign       injected: native code claimed N before validation
//
// A reserved variant without -signal-reserve is refused: nothing is inferred.
func runReservationProbe(variant string, reserve int) error {
	runtime.LockOSThread()
	p := &probe{report: map[string]any{"variant": variant, "pid": os.Getpid(), "reserve_flag": reserve}}
	p.notified = []syscall.Signal{syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGHUP} // declared
	declared := make(chan os.Signal, 8)
	signal.Notify(declared, syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGHUP)
	urg, _ := sigaction(int(syscall.SIGURG))
	p.goHandler = urg.handler
	start := p.snapshot("go-start")
	available, err := p.loadJSC(false)
	if err != nil {
		return err
	}
	loaded := p.snapshot("after-dlopen")
	p.report["libc_sigrtmin"], p.report["libc_sigrtmax"] = p.rtmin, p.rtmax
	p.report["configure_symbol"] = map[bool]string{true: "present", false: "absent"}[available]
	reserved := variant == "reserved" || variant == "reserved-violate-before" || variant == "reserved-violate-after" || variant == "reserved-foreign"

	// An independent subscriber: a component that called signal.Notify on its
	// own. Nothing reports it to the host's list.
	hiddenSig := 0
	switch variant {
	case "hidden-subscriber", "hidden-subscriber-jsc":
		hiddenSig = p.rtmax // where the inferred selector looks first
	case "reserved-violate-before":
		hiddenSig = reserve
	}
	var hidden chan os.Signal
	if hiddenSig != 0 {
		hidden = make(chan os.Signal, 1)
		signal.Notify(hidden, syscall.Signal(hiddenSig))
		p.report["hidden_subscriber"] = map[string]any{"signal": hiddenSig, "declared_to_host": false}
		p.report["hidden_received_before_selection"] = receive(hidden, hiddenSig)
		loaded = p.snapshot("after-hidden-subscribe")
		a, b := find(loaded, hiddenSig), find(loaded, hiddenSig-1)
		p.report["kernel_view"] = map[string]any{"subscribed": a, "unsubscribed_neighbour": b,
			"indistinguishable": a.Owner == b.Owner && slices.Equal(a.Flags, b.Flags) && a.Blocked == b.Blocked}
	}
	if variant == "reserved-foreign" && reserve != 0 {
		// Injected: native code claimed the signal (modelled as SIG_IGN set
		// behind Go's back) before the host validated the reservation.
		setDisposition(reserve, kernelSigaction{handler: 1})
		p.report["injected"] = "native SIG_IGN on the reserved signal before validation"
		loaded = p.snapshot("after-injected-foreign-owner")
	}

	selected, ready, reason := 0, true, ""
	if reserved {
		checks := p.validateReservation(reserve, loaded)
		p.report["reservation_checks"] = checks
		for _, c := range checks {
			if !c.Passed {
				ready, reason = false, "reservation refused: "+c.Check
				break
			}
		}
		selected = reserve
	} else {
		var rejected []map[string]any
		selected, rejected = p.selectSignal(loaded, false)
		p.report["rejected_candidates"] = rejected
		p.report["inferred_selection"] = selected
	}
	p.report["selected_signal"] = selected
	if !available {
		ready, reason = false, "configuration entry point unavailable"
	}
	if !ready {
		p.report["ready"], p.report["reason"] = false, reason
		p.report["changed_before_init"] = changed(start, p.snapshot("refused"))
		return p.emit()
	}

	// Release from Go. This is where an undeclared subscription is lost:
	// signal.Ignore cancels every Notify on the signal, and nothing tells
	// its owner.
	signal.Ignore(syscall.Signal(selected))
	p.snapshot("after-go-ignore")
	p.report["go_reports_ignored_after_release"] = signal.Ignored(syscall.Signal(selected))
	if hidden != nil {
		// Still SIG_IGN, so raising it is harmless and shows whether the
		// subscriber would ever hear it again.
		p.report["hidden_received_after_release"] = receive(hidden, selected)
	}
	if variant == "hidden-subscriber" {
		p.report["ready"], p.report["reason"] = true, "release only: stopped before the default disposition and JSC"
		return p.emit()
	}
	if err := setDisposition(selected, kernelSigaction{}); err != nil {
		return err
	}
	p.report["configure_result"] = p.jsc.configure(int32(selected))
	configured := p.snapshot("before-jsc-init")
	ctx := p.jsc.globalContextCreate(0)
	initialized := p.snapshot("after-jsc-init")
	p.report["changed_by_jsc_init"] = changed(configured, initialized)
	p.report["audit_after_init"] = p.auditReservation(selected, initialized)
	if variant == "reserved-violate-after" {
		// The application breaks its promise. os/signal reinstalls Go's
		// handler over JSC's (keeping JSC's only as a forwarding target), so
		// the host's later audit can see it, after the fact.
		late := make(chan os.Signal, 1)
		signal.Notify(late, syscall.Signal(selected))
		p.report["audit_after_violation"] = p.auditReservation(selected, p.snapshot("after-late-notify"))
		p.report["ready"], p.report["reason"] = true, "violation after initialization; the audit is the only check that can see it"
		return p.emit()
	}
	p.report["workload"] = p.workload(ctx, 2)
	p.report["audit_after_workload"] = p.auditReservation(selected, p.snapshot("after-workload"))
	p.report["delivery"] = []map[string]any{deliver(declared, syscall.SIGUSR2), deliver(declared, syscall.SIGHUP), deliver(declared, syscall.SIGUSR1)}
	p.jsc.globalContextRelease(ctx)
	p.report["ready"], p.report["reason"] = ready, reason
	if err := p.emit(); err != nil || hidden == nil {
		return err
	}
	// Hazardous, after the report: the signal is now JSC's suspend signal,
	// raised as the old subscriber's sender would. A process that dies here
	// prints nothing more.
	fmt.Println(`{"event":"raising-reserved-signal","signal":` + strconv.Itoa(selected) + `}`)
	got := receive(hidden, selected)
	fmt.Println(`{"event":"raised-reserved-signal","old_subscriber_received":` + strconv.FormatBool(got) + `}`)
	return nil
}

// auditReservation re-reads the reserved signal's disposition: after
// initialization it must still be JSC's. This catches later Go registrations,
// because os/signal reinstalls Go's handler; it cannot see anything that left
// the disposition alone.
func (p *probe) auditReservation(sig int, ds []disposition) map[string]any {
	d := find(ds, sig)
	return map[string]any{"signal": sig, "owner": d.Owner, "flags": d.Flags,
		"jsc_owns_it": strings.HasPrefix(d.Owner, "native:libjavascriptcoregtk")}
}
