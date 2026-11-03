---
title: Windows WebView2 spike
state: inbox
created: 2026-11-03
tags: [feature, spike]
---

execute [the Windows WebView2 spike brief](../windows-webview2-spike.md) on the Windows development host. host WebView2 without cgo in a separate experiment, serving the same page and Go HTTP handler through loopback and socket-free request interception. prove finite request/response handling, UI responsiveness, and close with pending work; return implementation, audition evidence, and explicit transport limitations for dfx2's platform design.

## why

the native-window spike established enough Win32 and background-wakeup evidence to proceed. WebView2 now needs to establish its COM, request, and lifetime constraints before the shared platform contract is settled.
