"""gdb monitor for host-process JavaScriptCore use, loaded with gdb -x.

    VMWATCH_LOG=FILE gdb -q -batch -nx -ex 'set debug-file-directory DIR' \
        -x vmwatch_gdb.py --args BINARY ARGS...

It runs BINARY under gdb and writes one JSON line per breakpoint hit to
VMWATCH_LOG, then the per-breakpoint totals, resolved locations and the
inferior's exit. It follows no forks, so WebKit's web and network processes,
which legitimately run JavaScript, are never observed: every hit is in the
Go host process. The host does not know it is watched and reports nothing to
this script.

The internal C++ functions are local symbols, resolvable only with the
matching debug file for libjavascriptcoregtk in DIR. A breakpoint that
resolved to no location watches nothing; the totals record that, and a
positive control has to hit before an absence means anything.
"""
import json
import os
import time

import gdb

LOG = open(os.environ["VMWATCH_LOG"], "w", buffering=1)
FRAMES = int(os.environ.get("VMWATCH_FRAMES", "14"))
# High-frequency kinds: the first FULL hits per thread are logged with a
# backtrace, later ones only counted. Every other hit is logged.
FULL = 3
FREQUENT = {"jsc-initialize", "jslock-acquired", "thread-suspend", "suspend-handler-ran"}
# Watches that exist only in some WebKit versions; leaving one unresolved is
# expected, and the totals say which.
OPTIONAL = {"realtime-threads-created", "realtime-thread-registered", "highpriority-threads-created", "highpriority-thread-registered",
            "realtime-promote", "realtime-rtkit", "scheduling-attributes", "highpriority-rtkit", "realtime-sigxcpu-callback", "realtime-demote-all"}
# Kinds whose whole stack is kept, down to the Go frames that called in.
DEEP = {"vm-constructed", "global-context-create", "global-context-create-in-group", "jsc-context-new", "jsc-virtual-machine-new",
        "thread-registered-for-gc"}


def emit(**kv):
    kv["t"] = round(time.time(), 3)
    LOG.write(json.dumps(kv, default=str) + "\n")


def frames(limit):
    out, f = [], gdb.newest_frame()
    while f is not None and len(out) < limit:
        try:
            name = f.name() or "??"
            lib = gdb.solib_name(f.pc()) or "exe"
            where = ""
            sal = f.find_sal()
            if sal.symtab is not None:
                where = f" ({sal.symtab.filename.split('/Source/', 1)[-1]}:{sal.line})"
            out.append(f"{name} @ {os.path.basename(lib)}{where}")
            f = f.older()
        except gdb.error:
            break
    return out


def comm(pid, lwp):
    try:
        with open(f"/proc/{pid}/task/{lwp}/comm") as c:
            return c.read().strip()
    except OSError:
        return None


def process_comm(pid):
    try:
        with open(f"/proc/{pid}/comm") as c:
            return c.read().strip()
    except OSError:
        return None


class Watch(gdb.Breakpoint):
    """Logs and continues; never stops the inferior."""

    def __init__(self, kind, spec, arg=None):
        super().__init__(spec)
        self.kind, self.spec, self.arg, self.seen, self.hits = kind, spec, arg, {}, 0

    def stop(self):
        th = gdb.selected_thread()
        pid, lwp, _ = th.ptid
        self.hits += 1  # gdb's hit_count stays 0 when stop() declines
        n = self.seen.get(lwp, 0) + 1
        self.seen[lwp] = n
        if n <= FULL or self.kind not in FREQUENT:
            name = comm(pid, lwp)
            entry = {"kind": self.kind, "spec": self.spec, "pid": pid, "lwp": lwp, "main_thread": lwp == pid, "thread_name": name,
                     "named_like_process": name == process_comm(pid), "frames": frames(200 if self.kind in DEEP else FRAMES)}
            entry["go_frames"] = [f for f in entry["frames"] if f.startswith(("main.", "runtime.", "github.com/ebitengine/purego"))]
            if self.arg:
                try:
                    entry.update(self.arg())
                except gdb.error as e:
                    entry["arg_error"] = str(e)
            emit(**entry)
        return False


def policy_of(pid, tid):
    """A thread's scheduling as the kernel reports it, read from /proc."""
    try:
        with open(f"/proc/{pid}/task/{tid}/stat") as f:
            st = f.read()
    except OSError:
        return {"tid": tid, "gone": True}
    fields = st[st.rindex(")") + 1:].split()
    return {"tid": tid, "policy": int(fields[38]), "rt_priority": int(fields[37]), "nice": int(fields[16])}


class Returned(gdb.FinishBreakpoint):
    """Logs a watched call's return value (%rax; libc has no debug types
    here) and the target thread's scheduling afterwards."""

    def __init__(self, kind, pid, lwp, target):
        super().__init__(gdb.newest_frame(), internal=True)
        self.kind, self.pid, self.lwp, self.target = kind, pid, lwp, target

    def stop(self):
        try:
            value = int(gdb.parse_and_eval("(long)$rax"))
        except gdb.error:
            value = None
        emit(kind=self.kind + "-returned", lwp=self.lwp, value=value, after=policy_of(self.pid, self.target or self.lwp))
        return False

    def out_of_scope(self):
        emit(kind=self.kind + "-returned", lwp=self.lwp, value=None, out_of_scope=True)


def sched_args():
    # sched_setscheduler(pid, policy, param): pid 0 is the calling thread.
    pid, lwp, _ = gdb.selected_thread().ptid
    target = int(gdb.parse_and_eval("$rdi"))
    Returned("sched-setscheduler", pid, lwp, target)
    return {"target": target, "policy": int(gdb.parse_and_eval("$rsi"))}


def rtkit_args():
    # realTimeKitMakeThread*(this, processID, threadID, ...): the target is
    # the third argument.
    pid, lwp, _ = gdb.selected_thread().ptid
    target = int(gdb.parse_and_eval("$rdx"))
    Returned("rtkit", pid, lwp, target)
    return {"target": target}


def sigaction_args():
    # x86-64: rdi = signum, rsi = act (NULL for a query).
    return {"signal": int(gdb.parse_and_eval("$rdi")), "installs": int(gdb.parse_and_eval("$rsi")) != 0}


WATCHES = [
    ("jsc-initialize", "JSC::initialize()", None),
    ("vm-constructed", "JSC::VM::VM(JSC::VM::VMType, JSC::HeapType, WTF::RunLoop*, bool*)", None),
    ("heap-constructed", "JSC::Heap::Heap(JSC::VM&, JSC::HeapType)", None),
    ("global-context-create", "JSGlobalContextCreate", None),
    ("global-context-create-in-group", "JSGlobalContextCreateInGroup", None),
    ("context-group-create", "JSContextGroupCreate", None),
    ("jsc-context-new", "jsc_context_new", None),
    ("jsc-virtual-machine-new", "jsc_virtual_machine_new", None),
    ("jslock-acquired", "JSC::JSLock::didAcquireLock()", None),
    ("thread-registered-for-gc", "JSC::MachineThreads::addCurrentThread()", None),
    ("thread-group-add", "WTF::ThreadGroup::addCurrentThread()", None),
    ("thread-suspend", "WTF::Thread::suspend(WTF::ThreadSuspendLocker const&)", None),
    ("suspend-handler-ran", "WTF::Thread::signalHandlerSuspendResume(int, siginfo_t*, void*)", None),
    ("fault-handlers-finalized", "WTF::SignalHandlers::finalize()", None),
    # Version-specific: 2.52.6 has RealTimeThreads (SCHED_RR, SIGXCPU
    # handler); 2.54.1 replaced it with HighPriorityThreads (nice levels).
    ("realtime-threads-created", "WTF::RealTimeThreads::RealTimeThreads()", None),
    ("realtime-thread-registered", "WTF::RealTimeThreads::registerThread(WTF::Thread&)", None),
    ("highpriority-threads-created", "WTF::HighPriorityThreads::HighPriorityThreads()", None),
    ("highpriority-thread-registered", "WTF::HighPriorityThreads::registerThread(WTF::Thread&)", None),
    # Thread priority: WebKit's promotion attempts and the libc call they
    # make (2.52.6 calls sched_setscheduler; 2.54.2 uses syscall()).
    ("realtime-promote", "WTF::RealTimeThreads::promoteThreadToRealTime(WTF::Thread const&)", None),
    ("realtime-rtkit", "WTF::RealTimeThreads::realTimeKitMakeThreadRealTime(unsigned long, unsigned long, unsigned int)", rtkit_args),
    ("realtime-sigxcpu-callback", "WTF::RealTimeThreads::signalCallback(int, GIOCondition, void*)", None),
    ("realtime-demote-all", "WTF::RealTimeThreads::demoteAllThreadsFromRealTime()", None),
    ("scheduling-attributes", "WTF::Thread::updateSchedulingAttributes(WTF::ThreadSchedulingState) const", None),
    ("highpriority-rtkit", "WTF::HighPriorityThreads::realTimeKitMakeThreadHighPriority(unsigned long, unsigned long, int)", rtkit_args),
    ("sched-setscheduler", "sched_setscheduler", sched_args),
    # Native handler installation. The Go runtime calls rt_sigaction
    # directly, never libc's wrapper, so every hit here is native code.
    ("libc-sigaction", "sigaction", sigaction_args),
]

gdb.execute("set pagination off")
gdb.execute("set confirm off")
gdb.execute("set breakpoint pending on")
gdb.execute("set print thread-events off")
gdb.execute("set print inferior-events off")
gdb.execute("set startup-with-shell off")
gdb.execute("set detach-on-fork on")
gdb.execute("set follow-fork-mode parent")
gdb.execute("handle all nostop noprint pass")
watches = [Watch(*w) for w in WATCHES]
exit_code = {"code": None, "signal": None}


def exited(event):
    exit_code["code"] = getattr(event, "exit_code", None)


gdb.events.exited.connect(exited)
emit(kind="monitor-start", args=gdb.parameter("args"))
try:
    gdb.execute("run")
except gdb.error as e:
    emit(kind="run-error", error=str(e))
try:
    sig = gdb.parse_and_eval("$_exitsignal")
    exit_code["signal"] = None if str(sig) == "void" else int(sig)
except gdb.error:
    pass
emit(kind="totals", watches={w.kind: {"hits": w.hits, "threads": len(w.seen), "locations": len([l for l in w.locations if l.enabled]), "pending": w.pending, "optional": w.kind in OPTIONAL}
                             for w in watches})
emit(kind="inferior-exit", exit_code=exit_code["code"], exit_signal=exit_code["signal"])
code = exit_code["code"] if exit_code["code"] is not None else (128 + exit_code["signal"] if exit_code["signal"] else 1)
gdb.execute(f"quit {code}")
