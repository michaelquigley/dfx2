---
title: Dispatch public callbacks during Win32 modal size and move loops
state: evaluating
created: 2026-11-04
tags: [enhancement]
log: docs/journal/2026-11-04.md
---

observe the built Win32 size/move dispatch with real applications. The synthetic-mixer arc implements bounded turns during size/move loops, with native validation and accepted limits in [ui.md](../../current/ui.md#dispatch-during-windows-size-and-move-loops). Other native modal loops remain outside that guarantee; expand it only for a concrete need.

## why

Michael's 2026-11-04 Windows audition saw both windows' title counters freeze while resizing either one. the lifetime example only updates titles, but the same deferral would stall meters and animation in a mixer window during a resize, which is exactly when a user is looking at it. That observation led to the now-built modal dispatch path; the historical foundation pause is superseded.
