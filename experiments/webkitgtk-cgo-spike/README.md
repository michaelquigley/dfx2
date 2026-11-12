# WebKitGTK cgo comparison

This is the first answer to the [Linux cgo webview comparison](../../docs/future/webview-cgo-spike.md): does a cgo binding change JavaScriptCore's signal conflict with Go on the installed runtime? **It does not.** The conflict that remains in a VM-free host is identical with cgo and purego, and the brief's stop condition applies. Browser policy, lifecycle and packaging validation were not started.

**Status (2026-11-12):** Michael chose to keep signal compatibility as a documented, monitored risk. The proposals below were not adopted, and the cgo comparison is closed.

This module is the brief's explicit cgo exception. Nothing outside `experiments/webkitgtk-cgo-spike/` changed: root code, canon, the purego experiment and its evidence are untouched. It reuses `../webview2-spike/feasibility` (the probe page) and `../webkitgtk-spike`'s monitor and drivers.

## What the host is

[`native_linux.go`](native_linux.go) uses only public GTK 3 and WebKitGTK 4.1 C interfaces:

- an ephemeral context, a view and a window
- `load-changed`, delivered through an exported Go callback
- `g_idle_add` for work posted to the GTK thread

Two labelled probes sit outside the ordinary path: a host JSC context, used only as the positive control, and the private `JSConfigureSignalForGC`, wired up but not used in this run.

The host does no JavaScript evaluation, result conversion or script messages. JavaScript runs only in WebKit's children. [`main_linux.go`](main_linux.go) runs one scripted sequence per process:

- **Go side:** `os/signal` subscribers for SIGHUP, SIGUSR1, SIGUSR2 and SIGXCPU; a loopback Go HTTP endpoint (the probe page plus `/fault/nil` and `/fault/bus`); and a progress goroutine. All start before GTK.
- **Phases:** `before-webkit`, `after-load`, `after-reopen` (window destroyed, a second loaded) and `after-teardown`, all while `gtk_main` runs.
- **Recorded at each phase:**
  - every watched disposition with its flags, and per-thread blocked masks
  - alternate stacks on the main thread and a locked Go thread
  - delivery to the Go subscriber of every signal Go still owns, sent three ways: `kill` from a goroutine, `/usr/bin/kill` from outside, and `tgkill` to a native thread
  - recovered Go nil and SIGBUS faults (the purego probe's truncated-mapping fault)
  - the same two faults inside HTTP handlers, followed by a request to show the endpoint still serves

[`probe.py`](probe.py) runs every case in its own bounded process.

```sh
cd experiments/webkitgtk-cgo-spike
CGO_ENABLED=1 go build -o /tmp/dfx2-cgo-probe .
(cd ../webkitgtk-spike && CGO_ENABLED=0 go build -o /tmp/dfx2-wk-feas .)
python3 -B probe.py /tmp/dfx2-cgo-probe /tmp/dfx2-wk-feas evidence/linux/signals --debug-dir DIR
```

`DIR` holds `.build-id` links to Ubuntu's debuginfod files for the installed `libjavascriptcoregtk` and `libwebkit2gtk`, as in the purego experiment's VM-free run. They are kept outside git.

Run 2026-11-12 on Ubuntu 26.04.1, kernel 7.0.0-43, Wayland:

- **Runtime:** WebKitGTK and JSC 2.52.6-0ubuntu0.26.04.1, GTK 3.24.52.
- **Toolchain:** Go 1.27.1, gcc 15.2.0, with headers from `libwebkit2gtk-4.1-dev`, `libjavascriptcoregtk-4.1-dev` and `libgtk-3-dev`.

All 20 checks passed (`evidence/linux/signals/summary.json`). Every process ran with working directory `/`, from a binary in `/tmp`.

## Observed

| Question | cgo host | purego VM-free host, same runtime |
| --- | --- | --- |
| Host VM, GC thread registration, suspension, fault-handler finalization | None. Every required watch resolved. The positive control (`-vm-control`, separate process) showed a VM, the Go main thread registered and fault handlers installed. | None (earlier `vm-free/` evidence) |
| Dispositions after page load | JSC owns SIGUSR1 (suspend handler: `SA_SIGINFO\|SA_RESTART`, no `SA_ONSTACK`) and SIGXCPU (`SA_RESTART`). Go keeps SIGSEGV, SIGBUS, SIGHUP and SIGUSR2. | The same |
| Who installs them | `ThreadingPOSIX.cpp:217` (SIGUSR1) and `RealTimeThreads.cpp:191` (SIGXCPU). GLib and GTK set SIGPIPE. | The same |
| Go subscribers | SIGHUP and SIGUSR2 delivered in every mode at every phase, including `tgkill` to native threads such as `[pango] fontcon`. SIGUSR1 and SIGXCPU stop reaching Go after load. | The same, where observed |
| Faults | Nil and SIGBUS faults recovered at every phase, each SIGBUS at a mapped page's address. Handler faults are recovered by `net/http`, which drops that connection; the endpoint then answers 200. | Recovered (no JSC fault handlers) |
| Go progress while `gtk_main` runs | The counter advanced across every phase | Not measured as such |
| Callbacks running Go | `load-changed` and the idle callback, all on the main thread, which also runs `gtk_main` | The same thread |
| Alternate stacks and masks | Go threads, including the main thread, have 32 KiB alternate stacks. Most native threads (IPC receive queues, `gdbus`, pools) block nothing, so process-directed signals can land on them. `gmain` and Mesa's threads block most signals. | Not recorded |
| **Stray SIGUSR1 after load** | **Kills the process** from every sender: `self` and `external` exit 2 ("signal arrived during cgo execution"), `native` dies by SIGSEGV (−11). | **Kills the process** (exit 2) |
| Stray SIGXCPU after load | Survives; the Go subscriber does not receive it | Survives |
| `JSC_SIGNAL_FOR_GC=60` set by the host before GTK | SIGUSR1 back to Go (a stray SIGUSR1 is received); 60 is JSC's; the run stays VM-free. JSC logs "ERROR: invalid option: JSC_SIGNAL_FOR_GC=60" once per process that inherits the variable. | The private-call reservation gives the same split |
| **Stray signal 60 with that reservation** | **Kills the process** (exit 2) | **Kills the process** (exit 2) |

**Where the stray faults:** gdb stops at `signalHandlerSuspendResume` (`Source/WTF/wtf/posix/ThreadingPOSIX.cpp:124`), fault address 0x58 (`crash-frames.txt`). The handler reads `targetThread`, which only a suspension sets. In a host that never suspends, the pointer is null, and `thread->m_suspendCount` faults inside the signal handler. Go's SIGSEGV handler then finds a fault in non-Go code and aborts.

## Why cgo cannot change it (source argument)

- **Whoever calls `sigaction` last owns the signal.** WTF and WebKit replace Go's handlers with `sigaction` from inside their own initialization: `Thread::initializePlatformThreading` and `RealTimeThreads::setupSignalHandler`. A binding cannot affect what a library does inside its own initialization.
- **cgo and `fakecgo` run Go the same way.** Both mark the program as using cgo (`iscgo`), create Go threads with `pthread_create` while every signal is blocked (Go's `runtime/cgo/pthread_unix.c`; purego's `internal/fakecgo/go_linux.go`), call native code on the system stack through `asmcgocall`, enter Go from callbacks through `cgocallback`, and propagate `os.Setenv` to C's `setenv`.

  The one difference observed: real cgo installs Go's own handlers through libc (`x_cgo_sigaction`), and purego uses `rt_sigaction` directly. That changed no disposition. It does mean that, in a cgo binary, the monitor's libc `sigaction` watch also sees Go's 55 installations, so the driver separates them.
- **Go documents the contract WebKit breaks.** In `os/signal`'s "Go programs that use cgo or SWIG", non-Go code "must use the SA_ONSTACK flag" for any handler it installs. JSC's suspend handler and WebKit's SIGXCPU handler do not. Under cgo or purego alike, the Go runtime cannot repair that afterwards.
- **A C event loop does not keep signals off Go.** The main thread runs `gtk_main` and also runs every Go callback on its goroutine stack. Process-directed signals go to whichever thread does not block them, and that includes Go threads.

## What remains, and the smallest accommodation for decision

None of the following is implemented.

**1. GC suspend handler (any runtime, either binding).** Unmodified, it takes SIGUSR1: application subscribers lose it, and any SIGUSR1 kills the process. Moving it with the reservation moves the hazard, it does not remove it.

- **No-code option.** The host sets `JSC_SIGNAL_FOR_GC` to an explicitly reserved signal before the first WebKit type is used. This needs no private symbol; the variable is undocumented, but WTF's own warning names it. The application must then never send that signal and must keep other software from sending it; a violation kills the process.

  Side effects: JSC's "invalid option" error on stderr in every inheriting process, and every child process inherits the variable. WebKit's own children then use the same GC signal, which is harmless because they are not Go.
- **Smallest native accommodation: restore after initialization.** Save Go's `sigaction` for SIGUSR1 (or the reserved signal) before WebKit starts. When the first WebKit object's construction returns (WTF installs the handler synchronously, once per process), write it back.

  In a VM-free host nothing ever suspends a host thread, so that handler is dead code there. Restoring Go's handler gives SIGUSR1 back to the application, makes strays harmless, and needs neither the environment variable nor the private symbol.

  Failure behaviour: if any path later creates a host VM against the rules, its collector would send the signal, Go would swallow it, and `Thread::suspend` would wait forever: a hang in place of the memory-safety hazard the handler-safety investigation found. A disposition audit (SIGSEGV leaving Go's ownership is the first VM's fingerprint) could fail fast instead.

  This is handler replacement on another library's signal, which the brief withholds until Michael decides. It works identically with purego.

**2. SIGXCPU (2.52.6).** WebKit replaces Go's handler after the first web-process connection. A stray survives, because the handler only writes an eventfd, but the Go subscriber loses it. The handler has no `SA_ONSTACK`, so a SIGXCPU delivered to a Go thread while it runs Go code would push its frame onto a goroutine stack. That was not provoked here.

- **No-code option: application restriction.** Do not subscribe to or send SIGXCPU, and do not set a soft `RLIMIT_CPU`. Audit the disposition.

  **Corrected 2026-11-12:** an earlier version of this line said the kernel's `RLIMIT_RTTIME` signal goes only to the overrunning real-time thread. That is unsupported. In upstream Linux v7.0, `kernel/time/posix-cpu-timers.c`'s `check_thread_timers` calls `check_rlimit`, which sends SIGXCPU with `PIDTYPE_TGID`. That is process-directed, so any thread that does not block SIGXCPU may receive it, including a Go thread running Go code. The restriction above therefore does not cover the real-time path, and must not be relied on to approve it. No native reproduction of that kernel path was made. In this session RealtimeKit grants nothing (see the SIGXCPU investigation), so no real-time thread existed to overrun.
- **A restore-after-install accommodation** like the one above loses WebKit's demotion. It is unsafe wherever real-time promotion is granted unless promotion is also disabled, which takes the private `RealTimeThreads::setEnabled(false)`. Not recommended.

## What cgo changes otherwise

- **Simpler.** Bindings are typed C. Callbacks are ordinary exported functions instead of purego's per-signature trampolines with token routing. Declarations are checked by the installed headers at build time, rather than by hand against GIR. The whole native file, including the two probes and the helpers, is 184 lines.
- **Costlier.** Building needs the C toolchain and `-dev` headers (above).

  Linking is to the system libraries: `NEEDED` lists `libwebkit2gtk-4.1.so.0`, `libgtk-3.so.0`, `libjavascriptcoregtk-4.1.so.0`, `libgobject-2.0.so.0`, `libglib-2.0.so.0` and `libc.so.6`, with interpreter `/lib64/ld-linux-x86-64.so.2` and 122 resolved libraries (`deps.json`).

  The dynamic linker maps WebKit, JSC and GTK before `main` runs (`loaded_before_gtk`). An executable importing such a package therefore depends on WebKit being installed even in native-only mode: it fails to start without it, and pays the load cost in every mode. Loading changed no signal disposition (the `before-webkit` phase). Avoiding this would mean `dlopen`, which gives up most of what cgo adds.
- **Unchanged.** It is still one executable, using the distribution's WebKitGTK, with the probe page embedded. The root suite and mixer build, vet and test with `CGO_ENABLED=0` (root module unchanged).

## Recommendation

cgo does not buy signal coexistence, so it is not a reason to leave purego. The remaining decision is the same for both bindings, and binding-independent:

1. a GC-signal policy: an application-reserved signal through `JSC_SIGNAL_FOR_GC` with a never-send restriction, or the restore-after-initialization accommodation
2. a SIGXCPU policy on 2.52.x: an application restriction with an audit (but see the correction above: the restriction does not cover the real-time path)

Given those, cgo's remaining trade is ergonomics against a build toolchain and a load-time WebKit dependency in native-only executables.

## Not done, deliberately

Stopped at the brief's condition:

- **Not started:** two windows, close/reopen with pending requests, the inspector, the browser-policy fixture, packaging beyond the dependency listing, and a native-only package boundary.
- **Not tested:**
  - the real `RLIMIT_CPU` or `RLIMIT_RTTIME` paths, since sends here are explicit
  - a SIGXCPU or GC-signal delivery while a Go thread runs Go code, the stack-overrun hazard
  - other runtimes and WPE
  - the graphics-teardown concern, which stays separate
