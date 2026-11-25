---
title: Preserve browser startup errors at Open
state: inbox
created: 2026-11-24
tags: [enhancement]
source: Terminus af5151522c39, browser-open-masks-synchronous-start-failure; canon 76e1f94
log:
  - stamp: 2026-11-24
    note: "Context: docs/journal/2026-11-24.md"
---

When refining browser creation errors, return a synchronous startup failure's cause from webview.Open instead of ErrClosed. Keep asynchronous failure reporting and retention of the original cause in Run. This is an accepted development-stage follow-up, not a stage 4 prerequisite; the current failure still cancels and drains the application and is returned by Run.
