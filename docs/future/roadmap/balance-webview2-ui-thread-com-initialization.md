---
title: Balance WebView2 UI-thread COM initialization
state: inbox
created: 2026-11-24
tags: [defect]
source: Terminus round 7b428eff75ad, finding X2 (webview stage 4 triage)
log: 2026-11-24
---

Pair each successful UI-thread `CoInitializeEx` that WebView2 browser creation makes (`initCOM` in `internal/platform/webview2`) with a `CoUninitialize` on the same thread. That covers both startup rollback and final attachment release, after the browser's native objects and callbacks have drained. **Today successful COM initialization calls are left unbalanced:** each browser window opened adds one, and nothing removes them. The UI thread lives as long as the process, so the visible effect is COM's per-thread count growing with every window. Keep the existing handling of `S_FALSE` and `RPC_E_CHANGED_MODE`, and validate natively on Windows.
