# dfx2

**Desktop apps in Go. Hold the C.**

dfx2 is a Go framework for desktop applications. It draws native user interfaces itself, with an immediate-mode API, and shows web interfaces in the system webview, all without cgo. It is the successor to [dfx](https://github.com/michaelquigley/dfx) (immediate-mode UI on Dear ImGui) and [dfw](https://github.com/michaelquigley/dfw) (native windows for Go HTTP apps), and builds on [df](https://github.com/michaelquigley/df).

## Status

The root module contains the portable application/window lifecycle core, a controllable fake backend, and Linux/amd64 GTK 3 and Windows/amd64 Win32 backends. Both backends have passed native scripts and manual auditions; the [final validation record](evidence/foundation/stage-4.md) retains commands, host details and limitations. The [lifetime example](examples/lifetime/README.md) opens independent blank windows with worker-updated titles and demonstrates both application modes. Builds use `CGO_ENABLED=0`; platform libraries initialize inside `Run`. Other targets return `ErrUnsupportedPlatform`. Built behavior is in [runtime.md](docs/current/runtime.md).

The `ui` package has begun: stage 1 of the [synthetic mixer](docs/future/synthetic-mixer.md) draws immediate-mode text and an animated meter on the CPU with pure-Go text and presents it through GTK on Linux; Windows presentation is the next stage. The [mixer example](examples/mixer/README.md) shows it, [ui.md](docs/current/ui.md) describes what is built and the [stage 1 record](evidence/synthetic-mixer/stage-1.md) holds its validation.

The broader framework remains in planning. The design work lives in [`docs/future/`](docs/future): start with [direction.md](docs/future/direction.md). Experiments live in [`experiments/`](experiments), each its own Go module.
