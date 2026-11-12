package main

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// The WebView2 bindings this experiment uses: IIDs and vtable slots, taken
// from WebView2.idl in the pinned SDK (Microsoft.Web.WebView2 1.0.4129.50).
// TestBindingsMatchIDL checks every entry below against that file. Only
// what the host calls or implements is here.
//
// The data is portable so the check runs on any platform; the calls that
// use it are in the windows files.

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

func mustGUID(s string) guid {
	h := strings.ReplaceAll(s, "-", "")
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != 16 {
		panic("bad GUID " + s)
	}
	var g guid
	g.Data1 = uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	g.Data2 = uint16(b[4])<<8 | uint16(b[5])
	g.Data3 = uint16(b[6])<<8 | uint16(b[7])
	copy(g.Data4[:], b[8:])
	return g
}

func guidString(g guid) string {
	return fmt.Sprintf("%08x-%04x-%04x-%02x%02x-%x", g.Data1, g.Data2, g.Data3, g.Data4[0], g.Data4[1], g.Data4[2:])
}

// Interfaces outside WebView2.idl (objidl.h / objidlbase.h): stable since
// COM's beginning, not checked by the IDL test.
var (
	iidIUnknown     = mustGUID("00000000-0000-0000-c000-000000000046")
	iidIAgileObject = mustGUID("94ea2b94-e9cc-49e0-c0ff-ee64ca8f5b90")
	iidIMarshal     = mustGUID("00000003-0000-0000-c000-000000000046")
)

const (
	slotQueryInterface = 0
	slotAddRef         = 1
	slotRelease        = 2
	streamRead         = 3 // ISequentialStream::Read
)

// Handler interfaces the host implements. Each has one method, Invoke, at
// slot 3, with two arguments after this.
var (
	iidEnvironmentCompleted = mustGUID("4e8a3389-c9d8-4bd2-b6b5-124fee6cc14d")
	iidControllerCompleted  = mustGUID("6c4819f3-c9b7-4260-8127-c9f5bde7f68c")
	iidWebResourceRequested = mustGUID("ab00b74c-15f1-4646-80e8-e76341d25d71")
	iidNavigationStarting   = mustGUID("9adbe429-f36d-432b-9ddc-f8881fbd76e3")
	iidNavigationCompleted  = mustGUID("d33a35bf-1c49-4f98-93ab-006e0533fe1c")
	iidNewWindowRequested   = mustGUID("d4c185fe-c81c-4989-97af-2d3fa7ab5651")
	iidProcessFailed        = mustGUID("79e0aea4-990b-42d9-aa1d-0fcc2e5bc7f1")
	handlerInvoke           = 3

	// Feasibility probes (-feasibility): more handlers the host implements.
	iidFrameNavigationStarting = mustGUID("e79908bf-2d5d-4968-83db-263fea2c1da3")
	iidGetCookiesCompleted     = mustGUID("5a4f5069-5c15-47c3-8646-f4de1c116670")
	iidCapturePreviewCompleted = mustGUID("697e05e9-3d8f-45fa-96f4-8ffe1ededaf5")
	iidBrowserProcessExited    = mustGUID("fa504257-a216-4911-a860-fe8825712861")

	// Interfaces the feasibility host asks for with QueryInterface.
	iidICoreWebView2_2   = mustGUID("9e8f0cf8-e670-4b5e-b2bc-73e061e3184c")
	iidICoreWebView2_13  = mustGUID("f75f09a8-667e-4983-88d6-c8773f315e84")
	iidIEnvironment5     = mustGUID("319e423d-e0d7-4b8d-9254-ae9475de9b17")
	iidICookieManager    = mustGUID("177cd9e7-b6f5-451a-94a0-5d7a3a4c4141")
	iidICookie           = mustGUID("ad26d6be-1486-43e6-bf87-a2034006ca21")
	iidICookieList       = mustGUID("f7f6f714-5d2a-43c6-9503-346ece02d186")
	iidIBrowserExitedArg = mustGUID("1f00663f-af8c-4782-9cdd-dd01c52e34cb")
	iidIProfile          = mustGUID("79110ad3-cd5d-4373-8bc3-c60658f17a5f")
	iidINavDoneArgs2     = mustGUID("fdf8b738-ee1e-4db2-a329-8d7d7b74d792")
	iidResponseReceived  = mustGUID("7de9898a-24f5-40c3-a2de-d4f458e69828") // handler the host implements
	iidIResponseRecvArgs = mustGUID("d1db483d-6796-4b8b-80fc-13712bb716f4")
	iidIResponseView     = mustGUID("79701053-7759-4162-8f7d-f1b3f084928d")
)

// Slots of the interfaces the host calls.
const (
	envCreateController          = 3
	envCreateWebResourceResponse = 4
	envGetBrowserVersionString   = 5

	ctrlPutIsVisible                      = 4
	ctrlPutBounds                         = 6
	ctrlMoveFocus                         = 12
	ctrlNotifyParentWindowPositionChanged = 23
	ctrlClose                             = 24
	ctrlGetCoreWebView2                   = 25

	wvGetSettings                      = 3
	wvNavigate                         = 5
	wvAddNavigationStarting            = 7
	wvRemoveNavigationStarting         = 8
	wvAddNavigationCompleted           = 15
	wvRemoveNavigationCompleted        = 16
	wvAddProcessFailed                 = 25
	wvRemoveProcessFailed              = 26
	wvGetBrowserProcessID              = 37
	wvAddNewWindowRequested            = 44
	wvRemoveNewWindowRequested         = 45
	wvOpenDevToolsWindow               = 51
	wvAddWebResourceRequested          = 55
	wvRemoveWebResourceRequested       = 56
	wvAddWebResourceRequestedFilter    = 57
	wvRemoveWebResourceRequestedFilter = 58

	setPutAreDevToolsEnabled = 12

	wrrGetRequest  = 3
	wrrPutResponse = 5
	wrrGetDeferral = 6

	reqGetURI     = 3
	reqGetMethod  = 5
	reqGetContent = 7
	reqGetHeaders = 9

	hdrsGetIterator = 8

	iterGetCurrentHeader    = 3
	iterGetHasCurrentHeader = 4
	iterMoveNext            = 5

	deferralComplete = 3

	navStartGetURI          = 3
	navStartPutCancel       = 8
	navStartGetNavigationID = 9

	navDoneGetIsSuccess      = 3
	navDoneGetWebErrorStatus = 4
	navDoneGetNavigationID   = 5

	newWinGetURI     = 3
	newWinPutHandled = 6

	pfGetProcessFailedKind = 3

	// Feasibility probes.
	wvGetSource                     = 4
	wvNavigateToString              = 6
	wvAddFrameNavigationStarting    = 17
	wvRemoveFrameNavigationStarting = 18
	wvCapturePreview                = 30
	wvReload                        = 31
	wvStop                          = 43
	wvGetDocumentTitle              = 48

	wv2AddWebResourceResponseReceived    = 61 // ICoreWebView2_2
	wv2RemoveWebResourceResponseReceived = 62
	wv2GetCookieManager                  = 66

	rrGetRequest  = 3 // ICoreWebView2WebResourceResponseReceivedEventArgs
	rrGetResponse = 4

	rvGetStatusCode = 4 // ICoreWebView2WebResourceResponseView

	wv13GetProfile = 105 // ICoreWebView2_13

	profileGetProfilePath = 5

	setPutIsScriptEnabled           = 4
	setPutIsWebMessageEnabled       = 6
	setPutAreHostObjectsAllowed     = 16
	setPutIsBuiltInErrorPageEnabled = 20

	navStartGetIsRedirected = 5

	navDone2GetHTTPStatusCode = 6 // ICoreWebView2NavigationCompletedEventArgs2

	wrrGetResourceContext = 7

	env5AddBrowserProcessExited    = 12
	env5RemoveBrowserProcessExited = 13

	bpeGetExitKind  = 3
	bpeGetProcessID = 4

	cmCreateCookie      = 3
	cmGetCookies        = 5
	cmAddOrUpdateCookie = 6

	cookieGetName       = 3
	cookieGetIsHttpOnly = 10
	cookiePutIsHttpOnly = 11
	cookieGetSameSite   = 12
	cookiePutSameSite   = 13
	cookieGetIsSession  = 16

	cookieListGetCount        = 3
	cookieListGetValueAtIndex = 4
)

type binding struct {
	iface   string
	iid     guid // zero for interfaces the host only calls
	methods map[string]int
}

// bindings is what TestBindingsMatchIDL checks.
var bindings = []binding{
	{"ICoreWebView2Environment", guid{}, map[string]int{
		"CreateCoreWebView2Controller": envCreateController,
		"CreateWebResourceResponse":    envCreateWebResourceResponse,
		"get_BrowserVersionString":     envGetBrowserVersionString,
	}},
	{"ICoreWebView2Controller", guid{}, map[string]int{
		"put_IsVisible":                     ctrlPutIsVisible,
		"put_Bounds":                        ctrlPutBounds,
		"MoveFocus":                         ctrlMoveFocus,
		"NotifyParentWindowPositionChanged": ctrlNotifyParentWindowPositionChanged,
		"Close":                             ctrlClose,
		"get_CoreWebView2":                  ctrlGetCoreWebView2,
	}},
	{"ICoreWebView2", guid{}, map[string]int{
		"get_Settings":                     wvGetSettings,
		"Navigate":                         wvNavigate,
		"add_NavigationStarting":           wvAddNavigationStarting,
		"remove_NavigationStarting":        wvRemoveNavigationStarting,
		"add_NavigationCompleted":          wvAddNavigationCompleted,
		"remove_NavigationCompleted":       wvRemoveNavigationCompleted,
		"add_ProcessFailed":                wvAddProcessFailed,
		"remove_ProcessFailed":             wvRemoveProcessFailed,
		"get_BrowserProcessId":             wvGetBrowserProcessID,
		"add_NewWindowRequested":           wvAddNewWindowRequested,
		"remove_NewWindowRequested":        wvRemoveNewWindowRequested,
		"OpenDevToolsWindow":               wvOpenDevToolsWindow,
		"add_WebResourceRequested":         wvAddWebResourceRequested,
		"remove_WebResourceRequested":      wvRemoveWebResourceRequested,
		"AddWebResourceRequestedFilter":    wvAddWebResourceRequestedFilter,
		"RemoveWebResourceRequestedFilter": wvRemoveWebResourceRequestedFilter,
	}},
	{"ICoreWebView2Settings", guid{}, map[string]int{"put_AreDevToolsEnabled": setPutAreDevToolsEnabled}},
	{"ICoreWebView2WebResourceRequestedEventArgs", guid{}, map[string]int{
		"get_Request": wrrGetRequest, "put_Response": wrrPutResponse, "GetDeferral": wrrGetDeferral,
	}},
	{"ICoreWebView2WebResourceRequest", guid{}, map[string]int{
		"get_Uri": reqGetURI, "get_Method": reqGetMethod, "get_Content": reqGetContent, "get_Headers": reqGetHeaders,
	}},
	{"ICoreWebView2HttpRequestHeaders", guid{}, map[string]int{"GetIterator": hdrsGetIterator}},
	{"ICoreWebView2HttpHeadersCollectionIterator", guid{}, map[string]int{
		"GetCurrentHeader": iterGetCurrentHeader, "get_HasCurrentHeader": iterGetHasCurrentHeader, "MoveNext": iterMoveNext,
	}},
	{"ICoreWebView2Deferral", guid{}, map[string]int{"Complete": deferralComplete}},
	{"ICoreWebView2NavigationStartingEventArgs", guid{}, map[string]int{
		"get_Uri": navStartGetURI, "put_Cancel": navStartPutCancel, "get_NavigationId": navStartGetNavigationID,
	}},
	{"ICoreWebView2NavigationCompletedEventArgs", guid{}, map[string]int{
		"get_IsSuccess": navDoneGetIsSuccess, "get_WebErrorStatus": navDoneGetWebErrorStatus, "get_NavigationId": navDoneGetNavigationID,
	}},
	{"ICoreWebView2NewWindowRequestedEventArgs", guid{}, map[string]int{"get_Uri": newWinGetURI, "put_Handled": newWinPutHandled}},
	{"ICoreWebView2ProcessFailedEventArgs", guid{}, map[string]int{"get_ProcessFailedKind": pfGetProcessFailedKind}},
	{"ICoreWebView2CreateCoreWebView2EnvironmentCompletedHandler", iidEnvironmentCompleted, map[string]int{"Invoke": handlerInvoke}},
	{"ICoreWebView2CreateCoreWebView2ControllerCompletedHandler", iidControllerCompleted, map[string]int{"Invoke": handlerInvoke}},
	{"ICoreWebView2WebResourceRequestedEventHandler", iidWebResourceRequested, map[string]int{"Invoke": handlerInvoke}},
	{"ICoreWebView2NavigationStartingEventHandler", iidNavigationStarting, map[string]int{"Invoke": handlerInvoke}},
	{"ICoreWebView2NavigationCompletedEventHandler", iidNavigationCompleted, map[string]int{"Invoke": handlerInvoke}},
	{"ICoreWebView2NewWindowRequestedEventHandler", iidNewWindowRequested, map[string]int{"Invoke": handlerInvoke}},
	{"ICoreWebView2ProcessFailedEventHandler", iidProcessFailed, map[string]int{"Invoke": handlerInvoke}},

	// Feasibility probes.
	{"ICoreWebView2", guid{}, map[string]int{
		"get_Source": wvGetSource, "NavigateToString": wvNavigateToString,
		"add_FrameNavigationStarting": wvAddFrameNavigationStarting, "remove_FrameNavigationStarting": wvRemoveFrameNavigationStarting,
		"CapturePreview": wvCapturePreview, "Reload": wvReload, "Stop": wvStop, "get_DocumentTitle": wvGetDocumentTitle,
	}},
	{"ICoreWebView2_2", iidICoreWebView2_2, map[string]int{"get_CookieManager": wv2GetCookieManager,
		"add_WebResourceResponseReceived": wv2AddWebResourceResponseReceived, "remove_WebResourceResponseReceived": wv2RemoveWebResourceResponseReceived}},
	{"ICoreWebView2WebResourceResponseReceivedEventArgs", iidIResponseRecvArgs, map[string]int{"get_Request": rrGetRequest, "get_Response": rrGetResponse}},
	{"ICoreWebView2WebResourceResponseView", iidIResponseView, map[string]int{"get_StatusCode": rvGetStatusCode}},
	{"ICoreWebView2WebResourceResponseReceivedEventHandler", iidResponseReceived, map[string]int{"Invoke": handlerInvoke}},
	{"ICoreWebView2_13", iidICoreWebView2_13, map[string]int{"get_Profile": wv13GetProfile}},
	{"ICoreWebView2Profile", iidIProfile, map[string]int{"get_ProfilePath": profileGetProfilePath}},
	{"ICoreWebView2Settings", guid{}, map[string]int{
		"put_IsScriptEnabled": setPutIsScriptEnabled, "put_IsWebMessageEnabled": setPutIsWebMessageEnabled,
		"put_AreHostObjectsAllowed": setPutAreHostObjectsAllowed, "put_IsBuiltInErrorPageEnabled": setPutIsBuiltInErrorPageEnabled,
	}},
	{"ICoreWebView2NavigationStartingEventArgs", guid{}, map[string]int{"get_IsRedirected": navStartGetIsRedirected}},
	{"ICoreWebView2NavigationCompletedEventArgs2", iidINavDoneArgs2, map[string]int{"get_HttpStatusCode": navDone2GetHTTPStatusCode}},
	{"ICoreWebView2WebResourceRequestedEventArgs", guid{}, map[string]int{"get_ResourceContext": wrrGetResourceContext}},
	{"ICoreWebView2Environment5", iidIEnvironment5, map[string]int{
		"add_BrowserProcessExited": env5AddBrowserProcessExited, "remove_BrowserProcessExited": env5RemoveBrowserProcessExited,
	}},
	{"ICoreWebView2BrowserProcessExitedEventArgs", iidIBrowserExitedArg, map[string]int{
		"get_BrowserProcessExitKind": bpeGetExitKind, "get_BrowserProcessId": bpeGetProcessID,
	}},
	{"ICoreWebView2CookieManager", iidICookieManager, map[string]int{
		"CreateCookie": cmCreateCookie, "GetCookies": cmGetCookies, "AddOrUpdateCookie": cmAddOrUpdateCookie,
	}},
	{"ICoreWebView2Cookie", iidICookie, map[string]int{
		"get_Name": cookieGetName, "get_IsHttpOnly": cookieGetIsHttpOnly, "put_IsHttpOnly": cookiePutIsHttpOnly, "get_SameSite": cookieGetSameSite,
		"put_SameSite": cookiePutSameSite, "get_IsSession": cookieGetIsSession,
	}},
	{"ICoreWebView2CookieList", iidICookieList, map[string]int{"get_Count": cookieListGetCount, "GetValueAtIndex": cookieListGetValueAtIndex}},
	{"ICoreWebView2FrameNavigationStartingEventHandler", iidFrameNavigationStarting, map[string]int{"Invoke": handlerInvoke}},
	{"ICoreWebView2GetCookiesCompletedHandler", iidGetCookiesCompleted, map[string]int{"Invoke": handlerInvoke}},
	{"ICoreWebView2CapturePreviewCompletedHandler", iidCapturePreviewCompleted, map[string]int{"Invoke": handlerInvoke}},
	{"ICoreWebView2BrowserProcessExitedEventHandler", iidBrowserProcessExited, map[string]int{"Invoke": handlerInvoke}},
}
