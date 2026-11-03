---
title: Resolve WebKitGTK child process crash on exit
state: inbox
created: 2026-11-03
tags: [defect, spike]
log: 2026-11-03
---

validate a supported WebKitGTK runtime or backport as the remedy for the intermittent browser-child exit crash. start from the [shutdown investigation](../../../experiments/webkitgtk-spike/evidence/linux/shutdown/README.md): exact symbols strongly match the upstream Skia/EGL teardown race, but both installed 2.52.6 and isolated 2.54.2 libraries passed the repeat manual comparison. use the improved child-exit observer and docked-inspector audition to establish a supported deployment choice and carry the runtime prerequisite into dfx2's platform contract. don't infer a fix from host counters or one successful run, and don't add a host close-order workaround without evidence. preserve the no-cgo constraint and keep this distinct from JSC signal selection.
