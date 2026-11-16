package webview2

import (
	_ "embed"
	"errors"
	"fmt"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// The pinned loader, with the notices its license asks every distribution to
// carry. Both travel in every executable that links this package; see
// loader.go for the pin.
var (
	//go:embed sdk/WebView2Loader.dll
	loaderBlob []byte
	//go:embed sdk/LICENSE.txt
	loaderLicense string
	//go:embed sdk/NOTICE.txt
	loaderNotice string
)

// Notices returns the loader's redistribution texts, as embedded.
func Notices() (license, notice string) { return loaderLicense, loaderNotice }

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	ole32    = syscall.NewLazyDLL("ole32.dll")

	pRegisterClassExW   = user32.NewProc("RegisterClassExW")
	pCreateWindowExW    = user32.NewProc("CreateWindowExW")
	pDefWindowProcW     = user32.NewProc("DefWindowProcW")
	pPostMessageW       = user32.NewProc("PostMessageW")
	pGetModuleHandleW   = kernel32.NewProc("GetModuleHandleW")
	pGetCurrentThreadID = kernel32.NewProc("GetCurrentThreadId")
	pCoInitializeEx     = ole32.NewProc("CoInitializeEx")
	pCoTaskMemAlloc     = ole32.NewProc("CoTaskMemAlloc")
	pCoTaskMemFree      = ole32.NewProc("CoTaskMemFree")
)

const (
	hopClass   = "dfx2WebView2Hop"
	wmHop      = 0x8000 + 0x20 // WM_APP + 0x20: run the posted functions
	wmNCCreate = 0x0081
	hwndMsg    = ^uintptr(2) // HWND_MESSAGE

	coinitApartmentThreaded = 0x2
	coinitDisableOLE1DDE    = 0x4
)

type wndClassEx struct {
	size, style                        uint32
	wndProc                            uintptr
	clsExtra, wndExtra                 int32
	instance, icon, cursor, background uintptr
	menuName, className                *uint16
	iconSm                             uintptr
}

type rect struct{ left, top, right, bottom int32 }

// hop is the UI-thread message-only window that runs functions posted from
// other threads (stream releases, dropped handlers) and from the UI thread
// itself one message later. It lives for the process.
var hop struct {
	hwnd    uintptr
	queue   *callbackQueue
	wndProc uintptr
}

var installOnce struct {
	sync.Once
	err error
}

// install binds the native surface, creates the callback vtables and the hop
// window, and records the UI thread. Once per process, on the UI thread.
func install() error {
	installOnce.Do(func() { installOnce.err = bind() })
	return installOnce.err
}

func bind() error {
	for _, p := range []*syscall.LazyProc{pRegisterClassExW, pCreateWindowExW, pDefWindowProcW, pPostMessageW, pGetModuleHandleW,
		pGetCurrentThreadID, pCoInitializeEx, pCoTaskMemAlloc, pCoTaskMemFree} {
		if err := p.Find(); err != nil {
			return fmt.Errorf("WebView2 host requires %s: %w", p.Name, err)
		}
	}
	initObjects()
	t, _, _ := pGetCurrentThreadID.Call()
	objects.uiThread = uint32(t)
	inst, _, err := pGetModuleHandleW.Call(0)
	if inst == 0 {
		return fmt.Errorf("GetModuleHandleW: %w", err)
	}
	hop.wndProc = syscall.NewCallback(hopProc)
	class := utf16z(hopClass)
	wc := wndClassEx{wndProc: hop.wndProc, instance: inst, className: class}
	wc.size = uint32(unsafe.Sizeof(wc))
	if atom, _, err := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
		return fmt.Errorf("RegisterClassExW(%s): %w", hopClass, err)
	}
	hwnd, _, err := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(utf16z("dfx2 WebView2"))), 0, 0, 0, 0, 0, hwndMsg, 0, inst, 0)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowExW(%s): %w", hopClass, err)
	}
	hop.hwnd = hwnd
	hop.queue = &callbackQueue{
		wake: func() bool {
			ok, _, _ := pPostMessageW.Call(hwnd, wmHop, 0, 0)
			return ok != 0
		},
		after: func(delay time.Duration, retry func()) { time.AfterFunc(delay, retry) },
	}
	n = realAPI()
	return nil
}

func hopProc(hwnd, msg, wp, lp uintptr) uintptr {
	if msg == wmHop && hwnd == hop.hwnd {
		hop.queue.dispatch()
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}

// post queues fn for the UI thread, from any thread.
func post(fn func()) {
	hop.queue.post(fn)
}

// call invokes vtable slot i of a COM interface pointer with this first.
// Pointer arguments converted to uintptr at the call site stay valid:
// uintptrescapes keeps their targets alive for the call.
//
//go:uintptrescapes
func call(obj uintptr, i int, args ...uintptr) hresult {
	vtbl := *(*uintptr)(unsafe.Add(unsafe.Pointer(nil), obj))
	fn := *(*uintptr)(unsafe.Add(unsafe.Pointer(nil), vtbl+uintptr(i)*unsafe.Sizeof(uintptr(0))))
	a := make([]uintptr, 0, len(args)+1)
	a = append(a, obj)
	a = append(a, args...)
	r, _, _ := syscall.SyscallN(fn, a...)
	return hresult(r)
}

// callProc calls a DLL export, with the same escape rule.
//
//go:uintptrescapes
func callProc(p interface {
	Call(...uintptr) (uintptr, uintptr, error)
}, args ...uintptr) hresult {
	r, _, _ := p.Call(args...)
	return hresult(r)
}

func utf16z(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		p, _ = syscall.UTF16PtrFromString("invalid string")
	}
	return p
}

func wlen(p *uint16) int {
	if p == nil {
		return 0
	}
	n := 0
	for q := unsafe.Pointer(p); *(*uint16)(q) != 0; q = unsafe.Add(q, 2) {
		n++
	}
	return n
}

// takeString copies a callee-allocated LPWSTR and frees it with
// CoTaskMemFree, as the WebView2 API conventions require.
func takeString(p *uint16) string {
	if p == nil {
		return ""
	}
	s := syscall.UTF16ToString(unsafe.Slice(p, wlen(p)))
	pCoTaskMemFree.Call(uintptr(unsafe.Pointer(p)))
	return s
}

// getString reads one LPWSTR property.
func getString(obj uintptr, slot int) string {
	var p *uint16
	if call(obj, slot, uintptr(unsafe.Pointer(&p))).failed() {
		return ""
	}
	return takeString(p)
}

var dlls struct {
	sync.Mutex
	byHandle map[uintptr]*syscall.DLL
}

func realAPI() api {
	return api{
		initCOM: func() error {
			hr := callProc(pCoInitializeEx, 0, coinitApartmentThreaded|coinitDisableOLE1DDE)
			if hr == sOK || hr == sFalse {
				return nil
			}
			return fmt.Errorf("CoInitializeEx(COINIT_APARTMENTTHREADED) on the UI thread: HRESULT %v", hr)
		},
		loaderBlob: func() []byte { return loaderBlob },
		loadLibrary: func(path string) (uintptr, error) {
			dll, err := syscall.LoadDLL(path)
			if err != nil {
				return 0, err
			}
			dlls.Lock()
			if dlls.byHandle == nil {
				dlls.byHandle = make(map[uintptr]*syscall.DLL)
			}
			dlls.byHandle[uintptr(dll.Handle)] = dll
			dlls.Unlock()
			return uintptr(dll.Handle), nil
		},
		freeLibrary: func(lib uintptr) error {
			dlls.Lock()
			dll := dlls.byHandle[lib]
			delete(dlls.byHandle, lib)
			dlls.Unlock()
			if dll == nil {
				return errors.New("loader handle unknown")
			}
			return dll.Release()
		},
		runtimeVersion: func(lib uintptr) (string, error) {
			dlls.Lock()
			dll := dlls.byHandle[lib]
			dlls.Unlock()
			if dll == nil {
				return "", errors.New("loader handle unknown")
			}
			p, err := dll.FindProc("GetAvailableCoreWebView2BrowserVersionString")
			if err != nil {
				return "", err
			}
			var v *uint16
			hr := callProc(p, 0, uintptr(unsafe.Pointer(&v)))
			version := takeString(v)
			if hr.failed() || version == "" {
				return "", fmt.Errorf("WebView2 Runtime not found (HRESULT %v): install the Evergreen WebView2 Runtime from https://developer.microsoft.com/microsoft-edge/webview2/ (Windows 11 includes it)", hr)
			}
			return version, nil
		},
		post:       post,
		newHandler: newHandlerObject,
		queryInterface: func(obj uintptr, iid *guid) (uintptr, hresult) {
			var p uintptr
			hr := call(obj, slotQueryInterface, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&p)))
			return p, hr
		},
		addRef:  func(obj uintptr) { call(obj, slotAddRef) },
		release: func(obj uintptr) { call(obj, slotRelease) },
		createEnvironment: func(lib uintptr, userData string, cb handler) hresult {
			dlls.Lock()
			dll := dlls.byHandle[lib]
			dlls.Unlock()
			if dll == nil {
				return eFail
			}
			p, err := dll.FindProc("CreateCoreWebView2EnvironmentWithOptions")
			if err != nil {
				return eFail
			}
			return callProc(p, 0, uintptr(unsafe.Pointer(utf16z(userData))), 0, cb.ptr())
		},
		envVersion:    func(env uintptr) string { return getString(env, envGetBrowserVersionString) },
		envCreateCtrl: func(env, parent uintptr, cb handler) hresult { return call(env, envCreateController, parent, cb.ptr()) },
		envCreateResponse: func(env, body uintptr, status int, reason, headers string) (uintptr, hresult) {
			var r uintptr
			hr := call(env, envCreateWebResourceResponse, body, uintptr(status), uintptr(unsafe.Pointer(utf16z(reason))),
				uintptr(unsafe.Pointer(utf16z(headers))), uintptr(unsafe.Pointer(&r)))
			return r, hr
		},
		env5AddExited: func(env5 uintptr, cb handler) (int64, hresult) {
			var token int64
			hr := call(env5, env5AddBrowserProcessExited, cb.ptr(), uintptr(unsafe.Pointer(&token)))
			return token, hr
		},
		env5RemoveExited: func(env5 uintptr, token int64) { call(env5, env5RemoveBrowserProcessExited, uintptr(token)) },
		exitedArgs: func(args uintptr) (int32, uint32) {
			var kind int32
			var pid uint32
			call(args, bpeGetExitKind, uintptr(unsafe.Pointer(&kind)))
			call(args, bpeGetProcessID, uintptr(unsafe.Pointer(&pid)))
			return kind, pid
		},
		ctrlWebView: func(ctl uintptr) (uintptr, hresult) {
			var wv uintptr
			hr := call(ctl, ctrlGetCoreWebView2, uintptr(unsafe.Pointer(&wv)))
			return wv, hr
		},
		ctrlVisible: func(ctl uintptr, visible bool) { call(ctl, ctrlPutIsVisible, boolArg(visible)) },
		ctrlBounds: func(ctl uintptr, width, height int32) {
			r := rect{right: width, bottom: height}
			call(ctl, ctrlPutBounds, uintptr(unsafe.Pointer(&r))) // RECT is 16 bytes: passed by reference to a copy
		},
		ctrlNotifyMoved: func(ctl uintptr) { call(ctl, ctrlNotifyParentWindowPositionChanged) },
		ctrlFocus:       func(ctl uintptr) { call(ctl, ctrlMoveFocus, moveFocusProgrammatic) },
		ctrlClose:       func(ctl uintptr) hresult { return call(ctl, ctrlClose) },
		wvBrowserPID: func(wv uintptr) uint32 {
			var pid uint32
			call(wv, wvGetBrowserProcessID, uintptr(unsafe.Pointer(&pid)))
			return pid
		},
		wvSettings: func(wv uintptr, devtools bool) hresult {
			var settings uintptr
			if hr := call(wv, wvGetSettings, uintptr(unsafe.Pointer(&settings))); hr.failed() || settings == 0 {
				return hr
			}
			defer call(settings, slotRelease)
			for _, s := range []struct {
				slot  int
				value bool
			}{{setPutAreDevToolsEnabled, devtools}, {setPutIsWebMessageEnabled, false}, {setPutAreHostObjectsAllowed, false}, {setPutIsBuiltInErrorPageEnabled, false}} {
				if hr := call(settings, s.slot, boolArg(s.value)); hr.failed() {
					return hr
				}
			}
			return sOK
		},
		wvProfilePath: func(wv uintptr) (string, bool) {
			var wv13 uintptr
			if hr := call(wv, slotQueryInterface, uintptr(unsafe.Pointer(&iidICoreWebView2_13)), uintptr(unsafe.Pointer(&wv13))); hr.failed() || wv13 == 0 {
				return "", false
			}
			defer call(wv13, slotRelease)
			var profile uintptr
			if hr := call(wv13, wv13GetProfile, uintptr(unsafe.Pointer(&profile))); hr.failed() || profile == 0 {
				return "", false
			}
			defer call(profile, slotRelease)
			return getString(profile, profileGetProfilePath), true
		},
		wvAddEvent: func(wv uintptr, add int, cb handler) (int64, hresult) {
			var token int64
			hr := call(wv, add, cb.ptr(), uintptr(unsafe.Pointer(&token)))
			return token, hr
		},
		wvRemoveEvent: func(wv uintptr, remove int, token int64) { call(wv, remove, uintptr(token)) }, // EventRegistrationToken: 8 bytes by value
		wvAddFilter: func(wv uintptr, pattern string, context int32) hresult {
			return call(wv, wvAddWebResourceRequestedFilter, uintptr(unsafe.Pointer(utf16z(pattern))), uintptr(context))
		},
		wvNavigate: func(wv uintptr, uri string) hresult {
			return call(wv, wvNavigate, uintptr(unsafe.Pointer(utf16z(uri))))
		},
		wvNavigateHTML: func(wv uintptr, html string) hresult {
			return call(wv, wvNavigateToString, uintptr(unsafe.Pointer(utf16z(html))))
		},
		wvStop:         func(wv uintptr) { call(wv, wvStop) },
		wvSource:       func(wv uintptr) string { return getString(wv, wvGetSource) },
		wvTitle:        func(wv uintptr) string { return getString(wv, wvGetDocumentTitle) },
		wvOpenDevTools: func(wv uintptr) { call(wv, wvOpenDevToolsWindow) },
		wvCookieManager: func(wv2 uintptr) (uintptr, hresult) {
			var cm uintptr
			hr := call(wv2, wv2GetCookieManager, uintptr(unsafe.Pointer(&cm)))
			return cm, hr
		},
		navStarting: func(args uintptr) (string, uint64, bool) {
			var id uint64
			var redirected int32
			uri := getString(args, navStartGetURI)
			call(args, navStartGetNavigationID, uintptr(unsafe.Pointer(&id)))
			call(args, navStartGetIsRedirected, uintptr(unsafe.Pointer(&redirected)))
			return uri, id, redirected != 0
		},
		navCancel: func(args uintptr) { call(args, navStartPutCancel, 1) },
		navCompleted: func(args uintptr) (bool, int32, uint64, int32) {
			var ok, status, httpStatus int32
			var id uint64
			call(args, navDoneGetIsSuccess, uintptr(unsafe.Pointer(&ok)))
			call(args, navDoneGetWebErrorStatus, uintptr(unsafe.Pointer(&status)))
			call(args, navDoneGetNavigationID, uintptr(unsafe.Pointer(&id)))
			var args2 uintptr
			if hr := call(args, slotQueryInterface, uintptr(unsafe.Pointer(&iidINavDoneArgs2)), uintptr(unsafe.Pointer(&args2))); !hr.failed() && args2 != 0 {
				call(args2, navDone2GetHTTPStatus, uintptr(unsafe.Pointer(&httpStatus)))
				call(args2, slotRelease)
			}
			return ok != 0, status, id, httpStatus
		},
		newWindowHandled: func(args uintptr) { call(args, newWinPutHandled, 1) },
		processFailed: func(args uintptr) (int32, int32, int32, string) {
			var kind, reason, exitCode int32
			description := ""
			call(args, pfGetProcessFailedKind, uintptr(unsafe.Pointer(&kind)))
			var args2 uintptr
			if hr := call(args, slotQueryInterface, uintptr(unsafe.Pointer(&iidIProcessFailed2)), uintptr(unsafe.Pointer(&args2))); !hr.failed() && args2 != 0 {
				call(args2, pf2GetReason, uintptr(unsafe.Pointer(&reason)))
				call(args2, pf2GetExitCode, uintptr(unsafe.Pointer(&exitCode)))
				description = getString(args2, pf2GetProcessDescription)
				call(args2, slotRelease)
			}
			return kind, reason, exitCode, description
		},
		permissionDeny: func(args uintptr) { call(args, permissionPutState, permissionDeny) },
		downloadCancel: func(args uintptr) {
			call(args, downloadPutCancel, 1)
			call(args, downloadPutHandled, 1)
		},
		responseReceived: func(args uintptr) (string, int32) {
			var req, resp uintptr
			var status int32
			uri := ""
			if !call(args, rrGetRequest, uintptr(unsafe.Pointer(&req))).failed() && req != 0 {
				uri = getString(req, reqGetURI)
				call(req, slotRelease)
			}
			if !call(args, rrGetResponse, uintptr(unsafe.Pointer(&resp))).failed() && resp != 0 {
				call(resp, rvGetStatusCode, uintptr(unsafe.Pointer(&status)))
				call(resp, slotRelease)
			}
			return uri, status
		},
		requestInfo: func(args uintptr) (uintptr, string, string, int32) {
			var req uintptr
			var context int32
			if hr := call(args, wrrGetRequest, uintptr(unsafe.Pointer(&req))); hr.failed() || req == 0 {
				return 0, "", "", 0
			}
			call(args, wrrGetResourceContext, uintptr(unsafe.Pointer(&context)))
			return req, getString(req, reqGetURI), getString(req, reqGetMethod), context
		},
		requestHeaders: func(req uintptr) (uintptr, hresult) {
			var headers, it uintptr
			hr := call(req, reqGetHeaders, uintptr(unsafe.Pointer(&headers)))
			if hr.failed() || headers == 0 {
				return 0, hr
			}
			defer call(headers, slotRelease)
			hr = call(headers, hdrsGetIterator, uintptr(unsafe.Pointer(&it)))
			return it, hr
		},
		iterHas: func(it uintptr) bool {
			var has int32
			call(it, iterGetHasCurrentHeader, uintptr(unsafe.Pointer(&has)))
			return has != 0
		},
		iterCurrent: func(it uintptr) (*uint16, *uint16, bool) {
			var name, value *uint16
			hr := call(it, iterGetCurrentHeader, uintptr(unsafe.Pointer(&name)), uintptr(unsafe.Pointer(&value)))
			return name, value, !hr.failed()
		},
		iterNext: func(it uintptr) {
			var has int32
			call(it, iterMoveNext, uintptr(unsafe.Pointer(&has)))
		},
		wlen: wlen,
		take: takeString,
		free: func(p *uint16) {
			if p != nil {
				pCoTaskMemFree.Call(uintptr(unsafe.Pointer(p)))
			}
		},
		requestContent: func(req uintptr) uintptr {
			var content uintptr
			if call(req, reqGetContent, uintptr(unsafe.Pointer(&content))).failed() {
				return 0
			}
			return content
		},
		streamRead: func(stream uintptr, buf []byte) (int, bool, hresult) {
			if len(buf) == 0 {
				return 0, false, sOK
			}
			var read uint32
			hr := call(stream, streamRead, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&read)))
			if hr.failed() {
				return 0, true, hr
			}
			return int(read), hr == sFalse || read == 0, hr
		},
		deferral: func(args uintptr) (uintptr, hresult) {
			var def uintptr
			hr := call(args, wrrGetDeferral, uintptr(unsafe.Pointer(&def)))
			return def, hr
		},
		deferralDone:  func(def uintptr) hresult { return call(def, deferralComplete) },
		putResponse:   func(args, resp uintptr) hresult { return call(args, wrrPutResponse, resp) },
		newBodyStream: newStreamObject,
		cookieCreate: func(cm uintptr, name, value, domain, path string) (uintptr, hresult) {
			var cookie uintptr
			hr := call(cm, cmCreateCookie, uintptr(unsafe.Pointer(utf16z(name))), uintptr(unsafe.Pointer(utf16z(value))),
				uintptr(unsafe.Pointer(utf16z(domain))), uintptr(unsafe.Pointer(utf16z(path))), uintptr(unsafe.Pointer(&cookie)))
			return cookie, hr
		},
		cookieSecure: func(cookie uintptr) {
			call(cookie, cookiePutIsHttpOnly, 1)
			call(cookie, cookiePutSameSite, cookieSameSiteStrict)
		},
		cookieAdd: func(cm, cookie uintptr) hresult { return call(cm, cmAddOrUpdateCookie, cookie) },
		cookieList: func(cm uintptr, uri string, cb handler) hresult {
			return call(cm, cmGetCookies, uintptr(unsafe.Pointer(utf16z(uri))), cb.ptr())
		},
		cookieFound: func(list uintptr, name string) (bool, bool, int32) {
			var count uint32
			call(list, cookieListGetCount, uintptr(unsafe.Pointer(&count)))
			for i := uint32(0); i < count; i++ {
				var c uintptr
				if call(list, cookieListGetValueAtIndex, uintptr(i), uintptr(unsafe.Pointer(&c))).failed() || c == 0 {
					continue
				}
				if getString(c, cookieGetName) == name {
					var httpOnly, sameSite int32
					call(c, cookieGetIsHttpOnly, uintptr(unsafe.Pointer(&httpOnly)))
					call(c, cookieGetSameSite, uintptr(unsafe.Pointer(&sameSite)))
					call(c, slotRelease)
					return true, httpOnly != 0, sameSite
				}
				call(c, slotRelease)
			}
			return false, false, -1
		},
	}
}

func boolArg(b bool) uintptr {
	if b {
		return 1
	}
	return 0
}
