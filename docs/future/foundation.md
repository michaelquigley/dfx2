# dfx2 foundation: remaining integration work

The application/window runtime, synthetic mixer and webview integration arcs are accepted. Built contracts live in [runtime.md](../current/runtime.md), [ui.md](../current/ui.md) and [webview.md](../current/webview.md). The [webview stage 4 record](../../evidence/webview-integration/stage-4.md) preserves validation, accepted limits and deferred findings. The fulfilled integration spec and work order are retired; git history retains them. This document holds the remaining planning questions, not a new implementation order.

## Sessionmixer integration plan

Use the synthetic result to scope one real strip and its controller before expanding toward the full product. The next discussion should establish:

- Which real Sessionmixer device/controller path supplies gain, mute and meters for the first strip.
- How hardware acknowledgments, external changes, delayed commands and rejection map to the application's displayed and pending values.
- What that strip needs from the existing immediate-mode controls, and whether a browser view is useful in the first slice.
- How to test it with and without connected hardware, keeping device failures separate from framework lifetime failures.

Keep the existing application's hardware dependencies separate from dfx2's no-cgo guarantee. Application values and commands remain independent of rendering and HTTP. Shared workers belong to the application, and closing a view does not own or stop the controller. Device-specific units, taper, ganging, production meter ballistics and hardware acknowledgment belong to Sessionmixer. Expand only after this slice feels right; source compatibility with dfx or dfw is not required.

## Deferred capabilities

Native/browser composition within one window, arbitrary JavaScript evaluation or RPC, public readiness callbacks, persistent/shared browser profiles, browser recovery, authentication UI, downloads, permission-granting APIs, service workers, iframe hosting and broad external-site compatibility remain outside the built webview contract. Require an actual consumer and an explicit policy before adding them. External-client tokens, separate cookie sessions or cookie-less loopback access also remain separate access-policy work.

In-process streaming and precise fetch-abandonment cancellation remain unsupported; loopback is the explicit alternative where its contract supplies the needed facilities. WebSocket integration needs a separate ownership contract. Generated TypeScript and tray/discovery conveniences wait for a product need.

GPU rendering, grids and scrolling, menus/dialogs/docking, the full widget library, persistent window state and macOS remain future work. Begin with the demonstrated Linux/Windows amd64 paths; additional targets need their own evidence. IME, accessibility, mixed DPI and text editing remain product-readiness work. Stable labels and semantic roles in non-editable controls do not constitute accessibility integration; editable text needs composition, selection and caret contracts.

Fader appearance and mouse-wheel editing remain separate [visual](roadmap/improve-fader-appearance.md) and [input](roadmap/support-mouse-wheel-fader-edits.md) follow-ups. Native text engines, full color-font coverage and general interpolation helpers wait for demonstrated need; current text behavior and cache-pressure costs are in [ui.md](../current/ui.md).

Known browser risks and accepted defects are recorded in [webview.md](../current/webview.md) and the [roadmap](roadmap/). The Linux signal and child-exit investigations stay monitored compatibility risks, not prerequisites to restart before Sessionmixer planning. A successful audition or a temporary mixed-library comparison is not proof that those risks were fixed.
