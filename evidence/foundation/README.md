# Foundation validation

- [Stage 1](stage-1.md): portable lifecycle core, fake backend, concurrency/failure tests and clean Terminus review; committed as `82487b1` before stage 2.
- [Stage 2 Linux](linux/README.md): GTK backend, lifetime fixture, native scripts and Michael's manual audition.
- [Stage 3 Windows](windows/README.md): Win32 implementation, portable protocol/layout checks, native Windows matrix and manual audition. The runner-timeout correction passed review and native Windows execution. Michael accepted stage 3 through `405adb3` before stage 4.
- [Stage 4](stage-4.md): final Linux return checks, common-tree acceptance matrix, retained Windows execution, platform limits and final review status.

The [runtime contract](../../docs/current/runtime.md) describes built behavior. The fulfilled runtime work order is retired; the broader [foundation proposal](../../docs/future/foundation.md) retains subsequent UI/webview work.
