# UI content

Stages 1 and 2 of the [synthetic mixer](../future/synthetic-mixer.md) are built: windows whose content is an immediate-mode view drawn by the framework, with labels and keyed animated meters in one column, CPU rendering and pure-Go text. Linux/amd64 presents it through GTK and has passed native auditions. Windows/amd64 presents it through GDI, and **that code has not yet executed on Windows**: it is cross-built and tested with injected calls, so everything below about Win32 and size/move loops describes the implementation awaiting its [native audition](../../evidence/synthetic-mixer/windows/README.md), not observed behavior. Layout primitives and interactive controls are later stages. The [stage 1](../../evidence/synthetic-mixer/stage-1.md) and [stage 2](../../evidence/synthetic-mixer/stage-2.md) records hold the commands, native coverage and limits.

The [runtime contract](runtime.md) still governs lifetime, threads, posting, failure and shutdown. This document covers only what UI content adds.

## Programming model

```go
err := dfx2.Run(ctx, dfx2.Options{}, func(app *dfx2.App) error {
	w, err := ui.Open(app, dfx2.WindowOptions{Title: "Levels", Width: 560, Height: 260}, func(f *ui.Frame) {
		s := source.Snapshot() // application-owned, synchronized state
		f.Label(fmt.Sprintf("Level %.1f dB", s.DB))
		f.Meter("level", s.Level) // normalized [0, 1]
	})
	if err != nil {
		return err
	}
	// A worker publishes new samples, then requests a frame from any goroutine.
	return app.Go("sampler", func(ctx context.Context) error { return source.Run(ctx, w.Invalidate) })
})
```

`ui.Open(app, options, draw)` creates a window on the UI thread under `App.NewWindow`'s rules: creation failures are returned and latched as application failures. `ui.Window` forwards `Close`, `Context`, `Done`, `SetTitle`, `Post` and `Go` with their documented thread rules and adds `Invalidate`. It exposes no native handles.

The application owns its domain values and composes the view with ordinary Go functions; there is no component base type or framework state container. A `*ui.Frame` is valid only during the draw call. `Label(text)` records wrapped, stateless text. `Meter(key, level)` records a display-only meter whose retained state lives under `key`; it reports no changes. Widgets are placed top to bottom with 16 logical units of padding and 10 between them. Each widget clips to its own rectangle, and text wraps to the column without breaking inside a word, so an unbreakable word is clipped.

Programmer mistakes panic: a duplicate or empty key, a level outside [0, 1] or NaN, and use of a frame after its draw call returned. The panic becomes an application failure with its stack and the window/frame in the error, and the runtime's cancel-and-drain path follows. It never unwinds through a native callback.

The meter's displayed peak attacks immediately and falls linearly at 0.5 units per second of monotonic time; it requests animation only while the peak is above the latest level. Removing a meter's key from the view discards its state; reintroducing the key starts fresh.

Linux and Windows both host content, and other targets fail earlier, in `Run`. The core still honors a backend that refuses content before native work: `ui.Open` returns an error matching `errors.Is(err, ui.ErrUnsupportedContent)`, nothing is latched and ordinary `dfx2` windows keep working. The refused window still counts for last-window shutdown: while its creation was pending it was a live starting window, so if every other window closed meanwhile, a window application shuts down once the refusal returns. A resident application stays alive. No shipping backend refuses today; the path is exercised with the fake backend.

## Frames and scheduling

A frame is one evaluation of the view: the framework calls `draw` once, measures the record, renders it and presents the image. Layout, rewrapping and exposure never call `draw` again. Frames arise from `Invalidate`, a changed surface size or scale, and active animation. An exposed window whose image is still valid only repaints the retained image.

`Invalidate` is safe from any goroutine, never waits and never uses one of the window's 256 post slots. It advances a per-window generation under the runtime's producer mutex and shares the ordinary wake and wake-failure machinery; repeated calls coalesce and no goroutine is created. A frame clears only the generation it sampled at its start, so an invalidation racing a frame in progress causes a later frame. After the window starts closing, `Invalidate` returns `dfx2.ErrClosed`.

Each window has its own scheduling state: surface size, scale and visibility, a generation mark, a next animation deadline and a frame counter. A dispatch turn renders at most one frame per due window, in rotating window order, after the turn's bounded public callbacks; a frame requested while rendering waits for a later turn. A frame clears the window's animation request as it starts, and the view renews it only if another frame is needed; the next deadline is the frame's start plus 1/60 s of monotonic time. That is one-shot pacing, not display synchronization. Missed frames neither slow animation nor cause a catch-up burst, because animation reads elapsed time.

One app-owned one-shot timer on the runtime's supervisor serves the earliest live deadline and delivers through the private wake path; rendering stays on the UI thread. No live deadline means no armed timer, so an unchanged, settled window does no periodic work. A deadline the supervisor first sees already due is delivered by an immediate wake; an armed timer keeps its deadline even when the supervisor evaluates late. Timers stop before the dispatch target is released.

Windows reported hidden by the backend (unmapped or minimized), and zero-sized windows, render nothing and keep no armed deadline, while keeping their dirty mark. GTK/Wayland may not report minimization; its accepted limitation is described below. Becoming visible again renders once at the current monotonic time if the window is dirty or its animation deadline has passed. Compositor occlusion of a mapped window is not detected.

Closing seals content work at once: no further frame starts, a frame already rendering is not presented, the window's deadline stops counting toward the timer, and `Invalidate` is refused. After native teardown and its final callback, the content's caches, retained state and Go frame are released on the UI thread, outside the runtime's mutex. Until that release returns the window stays registered as closing: `Inspect` counts it and shutdown diagnostics report `content-release-pending=true`. Only then is it removed and `Done` closed.

Only one dispatch turn runs at a time, whichever native path delivered it, and setup counts as one. The core refuses a wake that arrives during a turn: no application callback, frame or view evaluation ever nests inside another. That wake is spent, so the running turn's closing check requests another if work remains. Backends need no knowledge of the refusal. On GTK it does not arise in practice; on Win32 it backs the size/move guards described below.

A view panic, a rendering error (for example an image over the size limit), a text-system error or a presentation failure enters the runtime's fatal cancel-and-drain path with the window ID and frame number in the error. A panic in content release is a cleanup failure: `Run` returns it, with its stack, alongside any earlier failure.

## Rendering

Each window renders into one reusable, opaque, premultiplied BGRA frame on the CPU: clipped fills, antialiased lines, glyph coverage masks and premultiplied color glyphs. Logical geometry multiplies by the surface scale into physical pixels. Image dimensions use checked arithmetic and a 256 MiB limit per image; exceeding it is a reported rendering failure, and a failed resize keeps the previous frame. Storage is reused unless it is more than four times the new size.

Presentation hands the complete frame to the backend, which copies it synchronously into storage the backend owns; native code never retains the Go slice. That copy keeps ownership simple for the first renderer and has not been measured as a bottleneck.

## Text

Text runs through a private engine seam. The single engine, go-text, finds faces with per-rune fallback (preferred families, then the generic emoji family), shapes with its HarfBuzz port, resolves bidi levels and UAX #14 break opportunities, and rasterizes outlines with slight vertical hinting: zones and the x-height overshoot snap to whole pixels, while advances stay fractional. Pens are positioned to the nearest quarter pixel and baselines to whole physical pixels. PNG color glyphs (Noto Color Emoji's strikes) are scaled. SVG glyphs use their fallback outline when one is present. Formats the engine cannot draw, including SVG without a fallback outline, COLR paint and other bitmap encodings, render as a visible hollow box rather than blank space. Spaces inside fallback-font runs take the preferred face, so a space after an emoji keeps normal width.

Shaped data crossing the seam is engine-neutral and owned: font and glyph IDs, glyphs in visual order with cluster, advance and offsets, runs with bidi level and line metrics, and break classes. Wrapping and line assembly are shared code over that data. A paragraph is shaped once per text and size; a new width rewraps the same shaped runs without reshaping and without mutating them. Lines reorder runs by the UAX #9 L2 rule from their levels; trailing whitespace keeps its glyphs but no advance. Paragraph direction is always left to right; there is no ellipsis, justification, caret or selection.

Each window owns bounded caches: at most 16 MiB of glyph pixels, and 1 MiB of shaped and wrapped paragraph payload in at most 256 entries. Glyph rasters are keyed by font, glyph, physical size (scale included) and quarter-pixel position; mask and color glyphs are stored separately and share the pixel budget. Entries are evicted only between frames, least recently used first, to restore the budgets and then to make room for entries the last frame could not admit. Every reference handed out during a frame stays valid until the frame ends, and evicted storage is dropped, never recycled. An entry that does not fit is used transiently for that frame only; transient scratch is bounded by the image limit and released when the frame ends. Wrapping is charged before it is kept, because a narrower width can more than double a paragraph's payload. A cached paragraph stores a new wrap only if the paragraph budget still holds; otherwise the new lines are lent as scratch for that frame, the entry keeps its previous wrap, and the shortfall is made up by evicting unused entries between frames, so a later frame stores the wrap without reshaping. Replacing a cached wrap already returned in this frame moves its old buffer into scratch accounting until frame end. An uncached paragraph charges every new wrap in full to scratch, retaining the charge for its earlier wraps. Reusing the stored wrap at the same width adds no charge. Exceeding the scratch limit is refused with an error, which fails the frame, and nothing is stored.

The frame's draw commands drop their borrowed text lines before scratch accounting resets, including on rendering errors and panics. The scene retained for internal inspection contains layout and command metadata, without text buffers.

Font discovery is separate from those budgets. The system font index is built on the first UI frame, cached on disk under the user cache directory (`dfx2/fontscan`), and kept with its loaded faces for the life of the process, like the native toolkit libraries. Windows hold only font IDs; closing a window releases its caches. The preferred family list defaults to `sans-serif`.

## Linux presentation

A content window adds a GTK drawing area to its toplevel before the window is shown, with the area referenced by the backend until the window's final release. Permanent shared callbacks, routed by numeric token, handle the area's `draw`, `size-allocate`, `notify::scale-factor`, `map` and `unmap` signals and the toplevel's `window-state-event`. Surface handlers copy the allocation, GTK's integer scale factor and visibility (mapped and not withdrawn or iconified) into a `SurfaceChanged` event only when one of them changes. The draw handler only paints the retained image; it never asks the core for a frame.

Presentation copies the frame into a Cairo-owned ARGB32 image surface: flush, row-by-row copy honouring Cairo's stride, mark dirty, set the device scale, queue a draw. Same-sized frames reuse the surface. A size change creates, checks and fills the replacement before destroying the previous surface, so a Cairo failure keeps the last valid image; an error surface is destroyed too. Content and drawing signals disconnect before teardown, and the retained surface is destroyed with the backend's final references after the destroy signal stack returns, before destruction is acknowledged.

GTK 3 learns of minimization only when the window system reports it. Under X11 (including XWayland) iconified windows suspend rendering and deadlines. In the GNOME Wayland session tested, a minimized window stops painting but GTK reports no iconified state, so frames and animation deadlines continue while it is minimized. The scale is GTK's integer factor; fractional desktop scaling is applied by the compositor afterward.

## Windows presentation

A content window has no child surface: the framework draws its client area directly. Surface events come from `WM_SIZE` and `WM_SHOWWINDOW` and from `WM_DPICHANGED`, whose scale is the window's DPI over 96, carrying the full client rectangle in physical pixels, only when a value changes. `SIZE_MINIMIZED` clears visibility and keeps the last client size, because Windows reports 0×0 while minimized and restoring an unchanged window should not read as a resize. Closing seals these reports.

Presentation copies the frame row by row into one `VirtualAlloc` allocation per window, then calls `InvalidateRect`, which only marks the window. Storage carries 25% headroom up to the 256 MiB limit, so growing by dragging rarely reallocates, and it is reused unless more than four times too large. A replacement is allocated and filled before the old allocation is freed, so a failed allocation keeps the last image and enters the fatal path. Native storage is a workaround the [text spike](../future/text.md) found for black frames: GDI set no lines for Go heap frames straddling a 4 MiB boundary. That allocation-boundary explanation remains an inference from one host.

`WM_PAINT` runs `BeginPaint`, `SetDIBitsToDevice` with a top-down 32-bit `BI_RGB` header, and `EndPaint`. It never asks the core for a frame. `WM_ERASEBKGND` fills only what the retained image will not cover, in black, so a grown window shows black rather than the class's white until its next frame. A failed `BeginPaint`, a failed fill, or a blit that sets no lines fails the application: the step counts in `Counters.PaintFailures` and its error reaches `Run` through `Pump`, with `EndPaint` still balancing a successful `BeginPaint` and `DefWindowProc` validating after a failed one. There is no exception for lock screens or disconnected sessions; what those do to painting has not been observed on a Windows host. Retained storage is freed after `WM_NCDESTROY`'s stack has returned, immediately before the destruction acknowledgment. A failed free keeps the allocation owned by its window: the first failure fails the application, `Counters.Problem` carries it to the shutdown diagnostics, and a one-shot 10 ms timer on the dispatch window retries it. The window's `Destroyed` waits until every allocation is released, so a persistent failure keeps `Done` and `Run` pending, as other stuck native cleanup does. `Destroyed` also carries each allocation's first error, so `Run` returns it even if an earlier failure took first place and the retry later succeeded. Ordinary windows keep the default paint and erase handling.

Desktop composition retains window contents, so occluding and uncovering a window normally sends no `WM_PAINT`. Exposure still repaints correctly when it does arrive; the audition forces one with `InvalidateRect`.

## Dispatch during Windows size and move loops

Dragging a title bar or border runs a modal loop inside `DefWindowProc`, so control does not return to `Pump`. During that loop every window keeps receiving bounded dispatch turns, so frames and posted callbacks keep advancing:

- `WM_ENTERSIZEMOVE` and `WM_EXITSIZEMOVE` track the loop's window. `WM_APP` still only latches readiness; inside a loop it additionally schedules delivery. It never runs callbacks from its own handler.
- Delivery is a one-shot timer on the application's message-only dispatch window, with a 10 ms delay (`USER_TIMER_MINIMUM`) that yields to the loop's own input and paint. Its handler kills it on entry, then delivers one ordinary core turn: at most 64 callbacks and one frame per window. A turn leaving work behind requests a wake, whose message arms the next one-shot; animation deadlines arrive the same way, from the supervisor's timer. So modal frame pacing is the deadline plus this delay, and animation stays correct because it reads elapsed time.
- With no latched wake nothing is armed, so holding a drag still on a settled application runs no timer at all.
- A turn runs only with the loop established, no other turn active and no unfinished native operation. `Create`, `Destroy`, `SetTitle`, `ShowWindow` and DPI repositioning count as native operations. A refused delivery keeps the wake latched; the end of that turn or operation re-arms. Leaving the loop kills the timer and hands any latched wake to `Pump`, which delivers it when the message's stack returns. A latched native failure also holds delivery, so the failure reaches appcore through `Pump` before more callbacks start. A failed `SetTimer` keeps the wake and reports through `Pump` the same way.
- Timer IDs are never reused, so a late `WM_TIMER`, which `KillTimer` does not remove from the queue, cannot pass for the live timer. Release stops any armed timer before destroying the dispatch target.
- Destruction is acknowledged only after the destroyed object's stack has unwound. Another window closed during a loop is acknowledged right after the turn that destroyed it, so its `Done` does not wait for the mouse. The loop's own window can be destroyed from a turn inside its loop. The backend then expects Windows to end the loop without `WM_EXITSIZEMOVE`, stops modal delivery and acknowledges once `Pump` regains control.

Only size/move loops have this. A system menu, or other modal loops native code runs inside `DefWindowProc`, still defers callbacks until it ends, with wakes latched rather than lost. A press on a title bar may also block briefly before Windows announces a loop.

## Internal surfaces

`appcore.OpenContent`, `appcore.Invalidate` and the `Content` interface are the private bridge `ui` uses. `appcore.Inspect` reports frames, animating windows and deadline timers armed, and the backend counters add presents, exposure paints, paint failures and, on Win32, size/move loops, modal turns, timers and refusals. `appcore.ContentWindows`, `InspectContent` and the backend audition hooks (resize, iconify/restore, exposure, retained-image PNG snapshot) serve the [mixer example](../../examples/mixer/README.md)'s native scripts; none of them is public API. The root package imports none of the UI, renderer, text or font packages, which a transitive import test enforces.
