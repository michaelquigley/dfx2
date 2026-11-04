---
title: Dispatch public callbacks during Win32 modal size and move loops
state: inbox
created: 2026-11-04
tags: [enhancement]
log: docs/journal/2026-11-04.md
---

on Windows, dragging a window's border or title bar pauses posted callbacks for every window in the application until the mouse is released: `DefWindowProc` runs a modal size/move loop inside `Pump`'s dispatch call, and the Win32 backend only latches readiness on `WM_APP`, running public callbacks when control returns to `Pump`. decide whether the foundation should keep that contract or drain owners from inside the modal loop, and implement the decision for both backends consistently.

candidates: handle `WM_ENTERSIZEMOVE`/`WM_EXITSIZEMOVE` with a short `WM_TIMER` on the dispatch window that services the bounded callback batch, or run the batch from the `WM_APP` handler itself when the pump is known to be inside a native loop. either path must keep the existing guarantees: no public callback starts inside a creation or destruction call stack, batches stay bounded and yield to paint, close/quit/completions never wait on the public queue, and `ErrBusy` behavior for a saturated owner stays explicit. GTK does not have this stall; do not make the common core Windows-shaped to fix it.

## why

Michael's 2026-11-04 Windows audition saw both windows' title counters freeze while resizing either one. the lifetime example only updates titles, but the same deferral would stall meters and animation in a mixer window during a resize, which is exactly when a user is looking at it. `docs/current/runtime.md` records the current behavior as the contract until this is decided.
