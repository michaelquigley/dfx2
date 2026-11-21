---
title: Investigate a WebKitGTK content filter completing after owner close
state: inbox
created: 2026-11-20
tags: [defect, spike]
source: Terminus round f7aee917e2b7, finding V3 (stage 3 triage)
log: 2026-11-20
---

Establish what happens when a WebKitGTK window's content-filter save completes after its core owner has started closing but before the browser has received `Close`. The filter callback checks only the browser's own `closing` flag, so it can still install the filter. `maybeNavigate` can then call `host.Ready()` and start the first load for a window whose owner is already closing. Trace the core's close ordering in `internal/appcore` against `loadFilter` and `maybeNavigate` in `internal/platform/webkit/browser_linux_amd64.go`, and find out whether that window exists. If it does, decide whether it has an observable consequence: a `Ready` the core no longer expects, a navigation started during close, or extra work before release. If it does, fix it at the boundary the evidence points to, with a test that forces the ordering. If it doesn't, record why and close this item. The stage 2 F1 decision stands: a successful late filter is released without being installed once the browser is closing.
