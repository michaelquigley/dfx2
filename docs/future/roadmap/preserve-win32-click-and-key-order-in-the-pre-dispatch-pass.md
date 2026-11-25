---
title: Preserve Win32 click and key order in the pre-dispatch pass
state: inbox
created: 2026-11-24
tags: [defect]
source: Terminus round 7b428eff75ad, finding X1 (webview stage 4 triage)
log:
  - stamp: 2026-11-24
    note: "Context: docs/journal/2026-11-24.md"
---

Make the Win32 backend's pre-dispatch message pass in `internal/platform/win32/backend_amd64.go` retrieve keyboard and pointer input in the order it was queued. It currently peeks the keyboard range before the mouse ranges, so a pointer press queued before a key can be handled after it. **That can reverse a user's click and key order:** for example, a click that moves focus followed by a key press, where the key reaches the old focus. Retrieve input in one bounded, order-preserving pass. Cover it with a modeled pump test of a focus-changing press followed by a key edge, and validate natively on Windows. This is accepted foundation and synthetic-mixer code; change it deliberately, not as part of the webview arc.
