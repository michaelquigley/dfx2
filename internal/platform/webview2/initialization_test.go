package webview2

import (
	"strings"
	"testing"

	"github.com/michaelquigley/dfx2/internal/platform"
)

func TestClosingInitializationKeepsFailuresAndDrains(t *testing.T) {
	for _, stage := range []string{"environment", "controller", "cookies"} {
		for _, result := range []struct {
			name    string
			hr      hresult
			object  bool
			failure bool
		}{
			{"failed", eFail, false, true},
			{"abort is not our cancellation", eAbort, false, true},
			{"success without result", sOK, false, true},
			{"late success", sOK, true, false},
		} {
			t.Run(stage+"/"+result.name, func(t *testing.T) {
				h := &fakeHost{ready: true}
				f, b := newTestBrowser(t, h, platform.BrowserInProcess)
				var callback string
				var object uintptr
				switch stage {
				case "environment":
					b.Start()
					callback = "EnvironmentCompleted"
					object = 100
				case "controller":
					b.Start()
					f.invoke("EnvironmentCompleted", 0, 100)
					callback = "ControllerCompleted"
					object = 200
				case "cookies":
					// Exercise the real GetCookies completion wrapper without the unrelated
					// cross-platform profile fixture or controller setup.
					b.env, b.env5, b.ctl, b.wv2 = 100, 105, 200, 302
					b.browserStarted = true
					b.cookie = &platform.BrowserCookie{Name: "credential", Value: "secret", Origin: "http://127.0.0.1:4000"}
					b.installCookie()
					callback = "GetCookiesCompleted"
					object = 402
				}
				b.Close()
				if h.released != 0 {
					t.Fatal("released with initialization still pending")
				}
				if !result.object {
					object = 0
				}
				f.invoke(callback, uintptr(result.hr), object)
				want := 0
				if result.failure {
					want = 1
				}
				if len(h.failed) != want {
					t.Fatalf("failures=%v, want %d", h.failed, want)
				}
				if result.hr.failed() && !strings.Contains(h.failed[0].Error(), result.hr.String()) {
					t.Fatalf("HRESULT lost: %v", h.failed)
				}
				if b.navigated || b.cookieReady || h.readies != 0 || f.did("settings") || (stage == "environment" && f.did("create-controller")) {
					t.Fatalf("late result attached or navigated: %v", f.calls)
				}
				if b.envPending || b.ctlPending || b.owed != 0 {
					t.Fatalf("completion not settled: %+v", b.AuditionState())
				}
				if stage == "controller" && result.object && !f.did("controller-close") {
					t.Fatal("late controller not closed")
				}
				f.flush()
				if b.browserStarted {
					if h.released != 0 {
						t.Fatal("released before browser exit")
					}
					b.onBrowserProcessExited(exitKindNormal)
					f.flush()
				}
				b.Close()
				f.flush()
				if h.released != 1 || len(h.failed) != want {
					t.Fatalf("released=%d failures=%v", h.released, h.failed)
				}
			})
		}
	}
}

func TestClosingCookieVerificationFailureIsKept(t *testing.T) {
	for _, field := range []string{"missing cookie", "not HttpOnly"} {
		t.Run(field, func(t *testing.T) {
			f := installFake(t)
			h := &fakeHost{}
			b := &browser{host: h, closing: true}
			f.cookie.found = field != "missing cookie"
			f.cookie.httpOnly = field != "not HttpOnly"
			b.onCookies(sOK, 402, "credential")
			if len(h.failed) != 1 || !strings.Contains(h.failed[0].Error(), "endpoint credential not installed") {
				t.Fatalf("failure lost: %v", h.failed)
			}
			f.flush()
			if h.released != 1 || b.cookieReady {
				t.Fatalf("released=%d cookie ready=%v", h.released, b.cookieReady)
			}
		})
	}
}

func TestClosingDroppedCookieCompletionIsSilent(t *testing.T) {
	f := installFake(t)
	h := &fakeHost{}
	b := &browser{host: h, wv2: 302, cookie: &platform.BrowserCookie{Name: "credential", Value: "secret", Origin: "http://127.0.0.1:4000"}}
	b.installCookie()
	b.Close()
	if h.released != 0 {
		t.Fatal("released before dropped completion")
	}
	f.drop("GetCookiesCompleted")
	f.flush()
	b.Close()
	f.flush()
	if len(h.failed) != 0 || h.released != 1 || b.owed != 0 {
		t.Fatalf("failures=%v released=%d owed=%d", h.failed, h.released, b.owed)
	}
}
