---
title: Decode Windows large image audition snapshots
state: inbox
created: 2026-11-25
tags: [defect]
source: Terminus 1b552e54b3c0, windows-large-snapshot-header-only
log: 2026-11-25
---

Make the Windows large-image audition decode each retained PNG before accepting its dimensions. `Get-PngSize` in `evidence/synthetic-mixer/windows/audition.ps1` reads only the first 24 bytes, so a truncated file can satisfy the gate. Keep the existing same-run snapshot and observed-surface checks, and verify rejection of a header-only or truncated image. This is deferred driver work: all five large-case PNGs from the stage 4 Windows return fully decoded during Linux review, so this gap did not invalidate that run's artifacts.
