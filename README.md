# dfx2

**Desktop apps in Go. Hold the C.**

dfx2 is a Go framework for desktop applications. It draws native user interfaces itself, with an immediate-mode API, and shows web interfaces in the system webview, all without cgo. It is the successor to [dfx](https://github.com/michaelquigley/dfx) (immediate-mode UI on Dear ImGui) and [dfw](https://github.com/michaelquigley/dfw) (native windows for Go HTTP apps), and builds on [df](https://github.com/michaelquigley/df).

## Status

The root module contains the portable application/window lifecycle core and a controllable fake backend (foundation runtime stage 1). Native backend selection currently returns `ErrUnsupportedPlatform` on every target. There is no GUI example yet. Built behavior and validation are in [runtime.md](docs/current/runtime.md).

The broader framework remains in planning. The design work lives in [`docs/future/`](docs/future): start with [direction.md](docs/future/direction.md). Experiments live in [`experiments/`](experiments), each its own Go module.
