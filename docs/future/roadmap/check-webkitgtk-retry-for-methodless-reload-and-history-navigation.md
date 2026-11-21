---
title: Check WebKitGTK Retry for methodless reload and history navigation
state: inbox
created: 2026-11-20
tags: [defect, spike]
source: Terminus round f7aee917e2b7, finding V5 (stage 3 triage)
log: 2026-11-20
---

Check WebKitGTK's error-page Retry against its documented promise: Retry is offered only for a *known* GET. `navigationMethod` in `internal/platform/webkit/policy.go` treats any methodless navigation other than a form submission or resubmission as GET. That includes reloads and back/forward history navigations, which can return to a page that a POST produced. Because Retry always reloads the URI with GET, it can never resubmit a POST; the question is only whether it is offered when the method isn't actually known. Establish natively what WebKit reports, both the method and the navigation type, for a reload of a POST result and for history navigation to one, in each owned transport. Then either narrow the classifier so those cases withhold Retry, or show they can't reach the error page with an unknown method. Keep Return to start unaffected.
