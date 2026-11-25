---
title: Revisit WebView2 redirect Retry method evidence
state: inbox
created: 2026-11-24
tags: [enhancement]
source: Terminus af5151522c39, webview2-retry-inherits-unconfirmed-method; canon 76e1f94
log:
  - stamp: 2026-11-24
    note: "Context: docs/journal/2026-11-24.md"
---

When revisiting load-error behavior, consider requiring a document request for the current redirect hop before offering Retry. Today an unobserved hop can inherit the previous request's GET. Preserve GET-only actions and rejection of stale actions. This is an accepted development-stage follow-up, not a stage 4 prerequisite; Retry does not replay a POST.
