---
title: Investigate failed WebView2 blocking responses
state: inbox
created: 2026-11-24
tags: [defect, spike]
source: Terminus af5151522c39, webview2-block-response-fails-open
log:
  - stamp: 2026-11-24
    note: "Context: docs/journal/2026-11-24.md"
---

Establish what happens to an off-origin request if WebView2 cannot create or install the replacement blocking response. The current refusal path ignores these failures and records the request as blocked. Determine whether the request can reach the network and whether a small supported mechanism can prevent that. Do not assume that reporting an application failure stops the request before its callback returns.

## disposition

Michael deferred this investigation on 2026-11-24 under the development-stage scope. Neither the native failure nor a resulting request escape has been reproduced. A leaked loopback credential is a possible consequence, not an observed result. Revisit on concrete runtime evidence or before making a stronger confinement guarantee.
