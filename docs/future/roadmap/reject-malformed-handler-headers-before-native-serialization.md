---
title: Reject malformed handler headers before native serialization
state: inbox
created: 2026-11-24
tags: [defect]
source: Terminus round 327db59389e8, finding W2 (webview stage 4 triage)
log:
  - stamp: 2026-11-24
    note: "Context: docs/journal/2026-11-24.md"
---

Reject a finite (in-process) response whose handler-supplied header names or values are malformed, before any browser host serializes it. Answer it with the endpoint's existing fixed 502 for malformed metadata, for both engines. No layer validates names or values today: `internal/endpoint/finite.go` measures them but passes them through. On Windows, `deliver` in `internal/platform/webview2/browser.go` joins them into one header string, and `utf16z` replaces any string it cannot convert, such as one containing a NUL, with the literal `invalid string`. So a single bad header from the application's handler replaces the whole response header block, and **the framework's Content-Security-Policy is lost**, with the response still delivered. It takes an application handler mistake, not anything a page can send. Check WebKitGTK's path for the same exposure. Cover NUL, CR and LF in names and values, and an invalid name token, with modeled tests for each engine.
