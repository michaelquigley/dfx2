# Foundation validation

- [Stage 1](stage-1.md): portable lifecycle core, fake backend, concurrency/failure tests and clean Terminus review; committed as `82487b1` before stage 2.
- [Stage 2 Linux](linux/README.md): GTK backend, lifetime fixture, native scripts and Michael's manual audition.
- [Stage 3 Windows](windows/README.md): Win32 implementation, portable protocol/layout checks, and the native Windows matrix executed on 2026-11-04 (nine cases, including a console control event). Michael's manual audition passed the same day, with interactive resize deferring every window's callbacks and DPI untested over RDP. The Linux return checks pass on `87b628f`; Terminus return review is clean; the runner-timeout correction also passed Terminus (`e81c9ae5cc96`) but awaits Windows execution; Michael's stage review remains pending.

The [runtime contract](../../docs/current/runtime.md) describes built behavior. The [work order](../../docs/future/foundation-work-order.md) controls the remaining stages; the broader UI/webview design remains future work.
