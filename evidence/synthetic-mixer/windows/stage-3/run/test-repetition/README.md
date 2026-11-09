# Repeated unit tests on the Windows host, 2026-11-09

Revision `35dc8c4`, Go 1.27.3 windows/amd64, `CGO_ENABLED=0`. These runs characterize the two failures that stopped the stage 2 regression matrix at its repeated-test gate (`run/matrix/tests-repeated.log`). No test or runtime code was changed; see `../../results.md` for the reading.

| Log | Command | Result |
| --- | --- | --- |
| `overflow-200.log` | `go test ./internal/appcore -run TestOverflowDiscardsPendingInputReportsOnceAndRequestsAFrame -count=200` | 88 of 200 failed, every one at `input_test.go:136` with `InputQueued` between 55 and 252, `Frames:0`, `Consumed:0`, `InputOverflows:0` |
| `overflow-200-gomaxprocs1.log` | the same with `GOMAXPROCS=1` | 0 of 200 failed |
| `ack-200.log` | `go test ./internal/platform/win32 -run TestAcknowledgmentKeepsTheFailureNotYetReported -count=200` | 3 of 200 failed, each `core_amd64_test.go:214: 3 free attempts on one retry, want 2` |
| `appcore-50.log` | `go test ./internal/appcore -count=50` | only the overflow test failed, 19 of 50 |
| `win32-50.log` | `go test ./internal/platform/win32 -count=50` | only the acknowledgment test failed, 1 of 50 |
| `ui-50.log` | `go test ./ui -count=50` | ok |

The matrix's own gate run (`run/matrix/tests-repeated.log`) showed the same two tests: 13 of 50 and 1 of 50.
