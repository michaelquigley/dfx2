# Same-URI error action: before-fix reproduction

The new `error_actions_same_uri` driver case ran on the preceding candidate, browser SHA-256 `c985e240c5437042fb3bd5e41230fbba5a9bdfa7ac666b1c86d08809fc7a43f1`, under isolated Wayland on 2026-11-14. The service first drops `/recover`, then serves a successful document at that identical URL. After the recovered title is observed, the driver requires the internal action command to refuse because no current error page remains. The old binary instead accepts Retry, so the check fails waiting for the expected refusal. This is an intended regression failure, not a failed run of the corrected candidate.

```sh
python3 -B evidence/synthetic-mixer/linux/controls_drive.py /tmp/dfx2-stage2-review-webview --out evidence/webview-integration/linux/stage-2-actions-before/session --base /tmp --timeout 120 --exec python3 -B evidence/webview-integration/linux/webview_drive.py /tmp/dfx2-stage2-review-webview --out evidence/webview-integration/linux/stage-2-actions-before --gdk-backend wayland --only error_actions_same_uri
```

The unit regression separately submits both captured old action URLs to the attachment after a same-URI navigation; both caused native loads before the fix. The native driver checks that the action state is retired after a successful real load, without exposing tokens or evaluating JavaScript in the host.
