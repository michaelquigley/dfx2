---
title: Investigate finite read buffer reservation lifetime
state: inbox
created: 2026-11-25
tags: [defect]
source: Terminus 1b552e54b3c0, finite-read-reservation-released-early
log:
  - stamp: 2026-11-25
    note: "Context: docs/journal/2026-11-25.md"
---

Check whether the finite endpoint's early read-buffer credit permits a material aggregate-budget overshoot while a browser still retains the buffer. `stopReadingLocked` and successful `RequestBodyEnd` clear the charge before request release; the Windows `readDone` callback still has its result buffer. Distinguish the request admission limit from the byte reservation. Establish the overlapping lifetime and admission with a focused test before choosing a correction. This accounting discrepancy is deferred from webview stage 4: no native overshoot or material application failure was demonstrated. Do not introduce new ownership machinery solely to close the review finding.
