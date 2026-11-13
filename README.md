# dfx2

**Desktop apps in Go. Hold the C.**

dfx2 is a Go framework for desktop applications. It draws native user interfaces itself, with an immediate-mode API, and shows web interfaces in the system webview, all without cgo. It is the successor to [dfx](https://github.com/michaelquigley/dfx) (immediate-mode UI on Dear ImGui) and [dfw](https://github.com/michaelquigley/dfw) (native windows for Go HTTP apps), and builds on [df](https://github.com/michaelquigley/df).

## Status

The root module contains the portable application/window lifecycle core, a controllable fake backend, and Linux/amd64 GTK 3 and Windows/amd64 Win32 backends. Both backends have passed native scripts and manual auditions; the [final validation record](evidence/foundation/stage-4.md) retains commands, host details and limitations. The [lifetime example](examples/lifetime/README.md) opens independent blank windows with worker-updated titles and demonstrates both application modes. Builds use `CGO_ENABLED=0`; platform libraries initialize inside `Run`. Other targets return `ErrUnsupportedPlatform`. Built behavior is in [runtime.md](docs/current/runtime.md).

The `ui` package draws immediate-mode text, measured rows and columns, animated meters, faders and checkboxes on the CPU with pure-Go text. Linux presents through GTK/Cairo; Windows presents through GDI and continues bounded dispatch during size/move loops. The accepted [synthetic mixer](examples/mixer/README.md) demonstrates two windows over an application-owned model with ordered commands and pending-value reconciliation. Both hosts passed automated and manual auditions; the two later input-deferral fixes have portable-test and cross-build coverage. Real Windows DPI transitions remain untested, and Windows CPU timing is unavailable as useful evidence. [ui.md](docs/current/ui.md) describes built behavior; the [stage 4 record](evidence/synthetic-mixer/stage-4.md) carries acceptance, validation revisions and remaining limits.

The `webview` package's portable contracts are built: explicit in-process, owned loopback and attached-URL modes, browser ownership in the shared lifetime, the finite in-process HTTP adapter and the owned loopback server ([webview.md](docs/current/webview.md)). No platform hosts a browser yet; the Linux and Windows browser hosts are the next stages. The design work lives in [`docs/future/`](docs/future): start with [direction.md](docs/future/direction.md). Experiments live in [`experiments/`](experiments), each its own Go module.


## Build and run

With Go and the platform runtime libraries installed:

```sh
make
mixer -controls
```

`make` installs the root module's examples into `GOBIN` (or `GOPATH/bin` when unset) with `CGO_ENABLED=0`. Put that directory on `PATH`. `make test` runs tests and vet; `make clean` cleans the root packages and removes the installed `mixer` and `lifetime` examples. See the [mixer README](examples/mixer/README.md) for platform prerequisites and audition controls.
