---
title: Bound page-controlled URIs retained in browser audit events
state: inbox
created: 2026-11-20
tags: [defect]
source: Terminus round 9c65a72a1b1d, finding N4 (stage 3 triage)
log:
  - stamp: 2026-11-20
    note: "Context: docs/journal/2026-11-20.md"
---

Browser audit events keep page-controlled URIs past the synchronous request call, outside every endpoint reservation. `blocked` is the concrete case: WebView2 records the full URI of every off-origin request a page makes. In-process, each URI can be as long as the metadata limit, so a page can fill the 64-event ring with about 4 MiB that `Buffers` never counts. `document-request`, the navigation events, `error-page` and `error-action` keep URIs the same way, and WebKitGTK's equivalents should be checked too.

Bound what these events keep. For example, truncate to a fixed prefix with the original length, or count the bytes against a budget. Keep enough for the native matrices' URI matching, which reads these events. `request-content` was already reduced to the getter's result in the N4 correction and is not part of this item.
