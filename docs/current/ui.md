# UI content

Stages 1 to 3 of the [synthetic mixer](../future/synthetic-mixer.md) are built: windows whose content is an immediate-mode view drawn by the framework, with measured rows and columns, labels, keyed animated meters, faders and checkboxes, CPU rendering and pure-Go text. Linux/amd64 presents it through GTK and has passed native auditions, including real pointer and keyboard input. Windows/amd64 presents it through GDI and passed [native automated and manual auditions](../../evidence/synthetic-mixer/windows/results.md) of stage 2's presentation on Windows 10 over Remote Desktop, including progress during size/move loops, large images, lock and disconnect/reconnect. Windows input and controls passed the [stage 3 native matrix](../../evidence/synthetic-mixer/windows/stage-3/results.md), 10/10, and Michael reported no problems in the testable manual steps. After correcting two test-barrier defects, the [return audition](../../evidence/synthetic-mixer/windows/stage-3/return-results.md) passed the original regression runners with all test gates enabled: 17 mixer and 9 foundation cases. Michael also confirmed the Linux controls manual audition looked fine. The [paragraph-key return](../../evidence/synthetic-mixer/windows/stage-3/paragraph-key-results.md) validates the shared text-cache correction with the original gates and controls matrix passing. Stage 3 was accepted at `27e0c4e`. Stage 4 builds the [synthetic mixer](#the-synthetic-mixer) on this surface; it adds no public API. Its Linux automated audition has passed, and Michael reported that the Linux hands-on checklist worked well. Windows automated and manual validation also passed; its CPU timing figures are unusable on the tested host, and final stage acceptance remains pending. Actual Windows DPI transitions remain untested; native cleanup-failure handling is exercised with injected calls only. The [stage 1](../../evidence/synthetic-mixer/stage-1.md), [stage 2](../../evidence/synthetic-mixer/stage-2.md), [stage 3](../../evidence/synthetic-mixer/stage-3.md) and [stage 4](../../evidence/synthetic-mixer/stage-4.md) records hold the commands, native coverage and limits.

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

A view with controls nests containers and scopes around them:

```go
func (m *Mixer) Draw(f *ui.Frame) {
	f.Row(ui.Layout{Gap: 12}, func(f *ui.Frame) {
		for _, ch := range m.Snapshot() {
			f.Scope(ch.ID, func(f *ui.Frame) {
				f.Column(ui.Layout{Width: 120, Gap: 8}, func(f *ui.Frame) {
					f.Label(ch.Name)
					f.Meter("level", ch.Level)
					if gain, changed := f.Fader("gain", ch.Gain); changed {
						m.SubmitGain(ch.ID, gain)
					}
					if muted, changed := f.Checkbox("mute", "Mute", ch.Muted); changed {
						m.SubmitMute(ch.ID, muted)
					}
				})
			})
		}
	})
}
```

The application owns its domain values and composes the view with ordinary Go functions; there is no component base type or framework state container. A `*ui.Frame` is valid only during the draw call.

- `Row(layout, body)` and `Column(layout, body)` place their body's widgets along one axis.
- `Scope(key, body)` names its body's widgets and adds no layout. A widget's identity is the path of its enclosing scope keys and its own, so a key containing `/` cannot pass for a nested path. Sibling scopes and widgets share one key space, and rows and columns are transparent to it.
- `Label(text)` records stateless text.
- `Meter(key, level)` records a display-only meter; it reports no changes.
- `Fader(key, value)` and `Checkbox(key, label, value)` return the value to use from now on and whether the user changed it.

Programmer mistakes panic: a duplicate or empty key, a value outside [0, 1] or NaN, a negative or non-finite `Layout` value, a minimum above its maximum, a nil body, and use of a frame after its draw call returned. The panic becomes an application failure with its stack and the window/frame in the error, and the runtime's cancel-and-drain path follows. It never unwinds through a native callback.

The meter's displayed peak attacks immediately and falls linearly at 0.5 units per second of monotonic time; it requests animation only while the peak is above the latest level. Removing a key from the view discards its state; reintroducing the key starts fresh.

Linux and Windows both host content, and other targets fail earlier, in `Run`. The core still honors a backend that refuses content before native work: `ui.Open` returns an error matching `errors.Is(err, ui.ErrUnsupportedContent)`, nothing is latched and ordinary `dfx2` windows keep working. The refused window still counts for last-window shutdown: while its creation was pending it was a live starting window, so if every other window closed meanwhile, a window application shuts down once the refusal returns. A resident application stays alive. No shipping backend refuses today; the path is exercised with the fake backend.

## The synthetic mixer

[`examples/mixer -controls`](../../examples/mixer/README.md) is the application this surface was built for: six channels in two windows over one model, a 30 Hz level producer and an ordered command worker standing in for a device. The framework supplies frames, layout, widgets and invalidation; everything about the model, devices and acknowledgments is the example's own code. Its shape, reduced:

```go
// Application state: a mutex and an owned snapshot, replaced on every change.
func (m *model) publish(change func()) {
	m.mu.Lock()
	change()
	m.current = m.copyLocked() // never mutated after this
	windows := slices.Collect(maps.Keys(m.subscribers))
	m.mu.Unlock()
	for _, w := range windows {
		_ = w.Invalidate() // dfx2.ErrClosed from a window closing meanwhile is expected
	}
}

func (m *mixer) view(window string) func(*ui.Frame) {
	return func(f *ui.Frame) {
		s := m.model.Snapshot()
		f.Row(ui.Layout{Gap: 12}, func(f *ui.Frame) {
			for _, ch := range s.Channels {
				f.Scope(ch.ID, func(f *ui.Frame) {
					f.Column(ui.Layout{Width: 110, Gap: 8}, func(f *ui.Frame) {
						// Show the pending edit while its command is outstanding.
						if gain, changed := f.Fader("gain", ch.ShownGain()); changed {
							m.commands.Submit(Command{Channel: ch.ID, Control: "gain", Gain: gain}) // never waits
						}
						if muted, changed := f.Checkbox("mute", "Mute", ch.ShownMute()); changed {
							m.commands.Submit(Command{Channel: ch.ID, Control: "mute", Mute: muted})
						}
						f.Meter("level", ch.Level)
						f.Label(ch.Name)
					})
				})
			}
		})
		f.Label(s.LastError) // refusals and device rejections are visible
	}
}

// In setup: the producer and the worker belong to the application, not a window.
app.Go("sampler", m.model.run)
app.Go("commands", m.commands.run)
```

What the example shows about using `ui`:

- **Views only read.** A view takes one snapshot and submits only when a control reports `changed`. External changes, acknowledgments and samples arrive as new snapshots and never become commands.
- **Producers invalidate; they never draw.** Publication copies state under the application's own lock and invalidates outside it, so a window closing concurrently costs one refused `Invalidate`. Invalidation never uses a post slot, so a 30 Hz producer cannot crowd out posted callbacks.
- **Pending values belong to the application.** The widgets start keyboard and toggle edits from the value last shown, but across frames they show what the view supplies. Supplying the pending value until its command resolves is what keeps a delayed device from reverting a fader, or making the next Up start from a stale value. The example clears a pending value only on its own command's resolution, and returns to the device's value when the device rejects the latest edit, showing the error.
- **Submission never blocks the UI thread.** The example's queue holds 64 commands and refuses the rest at once, visibly. Order is preserved and nothing is merged.
- **Shutdown is the runtime's.** Application tasks are canceled when the last window closes or the app quits; the worker cancels its held and queued commands and reports them as never applied.

The [stage 4 record](../../evidence/synthetic-mixer/stage-4.md) holds the deterministic fixtures and native auditions of these behaviors.

## Layout

The view's root is a column filling the window, with 16 logical units of padding and 10 between its children. `Layout` holds logical units: `Width` and `Height` (zero is intrinsic), `Padding`, `Gap`, `MinWidth`, `MinHeight`, `MaxWidth`, `MaxHeight` (zero maximum is unbounded) and a `Flex` weight. Physical pixels are the units rounded at the surface's scale.

- **Measuring.** Each node is solved exactly once per frame, bottom-up, with the room on offer passed down. Nothing replays the view.
- **Sizes.** Nonflex children keep what they request or measure, along both axes; nothing stretches implicitly.
- **Flex.** Flex children share the main-axis space left after their siblings and gaps, by weight and within their own bounds. A share outside a child's bounds freezes it at the bound and the rest is shared again. Only proportions matter, for any finite weights: each pass scales the weights still sharing by their largest, so `1e308` alone lays out like `1`, and two of them split evenly. Rounding loses no pixel. If the arithmetic still came out non-finite or stopped converging, the frame would fail with an error rather than loop or publish a bogus length. Validated layouts cannot reach that, and only a direct test exercises it. Flex takes precedence over a child's own main-axis `Width` or `Height`. A container with a flex child fills the room offered along its main axis; one offered no bound gives flex children their intrinsic size.
- **Overflow.** If minimum sizes exceed the space available, the minima hold. Every container and widget clips to its own rectangle, and what a clip hides cannot be pressed.

Leaf sizes are intrinsic:

| Widget | Logical size |
| --- | --- |
| `Label` | Wraps to the width on offer and takes the wrapped text's size. An unbreakable word is clipped to that width. In a row, nothing bounds the width, so it takes its natural one. |
| `Meter` | As wide as the room offered, or 120 inside a row, and 14 tall. Keeping the column's full width is what stage 1's placement did. |
| `Fader` | 36 wide and 160 tall, with a 12-tall thumb. It has no flexible height. |
| `Checkbox` | A 16 box with 3 of inset for the focus ring and 6 before its wrapped label. |

## Controls and input

The application's values stay the application's. Each window keeps only interaction state, under full scope paths: per-control base values and edits, the captured control, the focused control and the last completed layout's hit rectangles. A frame processes queued input against that last completed layout, calls the view once, solves layout and renders. A new control becomes interactive after its first completed layout. Each window has its own focus and capture, so neither crosses to another window over the same model.

**Reporting edits.** `changed` is true only for user input: a value the application supplies never reports a change, including during a drag. The output is what the view should use, so an edited value renders even when the caller supplies an older snapshot. The next frame without an edit shows the supplied value again, unless a drag holds it. Applications that need a pending value across a delayed acknowledgment keep it themselves. Keyboard and activation edits start from the value last shown, never from a stale snapshot.

**One edit per evaluation.** The value/changed API can report one edit per widget per view evaluation. A frame processes queued input up to, and not including, the event that would make a second edit to one control. That event and the rest wait for the next frame, which is scheduled at once. So two activations before a frame are two evaluations and two reported edits, in order. A consumed event is never replayed. A checkbox release already validated inside its target keeps that validation while deferred, so moving the same control in the intervening layout does not lose the activation. Gesture cancellation clears that validation. A deferred fader press retains its resolved target and value; a newer layout cannot redirect or remap that press. Removing its control discards the deferred press, and overflow clears it with the lost input. An edit whose target left the view in that frame is never reported.

**Fader.** A left press takes capture and focus, and the thumb's center follows the pointer: high at the top, clamped to [0, 1]. Pressing away from the thumb jumps to that position, which counts as an edit. During the drag the gesture owns the displayed value despite external updates. A release reports a change only if its value differs from the last one reported; after it, frames follow the supplied value. Arrow keys step by 0.01 (Up and Right raise), Page Up and Page Down by 0.1, and Home and End select 0 and 1. Keyboard steps stay on a decimal grid and are ignored during a drag.

**Checkbox.** A left press shows it pressed; a release inside its visible rectangle toggles it, and a release outside does nothing. Pressing Space with it focused shows it pressed, and releasing Space toggles it. A press never reports an edit.

**Focus.** A press on a control focuses it. Pressing empty space leaves focus alone. Tab and Shift+Tab move through the faders and checkboxes in recorded order, wrapping, starting from the first or last if nothing is focused. A focus ring shows the control, dimmer while the window lacks native focus. Native focus loss keeps the control focused for when the window returns.

**Cancellation.** Native focus loss, capture loss, Tab, a queue overflow or the control's removal clears pressed state and any gesture, with no final edit. Edits already returned remain the application's. A removed control's native capture is released; reintroducing its key starts fresh, and removing the focused key clears focus. Only the left button operates controls.

**The queue.** Native callbacks only copy normalized events: logical coordinates, button, key, modifiers and ordered edges. Each window queues up to 256 under the runtime's mutex. Adjacent pointer motion coalesces, never across an edge, and never into an event a frame in progress already holds. Overflow discards pending input, cancels any gesture at the next frame, requests that frame, and logs one diagnostic through `Logf` from the supervisor. A hidden window keeps its queue until it is visible again. Closing drops queued input and refuses more.

Backends report motion only while the left button is held, and only the keys widgets use. Hovering and other typing therefore request no frames, and other keys keep their native meaning. A settled window stays idle under them.

## Frames and scheduling

A frame is one evaluation of the view: the framework calls `draw` once, measures the record, renders it and presents the image. Layout, rewrapping and exposure never call `draw` again. Frames arise from input, `Invalidate`, a changed surface size or scale, and active animation. An exposed window whose image is still valid only repaints the retained image.

`Invalidate` is safe from any goroutine, never waits and never uses one of the window's 256 post slots. It advances a per-window generation under the runtime's producer mutex and shares the ordinary wake and wake-failure machinery; repeated calls coalesce and no goroutine is created. A frame clears only the generation it sampled at its start, so an invalidation racing a frame in progress causes a later frame. After the window starts closing, `Invalidate` returns `dfx2.ErrClosed`.

Each window has its own scheduling state: surface size, scale and visibility, a generation mark, a next animation deadline and a frame counter. A dispatch turn renders at most one frame per due window, in rotating window order, after the turn's bounded public callbacks; a frame requested while rendering waits for a later turn. A frame clears the window's animation request as it starts, and the view renews it only if another frame is needed; the next deadline is the frame's start plus 1/60 s of monotonic time. That is one-shot pacing, not display synchronization. Missed frames neither slow animation nor cause a catch-up burst, because animation reads elapsed time.

One app-owned one-shot timer on the runtime's supervisor serves the earliest live deadline and delivers through the private wake path; rendering stays on the UI thread. No live deadline means no armed timer, so an unchanged, settled window does no periodic work. A deadline the supervisor first sees already due is delivered by an immediate wake; an armed timer keeps its deadline even when the supervisor evaluates late. Timers stop before the dispatch target is released.

Windows reported hidden by the backend (unmapped or minimized), and zero-sized windows, render nothing and keep no armed deadline, while keeping their dirty mark. Eligibility is checked again as each selected frame starts, so a window that becomes hidden or zero-sized after the turn selected it, for example through a synchronous surface change during an earlier window's frame, runs no view and presents nothing; its queued input and invalidation wait for restoration. No shipping backend is known to report such a change synchronously; the fake backend does. GTK/Wayland may not report minimization; its accepted limitation is described below. Becoming visible again renders once at the current monotonic time if the window is dirty or its animation deadline has passed. Compositor occlusion of a mapped window is not detected.

Closing seals content work at once: no further frame starts, a frame already rendering is not presented, the window's deadline stops counting toward the timer, and `Invalidate` is refused. After native teardown and its final callback, the content's caches, retained state and Go frame are released on the UI thread, outside the runtime's mutex. Until that release returns the window stays registered as closing: `Inspect` counts it and shutdown diagnostics report `content-release-pending=true`. Only then is it removed and `Done` closed.

Only one dispatch turn runs at a time, whichever native path delivered it, and setup counts as one. The core refuses a wake that arrives during a turn: no application callback, frame or view evaluation ever nests inside another. That wake is spent, so the running turn's closing check requests another if work remains. Backends need no knowledge of the refusal. On GTK it does not arise in practice; on Win32 it backs the size/move guards described below.

A view panic, a rendering error (for example an image over the size limit), a text-system error, a presentation failure or a failed capture release enters the runtime's fatal cancel-and-drain path with the window ID and frame number in the error. A panic in content release is a cleanup failure: `Run` returns it, with its stack, alongside any earlier failure.

## Rendering

Each window renders into one reusable, opaque, premultiplied BGRA frame on the CPU: clipped fills, antialiased lines, glyph coverage masks and premultiplied color glyphs. Logical geometry multiplies by the surface scale into physical pixels. Image dimensions use checked arithmetic and a 256 MiB limit per image; exceeding it is a reported rendering failure, and a failed resize keeps the previous frame. Storage is reused unless it is more than four times the new size.

Presentation hands the complete frame to the backend, which copies it synchronously into storage the backend owns; native code never retains the Go slice. That copy keeps ownership simple for the first renderer and has not been measured as a bottleneck.

## Text

Text runs through a private engine seam. The single engine, go-text, finds faces with per-rune fallback (preferred families, then the generic emoji family), shapes with its HarfBuzz port, resolves bidi levels and UAX #14 break opportunities, and rasterizes outlines with slight vertical hinting: zones and the x-height overshoot snap to whole pixels, while advances stay fractional. Pens are positioned to the nearest quarter pixel and baselines to whole physical pixels. PNG color glyphs (Noto Color Emoji's strikes) are scaled. SVG glyphs use their fallback outline when one is present. Formats the engine cannot draw, including SVG without a fallback outline, COLR paint and other bitmap encodings, render as a visible hollow box rather than blank space. Spaces inside fallback-font runs take the preferred face, so a space after an emoji keeps normal width.

Shaped data crossing the seam is engine-neutral and owned: font and glyph IDs, glyphs in visual order with cluster, advance and offsets, runs with bidi level and line metrics, and break classes. Wrapping and line assembly are shared code over that data. A paragraph is shaped once per text and size; a new width rewraps the same shaped runs without reshaping and without mutating them. Lines reorder runs by the UAX #9 L2 rule from their levels; trailing whitespace keeps its glyphs but no advance. Paragraph direction is always left to right; there is no ellipsis, justification, caret or selection.

Each window owns bounded caches: at most 16 MiB of glyph pixels, and 1 MiB of paragraph payload in at most 256 entries, including shaped data, wraps and owned UTF-8 cache keys. A key is copied only when its paragraph is admitted, so a substring cannot retain its caller’s larger backing allocation. Admission reserves the key bytes once, and rewrapping preserves that charge; transient paragraphs retain no UTF-8 key. Glyph rasters are keyed by font, glyph, physical size (scale included) and quarter-pixel position; mask and color glyphs are stored separately and share the pixel budget. Entries are evicted only between frames, least recently used first, to restore the budgets and then to make room for entries the last frame could not admit. Every reference handed out during a frame stays valid until the frame ends, and evicted storage is dropped, never recycled. An entry that does not fit is used transiently for that frame only. Because entries the last frame used are never evicted, a window whose steady working set exceeds a budget uses the excess transiently every frame, rasterizing or shaping it again each time; transient scratch is bounded by the image limit and released when the frame ends. Wrapping is charged before it is kept, because a narrower width can more than double a paragraph's payload. A cached paragraph stores a new wrap only if the paragraph budget still holds; otherwise the new lines are lent as scratch for that frame, the entry keeps its previous wrap, and the shortfall is made up by evicting unused entries between frames, so a later frame stores the wrap without reshaping. Replacing a cached wrap already returned in this frame moves its old buffer into scratch accounting until frame end. An uncached paragraph charges every new wrap in full to scratch, retaining the charge for its earlier wraps. Reusing the stored wrap at the same width adds no charge. Exceeding the scratch limit is refused with an error, which fails the frame, and nothing is stored.

The frame's draw commands drop their borrowed text lines before scratch accounting resets, including on rendering errors and panics. The scene retained for internal inspection contains layout and command metadata, without text buffers.

Font discovery is separate from those budgets. The system font index is built on the first UI frame, cached on disk under the user cache directory (`dfx2/fontscan`), and kept with its loaded faces for the life of the process, like the native toolkit libraries. Windows hold only font IDs; closing a window releases its caches. The preferred family list defaults to `sans-serif`.

## Linux presentation

A content window adds a GTK drawing area to its toplevel before the window is shown, with the area referenced by the backend until the window's final release. Permanent shared callbacks, routed by numeric token, handle the area's `draw`, `size-allocate`, `notify::scale-factor`, `map` and `unmap` signals and the toplevel's `window-state-event`. Surface handlers copy the allocation, GTK's integer scale factor and visibility (mapped and not withdrawn or iconified) into a `SurfaceChanged` event only when one of them changes. The draw handler only paints the retained image; it never asks the core for a frame. After setting the source and painting, it checks `cairo_status` on the drawing context. A non-success status increments `PaintFailures` and retains the first pending error with the window ID, retained image size and status code. `Pump` reports it through the application failure path before blocking again; no further application dispatch is admitted while the error awaits delivery. A native wake deferred for that reason remains pending and is delivered on the next `Pump` call, so shutdown can safely coalesce into it. The core cancels and drains normally, and `Run` returns the failure. The Cairo error path has injected-call coverage; the native auditions exercise successful painting.

Presentation copies the frame into a Cairo-owned ARGB32 image surface: flush, row-by-row copy honouring Cairo's stride, mark dirty, set the device scale, queue a draw. Same-sized frames reuse the surface. A size change creates, checks and fills the replacement before destroying the previous surface, so a Cairo failure keeps the last valid image; an error surface is destroyed too. Content and drawing signals disconnect before teardown, and the retained surface is destroyed with the backend's final references after the destroy signal stack returns, before destruction is acknowledged.

GTK 3 learns of minimization only when the window system reports it. Under X11 (including XWayland) iconified windows suspend rendering and deadlines. In the GNOME Wayland session tested, a minimized window stops painting but GTK reports no iconified state, so frames and animation deadlines continue while it is minimized. The scale is GTK's integer factor; fractional desktop scaling is applied by the compositor afterward.

The drawing area is the one focusable widget, grabbing focus before the window is shown and again on each press. Input signals read their events through GDK's accessors rather than struct offsets, through permanent shared callbacks routed by token. Keys widgets use stop at the handler, which keeps Tab from GTK's own focus chain. A double-click's extra press events are ignored, since each press also arrives plainly. GTK's [implicit grab](https://docs.gtk.org/gtk3/input-handling.html) keeps a drag routed to the area outside the window. It has no revocation call, so `ReleaseCapture` does nothing here: a canceled gesture ignores the remaining pointer events, and the grab ends at the button-up edge. `grab-broken-event` and a shadowing `grab-notify` report capture loss; focus-out reports focus loss.

## Windows presentation

A content window has no child surface: the framework draws its client area directly. Surface events come from `WM_SIZE` and `WM_SHOWWINDOW` and from `WM_DPICHANGED`, whose scale is the window's DPI over 96, carrying the full client rectangle in physical pixels, only when a value changes. `SIZE_MINIMIZED` clears visibility and keeps the last client size, because Windows reports 0×0 while minimized and restoring an unchanged window should not read as a resize. Closing seals these reports.

Presentation copies the frame row by row into one `VirtualAlloc` allocation per window, then calls `InvalidateRect`, which only marks the window. Storage carries 25% headroom up to the 256 MiB limit, so growing by dragging rarely reallocates, and it is reused unless more than four times too large. A replacement is allocated and filled before the old allocation is freed, so a failed allocation keeps the last image and enters the fatal path. Native storage is a workaround the [text spike](../future/text.md) found for black frames: GDI set no lines for Go heap frames straddling a 4 MiB boundary. That allocation-boundary explanation remains an inference from one host.

`WM_PAINT` runs `BeginPaint`, `SetDIBitsToDevice` with a top-down 32-bit `BI_RGB` header, and `EndPaint`. It never asks the core for a frame. `WM_ERASEBKGND` fills only what the retained image will not cover, in black, so a grown window shows black rather than the class's white until its next frame. A failed `BeginPaint`, a failed fill, or a blit that sets no lines fails the application: the step counts in `Counters.PaintFailures` and its error reaches `Run` through `Pump`, with `EndPaint` still balancing a successful `BeginPaint` and `DefWindowProc` validating after a failed one. There is no exception for lock screens or disconnected sessions. On the tested Windows 10 RDP host, paints continued through both transitions with zero failures; this does not establish behavior on every host or at a changed reconnect resolution. Retained storage is freed after `WM_NCDESTROY`'s stack has returned, immediately before the destruction acknowledgment. A failed free keeps the allocation owned by its window: the first failure fails the application, `Counters.Problem` carries it to the shutdown diagnostics, and a one-shot 10 ms timer on the dispatch window retries it. The window's `Destroyed` waits until every allocation is released, so a persistent failure keeps `Done` and `Run` pending, as other stuck native cleanup does. `Destroyed` also carries each allocation's first error, so `Run` returns it even if an earlier failure took first place and the retry later succeeded. Ordinary windows keep the default paint and erase handling.

Windows has no implicit grab, so a left press on a content window takes `SetCapture` and the matching release gives it back. `WM_CAPTURECHANGED` reports a loss only when something else took it, such as a size/move loop or another window's press; the backend's own release clears its flag first. `WM_KILLFOCUS` releases capture at once, because no frame may run until the window is visible again. Client coordinates are signed, because a captured pointer can be left of or above the window, and divide by DPI over 96 into logical units. Taking and releasing capture count as native operations for the size/move guards below. `ReleaseCapture` cannot break a size/move loop's capture, because the loop's taking it already cleared the backend's flag. Keys and modifiers come from `WM_KEYDOWN`, `WM_KEYUP` and `GetKeyState`; unused keys reach `DefWindowProc` unchanged, and system keys are not intercepted. The [native Windows controls audition](../../evidence/synthetic-mixer/windows/stage-3/results.md) exercised capture across window edges, signed coordinates, button-up release, navigation keys and focus loss during a drag. `ReleaseCapture` inside a real size/move loop remains modeled only; native capture loss to a menu or another process was not provoked. Native DPI transitions also remain untested. Ordinary windows deliver no input events.

Desktop composition retains window contents, so occluding and uncovering a window normally sends no `WM_PAINT`. Exposure still repaints correctly when it does arrive; the audition forces one with `InvalidateRect`.

## Dispatch during Windows size and move loops

Dragging a title bar or border runs a modal loop inside `DefWindowProc`, so control does not return to `Pump`. During that loop every window keeps receiving bounded dispatch turns, so frames and posted callbacks keep advancing:

- `WM_ENTERSIZEMOVE` and `WM_EXITSIZEMOVE` track the loop's window. `WM_APP` still only latches readiness; inside a loop it additionally schedules delivery. It never runs callbacks from its own handler.
- Delivery is a one-shot timer on the application's message-only dispatch window, with a 10 ms delay (`USER_TIMER_MINIMUM`) that yields to the loop's own input and paint. Its handler kills it on entry, then delivers one ordinary core turn: at most 64 callbacks and one frame per window. A turn leaving work behind requests a wake, whose message arms the next one-shot; animation deadlines arrive the same way, from the supervisor's timer. So modal frame pacing is the deadline plus this delay, and animation stays correct because it reads elapsed time.
- With no latched wake nothing is armed, so holding a drag still on a settled application runs no timer at all.
- A turn runs only with the loop established, no other turn active and no unfinished native operation. `Create`, `Destroy`, `SetTitle`, `ShowWindow` and DPI repositioning count as native operations. A refused delivery keeps the wake latched; the end of that turn or operation re-arms. Leaving the loop kills the timer and hands any latched wake to `Pump`, which delivers it when the message's stack returns. A latched native failure also holds delivery, so the failure reaches appcore through `Pump` before more callbacks start. A failed `SetTimer` keeps the wake and reports through `Pump` the same way.
- Timer IDs are never reused, so a late `WM_TIMER`, which `KillTimer` does not remove from the queue, cannot pass for the live timer. Release stops any armed timer before destroying the dispatch target.
- Destruction is acknowledged only after the destroyed object's stack has unwound. Another window closed during a loop is acknowledged right after the turn that destroyed it, so its `Done` does not wait for the mouse. The loop's own window can be destroyed from a turn inside its loop. The backend stops modal delivery and acknowledges once `Pump` regains control, tolerating an absent `WM_EXITSIZEMOVE`. Native validation confirmed that destroying the dragged window ends the loop and drains; it did not log the exact exit-message sequence.

Only size/move loops have this. A system menu, or other modal loops native code runs inside `DefWindowProc`, still defers callbacks until it ends, with wakes latched rather than lost. A press on a title bar may also block briefly before Windows announces a loop; the manual audition observed a pause under one second. Minor flicker while resizing and Alt+F4 being ignored during a drag were also reported.

Cleanup retries have a failure-only fallback when `SetTimer` fails: `Pump` pauses 10 ms, retries pending frees and services a message without blocking. This limits message servicing to roughly 100 messages per second, cannot retry while a native size/move loop holds `Pump`, and skips a retry on a `Pump` call that delivers a wake. Ownership and diagnostics remain live. These limits are documented; this fallback was not exercised by native Windows validation.

## Internal surfaces

`appcore.OpenContent`, `appcore.Invalidate` and the `Content` interface are the private bridge `ui` uses. `appcore.Inspect` reports frames, animating windows and deadline timers armed, `InspectContent` adds queued input and overflows, and the backend counters add presents, exposure paints, paint failures and, on Win32, size/move loops, modal turns, timers and refusals. `appcore.ContentWindows`, `InspectContent` and the backend audition hooks (resize, iconify/restore, exposure, retained-image PNG snapshot) serve the [mixer example](../../examples/mixer/README.md)'s native scripts. `appcore.AuditionContent` returns a live window's content on the UI thread, refusing closing windows, so the example can read each window's text cache counters through the content's `TextStats` method. The example may also set `text.DefaultBudget`, which each window copies as it opens, and its fixtures install pinned fonts with `gotext.Preset` before the shared engine first loads. None of them is public API. The root package imports none of the UI, renderer, text or font packages, which a transitive import test enforces.
