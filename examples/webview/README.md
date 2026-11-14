# webview example

Auditions browser windows beside native ones in one application: in-process, owned loopback and attached-URL windows, each with its own session. The page is an ordinary `http.Handler` (`page.go`, with `static/`); its probes report to their own handler, and the host evaluates no JavaScript. On Linux/amd64 the browser is the system WebKitGTK 4.1; other platforms refuse browser windows.

```sh
make
webview -open native,inprocess,loopback
```

`-gc-signal` names the real-time signal reserved for JavaScriptCore's garbage collector (default 60); `-devtools` enables the inspector; `-resident` keeps running with no windows. Without `-open`, commands on stdin drive it; the [stage 2 driver](../../evidence/webview-integration/linux/webview_drive.py) uses them. See the package comment in `main.go`.

## Manual audition (Linux)

About ten minutes. Run from a terminal, so its output stays visible:

```sh
webview -open native,inprocess,loopback -devtools
```

Three windows open: `dfx2 native N`, `dfx2 webview I (inprocess)` and `dfx2 webview L (loopback)`.

1. **Both owned windows.** In I and L, the page shows its origin (`dfx2://app` and `http://127.0.0.1:PORT`), a blue square, and "stylesheet loaded". Press **Checks**: the log shows `query` `é&ü`, an `echo` of length 1005 and `missing` 404. In L, press **Stream**: two chunks arrive, the second after the first.
2. **Staying in.** In each owned window press **Navigate outside** and **Open popup**: the page stays where it is, and nothing opens. Then, in I only, click **404 page**: an ordinary application page reading "application status 404" appears (the page has no way back; I is done).
3. **Load failure.** In L, click **Failing page**: the framework's "This page did not load" page appears with **Retry** and **Return to start**. Retry shows the same page again; Return to start loads the start page. Then use the form button **POST to a failing page**: the error page says the form was not repeated and offers only Return to start.
4. **Resizing and inspection.** Resize and maximize each window; the page follows. Right-click a page and choose Inspect Element; close the inspector again.
5. **Closing.** Close I: N and L keep working (in L, Return to start and press Checks). Close L, then N: the application exits, and the terminal shows `"event":"return","error":"<nil>"` with no other errors.

In-process windows cannot show the failure page from a handler (a deliberate abort answers 502), and WebKitGTK follows no redirects there; [webview.md](../../docs/current/webview.md#in-process-transport) explains. Report anything that hangs, crashes, opens an outside page or prints a warning.
