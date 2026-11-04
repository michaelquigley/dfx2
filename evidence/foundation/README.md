# Foundation validation

- [Stage 1](stage-1.md): portable lifecycle core, fake backend, concurrency/failure tests and clean Terminus review; committed as `82487b1` before stage 2.
- [Stage 2 Linux](linux/README.md): GTK backend, lifetime fixture, native scripts and Michael's manual audition.
- Stage 3 Windows: implementation and actual Windows execution remain pending. A cross-build is not a Windows audition. The operator commits and transfers the common tree to the Windows host before that stage's native validation.

The [runtime contract](../../docs/current/runtime.md) describes built behavior. The [work order](../../docs/future/foundation-work-order.md) controls the remaining stages; the broader UI/webview design remains future work.
