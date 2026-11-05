# UI content

Stage 1 of the [synthetic mixer](../future/synthetic-mixer.md) is built: windows whose content is an immediate-mode view drawn by the framework, with labels and keyed animated meters in one column, CPU rendering, pure-Go text and GTK presentation. Linux/amd64 presents it; Windows/amd64 refuses it explicitly until stage 2. Layout primitives, interactive controls and Win32 presentation are later stages. The [stage 1 record](../../evidence/synthetic-mixer/stage-1.md) holds the commands, native coverage and limits.

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

On Windows, `ui.Open` returns an error matching `errors.Is(err, ui.ErrUnsupportedContent)` before any native work; nothing is latched and ordinary `dfx2` windows keep working. The refused window still counts for last-window shutdown: while its creation was pending it was a live starting window, so if every other window closed meanwhile, a window application shuts down once the refusal returns. A resident application stays alive. Other unsupported targets fail earlier, in `Run`.

## Frames and scheduling

A frame is one evaluation of the view: the framework calls `draw` once, measures the record, renders it and presents the image. Layout, rewrapping and exposure never call `draw` again. Frames arise from `Invalidate`, a changed surface size or scale, and active animation. An exposed window whose image is still valid only repaints the retained image.

`Invalidate` is safe from any goroutine, never waits and never uses one of the window's 256 post slots. It advances a per-window generation under the runtime's producer mutex and shares the ordinary wake and wake-failure machinery; repeated calls coalesce and no goroutine is created. A frame clears only the generation it sampled at its start, so an invalidation racing a frame in progress causes a later frame. After the window starts closing, `Invalidate` returns `dfx2.ErrClosed`.

Each window has its own scheduling state: surface size, scale and visibility, a generation mark, a next animation deadline and a frame counter. A dispatch turn renders at most one frame per due window, in rotating window order, after the turn's bounded public callbacks; a frame requested while rendering waits for a later turn. A frame clears the window's animation request as it starts, and the view renews it only if another frame is needed; the next deadline is the frame's start plus 1/60 s of monotonic time. That is one-shot pacing, not display synchronization. Missed frames neither slow animation nor cause a catch-up burst, because animation reads elapsed time.

One app-owned one-shot timer on the runtime's supervisor serves the earliest live deadline and delivers through the private wake path; rendering stays on the UI thread. No live deadline means no armed timer, so an unchanged, settled window does no periodic work. A deadline the supervisor first sees already due is delivered by an immediate wake; an armed timer keeps its deadline even when the supervisor evaluates late. Timers stop before the dispatch target is released.

Windows reported hidden by the backend (unmapped or minimized), and zero-sized windows, render nothing and keep no armed deadline, while keeping their dirty mark. GTK/Wayland may not report minimization; its accepted limitation is described below. Becoming visible again renders once at the current monotonic time if the window is dirty or its animation deadline has passed. Compositor occlusion of a mapped window is not detected.

Closing seals content work at once: no further frame starts, a frame already rendering is not presented, the window's deadline stops counting toward the timer, and `Invalidate` is refused. After native teardown and its final callback, the content's caches, retained state and Go frame are released on the UI thread, outside the runtime's mutex. Until that release returns the window stays registered as closing: `Inspect` counts it and shutdown diagnostics report `content-release-pending=true`. Only then is it removed and `Done` closed.

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

## Internal surfaces

`appcore.OpenContent`, `appcore.Invalidate` and the `Content` interface are the private bridge `ui` uses. `appcore.Inspect` reports frames, animating windows and deadline timers armed, and the backend counters add presents and exposure paints. `appcore.ContentWindows`, `InspectContent` and the GTK audition hooks (resize, iconify/restore, retained-image PNG snapshot) serve the [mixer example](../../examples/mixer/README.md)'s native scripts; none of them is public API. The root package imports none of the UI, renderer, text or font packages, which a transitive import test enforces.
