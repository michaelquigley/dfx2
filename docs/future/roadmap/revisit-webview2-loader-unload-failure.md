---
title: Revisit WebView2 loader unload failure
state: inbox
created: 2026-11-24
tags: [defect]
source: Terminus af5151522c39, webview2-loader-unload-loses-ownership
log:
  - stamp: 2026-11-24
    note: "Context: docs/journal/2026-11-24.md"
---

Assess the handling of a failed WebView2 loader unload when concrete runtime evidence warrants it. Last-user release clears ownership before FreeLibrary succeeds, although the unload error reaches cleanup. The runtime-version failure path also ignores the unload result before attempting directory removal. Establish the practical consequence before introducing retries or extending attachment lifetime.

## disposition

Michael deferred loader-unload retry machinery on 2026-11-24. No real unload failure has been reproduced. The development-stage priority is a complete usable application; this item is not a standing instruction to add a retry subsystem.
