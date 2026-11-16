# Request-body threading probe (Windows, 2026-11-16)

A narrow feasibility check for the [stage 3 return finding](../../../../../evidence/webview-integration/stage-3.md#request-body-threading-investigation--native-capability-check-needed): the production Windows host reads an intercepted request's body `IStream` synchronously on the UI thread. This probe establishes what the incoming stream is and whether it can be read elsewhere, with real browser bodies and with a modeled stream that does not return promptly. No production code, public API or canon changed; `CGO_ENABLED=0` throughout.

**Result.** On WebView2 Runtime 151.0.4129.93, every incoming request body stream (43 observed, across string, JSON, URL-encoded form, multi-part Blob and multipart/file POSTs at 64 KiB and 900 KB) advertises `IAgileObject`, carries the free-threaded marshaler, reports its full length through `Stat` before any read, and reads byte-exactly on a separate thread initialized with `CoInitializeEx(COINIT_MULTITHREADED)`, including while the UI thread is blocked without pumping. A `ReadableStream` request body arrives with **no content stream at all**. The modeled held read shows the UI thread, another window and the host's commands progressing while a worker is inside `Read`, and a close that settles native interception once and releases the window only after the read returns. A real close landed during a worker read once in four tries; the read completed after the close and everything settled once.

## Host and binaries

| Item | Value |
| --- | --- |
| Windows | 10.0.19045.6466, amd64, Remote Desktop session `Active`, 3840×1600 at 96 DPI |
| WebView2 Runtime | Evergreen 151.0.4129.93; SDK 1.0.4129.50, loader `a9a09232…` beside the executable |
| Go | go1.27.3 windows/amd64 |
| Revision | `fdb640b` plus this experiment's uncommitted changes (`run/environment.json`) |
| Host | `dfx2-webview2-spike.exe` SHA-256 `620fc16ed4a4d2dbae035a11f5e3ae3d52d4cd12d79716c832f3b8408efcb371` |
| Driver | `body-drive.exe` SHA-256 `408246c32f91cdc690cab9132d6ea6810410d64c28114336644ef6b537d0ca27` |

## What was added

- `bodyprobe_windows.go`, enabled by `-feasibility -feas-body inspect|worker`. On the UI thread, inside `WebResourceRequested`, each incoming body stream is queried for `IAgileObject`, `IMarshal` (and its unmarshal class for `MSHCTX_INPROC`), `ISequentialStream`, `Stat(STATFLAG_NONAME)` and `Seek(0, CUR)`; none of these consume or reposition it (`body-stream` event). `inspect` then reads on the UI thread as production does; `worker` takes the deferral, keeps the event arguments on the UI thread, and hands an agile stream as is to a goroutine locked to its own OS thread, `CoInitializeEx(COINIT_MULTITHREADED)`, which reads 64 KiB chunks up to the adapter's limit, releases the stream, uninitializes, and posts the result back (`body-worker`, `body-read`). A stream without the marker would go through `CoMarshalInterThreadInterfaceInStream` instead (never taken here). `?stall=MS` on the request makes the UI thread sleep that long, without pumping, right after the worker starts, and records whether the read finished meanwhile (`ui-stall`).
- A host-implemented agile read-only `IStream` whose `Read` blocks on a gate (`gate-read LABEL BYTES`, `gate-release`), read by the same worker path; its `Read` records the thread it runs on and its final release is counted. `ping` reports UI liveness; a 10 ms UI timer counts ticks while reads are in flight.
- Close: `closeReads` settles native interception for each read in flight (503, deferral completed, once) and keeps the read; `maybeRelease` waits for every read to return (`release-waiting-reads`), then the late result is dropped (`body-read-after-close`) and release proceeds.
- The probe page's `?run=body&kind=…&size=…&stall=…` builds the body and its own SHA-256 (for multipart, of the file part) and POSTs to the new `/api/body` echo, which digests the body and each multipart part.
- The driver `drive/` (Windows only; a stub elsewhere so `go vet ./...` covers it).

The unflagged feasibility host is unchanged: with `-feas-body` empty it reads on the UI thread exactly as before.

## Commands

From `experiments\webview2-spike`, with the loader set up:

```powershell
$env:CGO_ENABLED = "0"
go test ./...; go vet ./...
$env:WEBVIEW2_IDL = "$PWD\.sdk\1.0.4129.50\WebView2.idl"; go test -run IDL .
$env:GOOS = "linux"; go vet ./...; Remove-Item Env:GOOS
go build -o dfx2-webview2-spike.exe .
go build -o body-drive.exe .\evidence\windows\body-probe\drive
.\body-drive.exe -exe .\dfx2-webview2-spike.exe -out evidence\windows\body-probe\run
```

All checks passed. `body-drive.exe` takes `-only name,name`, `-root DIR` and `-step-timeout`. Use a new `-out` for every run. Two earlier runs of this matrix were driver development (the page's fields read from the wrong level of the report, a wrong expected echo length, and the `ReadableStream` case judged before its outcome was known) and were replaced; the host did not change between them and the final run except by `gofmt`, after which it was rebuilt and the matrix rerun so the tree rebuilds the recorded host.

## Scenarios

`run/<scenario>/result.json` has every check and its detail, `host.jsonl` the host's events, `host.stderr` its log; `run/summary.json` collects them. Exit criteria for every scenario: host exit 0, no browser process left, no callback object or gate stream alive, no worker alive, nothing alarming on stderr, no profile left.

| Scenario | Result |
| --- | --- |
| `inspect` | 8 bodies (string, JSON, form, Blob of 16 parts, multipart with a file, at 64 KiB; string, Blob, multipart at 900 KB): facts recorded before the read, read on the UI thread, exact bytes at the handler and in the host's read, 0 to 3 ms per read. The `ReadableStream` body: see below |
| `worker` | The same 8 bodies read on a worker thread (`off_ui`, `CoInitializeEx` `S_OK`, branch `agile` for every one), exact bytes, 0 to 2.8 ms per read |
| `worker-stall` | 900 KB Blob, 900 KB string and 64 KiB multipart with the UI thread asleep for 1500 ms right after the worker started: each read finished during the stall (read 0 to 2.8 ms; the request completed 1.5 s later when the UI resumed) |
| `gate` | Modeled held read, below |
| `close-during-upload` | Four windows closed 0, 20, 60 and 150 ms after the page was told to POST 900 KB, below |

### What every incoming stream is

Identical for all 43 streams the browser handed over (every body kind and size, and the page's own JSON report POSTs):

| Query, on the UI thread, before any read | Result |
| --- | --- |
| `QueryInterface(IAgileObject)` | `S_OK` |
| `QueryInterface(IMarshal)` → `GetUnmarshalClass(IStream, MSHCTX_INPROC)` | `S_OK` → `0000033a-0000-0000-c000-000000000046`, `CLSID_InProcFreeMarshaler` |
| `QueryInterface(ISequentialStream)` | `S_OK` |
| `Stat(STATFLAG_NONAME)` | `S_OK`, `STGTY_STREAM`, `cbSize` equal to the body's length (65536, 67017, 100505, 65824, 921600, 921888) |
| `Seek(0, STREAM_SEEK_CUR)` | `S_OK`, position 0 |

The intercepted request's headers carried no `Content-Length` for any body; `Stat` is where the length is known before reading.

### Where the read ran

In `worker` mode the reader thread differed from the UI thread for every body, and the worker's `CoInitializeEx(COINIT_MULTITHREADED)` returned `S_OK` each time (a fresh locked OS thread). The stall runs are the discriminator the record asked for: with the UI thread asleep and pumping nothing for 1500 ms, the 900 KB reads completed in under 3 ms, so `Read` executed on the worker and was not dispatched back to the UI apartment. The marshal branch (`CoMarshalInterThreadInterfaceInStream` then `CoGetInterfaceAndReleaseStream` on the worker) was never taken, because no stream lacked the marker; it is implemented and **untested natively**.

### `ReadableStream` bodies

`fetch(url, {method: "POST", body: new ReadableStream(...), duplex: "half"})` did not reject. The intercepted request had **no content** (`get_Content` returned null, so no stream to inspect), the handler received a 0-byte body, and the page received status 200. A streaming upload through the in-process transport therefore loses its body silently on this Runtime, and the interception layer cannot tell it from an empty POST (no `Content-Length` arrives either way). This is a transport fact for the webview contract, not a threading question; recorded in `inspect` and `worker` as an observation.

### The modeled held read (`gate`)

A 1 MiB gate stream, agile by construction, read by the worker path in window A while window B is open:

- The worker (thread 9000, `CoInitializeEx` `S_OK`) entered `Read` and stayed there; every one of the 17 `Read` calls later ran on that thread, so no proxy stood between the worker and the stream.
- While held: `ping` answered (ticks 26 after 0.5 s, 128 by release), window B loaded the probe page and its `checks` run completed, including two worker reads of its own POSTs; `status` showed one read in flight for A, none for B.
- `close A`: the read was found in flight and retained (`read_done false`); the controller closed; `release-waiting-reads` 24 ms after the close; the browser process exited 243 ms after the controller close while release still waited; 1.5 s later A was still not closed and `ping` still answered.
- `gate-release`: the read returned the whole 1 MiB (first `Read` 2359 ms after it was issued), the worker released the stream (`gate-stream-released`, final releases 1, on thread 9000), the UI handled the late result once (`body-read-after-close`, 1530 ms after the close), and A closed once, 1.565 s after the close command, with nothing pending.

This is scheduling and ownership evidence over a stream the host wrote; it says nothing about what a browser stream does when it does not return.

### Real close during an upload

| Close after the load command | Where it landed | Browser process exit after controller close |
| --- | --- | --- |
| 0 ms | before the page sent anything (no body stream yet) | 6.515 s |
| 20 ms | before the page sent anything | 6.461 s |
| 60 ms | **during the worker's read**: native interception settled once (503) while the worker still read; the read completed all 921600 bytes 31.6 ms after the close; the late result was dropped once; the window released after it | 6.592 s |
| 150 ms | after the read had completed and the handler answered | 275 ms |

What this establishes: a stream whose deferral was completed and whose response was installed stayed readable while the host held its reference, on this Runtime, for 32 ms; and the close path settled exactly once in every ordering. What it does not establish: how long such a stream stays valid, or anything about a read that does not return (the real reads all returned within 3 ms).

Observation beside the point of the probe: the three windows closed while their page's navigation or fetch was still in flight took about 6.5 s for the browser process to exit; the one closed at rest took 275 ms. The stage 3 record saw the same 6.6 s after `quit` closed open windows. It correlates with network activity in flight at close, not with `quit`; still not understood, still no timeout.

## Reading

- **Agility is established**, by the documented sufficient condition, for every incoming stream this Runtime produced: `IAgileObject` is advertised and the object marshals with the free-threaded marshaler. A production host can check `IAgileObject` per stream on the UI thread and, on `S_OK`, read on another thread without marshaling; the probe did exactly that with raw pointers and verified bytes, thread identity and UI independence.
- **The bounded worker approach is supported**: one worker per body, a locked OS thread in the multithreaded apartment, bounded chunks, release before `CoUninitialize`, result posted to the UI thread; event arguments, deferral and every WebView2 call stayed on the UI thread throughout.
- **Nothing blocked.** Every real read returned in milliseconds and `Stat` knew the length up front; the streams behave as memory-backed. No blocked native read was reproduced; the gate models that case.
- **Open**: only one Runtime version was observed, so production still checks the capability per stream; the marshal fallback is untested natively; `ReadableStream` bodies are dropped by the transport before interception sees them; and no evidence says how long a stream outlives its completed deferral beyond the 32 ms seen.
