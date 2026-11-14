package webkit

import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Signatures match the installed GTK 3, GLib/Gio, libsoup 3 and WebKit2GTK
// 4.1 headers (checked by TestBindingsMatchHeaders). gboolean/gint/enums are
// int32, guint is uint32, gulong is uint64 and goffset/gint64 are int64 on
// Linux/amd64. No Go pointer is retained by native code: strings and the out
// pointers are used only during each synchronous call.
type api struct {
	// GLib, GObject, Gio.
	ref, refSink        func(uintptr) uintptr
	unref               func(uintptr)
	connect             func(uintptr, string, uintptr, uintptr, uintptr, int32) uint64
	disconnect          func(uintptr, uint64)
	idleAdd             func(int32, uintptr, uintptr, uintptr) uint32
	malloc              func(uintptr) uintptr
	free                func(uintptr)
	bytesNew            func(unsafe.Pointer, uintptr) uintptr
	bytesNewWithFree    func(uintptr, uintptr, uintptr, uintptr) uintptr
	bytesData           func(uintptr, *uintptr) unsafe.Pointer
	bytesUnref          func(uintptr)
	memoryStream        func(uintptr) uintptr
	readBytesAsync      func(uintptr, uintptr, int32, uintptr, uintptr, uintptr)
	readBytesFinish     func(uintptr, uintptr, *uintptr) uintptr
	cancellableNew      func() uintptr
	cancellableCancel   func(uintptr)
	errorNewLiteral     func(uint32, int32, string) uintptr
	errorFree           func(uintptr)
	quarkFromString     func(string) uint32
	quarkToString       func(uint32) string
	containerAdd        func(uintptr, uintptr)
	widgetShow          func(uintptr)
	widgetDestroy       func(uintptr)
	headersNew          func(int32) uintptr
	headersAppend       func(uintptr, string, string)
	headersIterInit     func(*[3]uintptr, uintptr)
	headersIterNext     func(*[3]uintptr, *uintptr, *uintptr) int32
	cookieNew           func(string, string, string, string, int32) uintptr
	cookieSetHTTPOnly   func(uintptr, int32)
	cookieSetSameSite   func(uintptr, int32)
	cookieFree          func(uintptr)
	majorVersion        func() uint32
	minorVersion        func() uint32
	microVersion        func() uint32
	contextNewEphemeral func() uintptr
	securityManager     func(uintptr) uintptr
	schemeAsSecure      func(uintptr, string)
	schemeAsCORS        func(uintptr, string)
	registerScheme      func(uintptr, string, uintptr, uintptr, uintptr)
	dataManager         func(uintptr) uintptr
	cookieManager       func(uintptr) uintptr
	addCookie           func(uintptr, uintptr, uintptr, uintptr, uintptr)
	addCookieFinish     func(uintptr, uintptr, *uintptr) int32
	viewNew             func(uintptr) uintptr
	viewSettings        func(uintptr) uintptr
	setDeveloperExtras  func(uintptr, int32)
	setOpenWindows      func(uintptr, int32)
	loadURI             func(uintptr, string)
	stopLoading         func(uintptr)
	loadAlternateHTML   func(uintptr, string, string, uintptr)
	viewURI             func(uintptr) string
	viewTitle           func(uintptr) string
	viewContentManager  func(uintptr) uintptr
	addFilter           func(uintptr, uintptr)
	filterStoreNew      func(string) uintptr
	filterStoreSave     func(uintptr, string, uintptr, uintptr, uintptr, uintptr)
	filterStoreFinish   func(uintptr, uintptr, *uintptr) uintptr
	filterUnref         func(uintptr)
	navigationAction    func(uintptr) uintptr
	actionRequest       func(uintptr) uintptr
	actionType          func(uintptr) int32
	actionIsRedirect    func(uintptr) int32
	requestURI          func(uintptr) string
	requestMethod       func(uintptr) string
	policyIgnore        func(uintptr)
	mimeSupported       func(uintptr) int32
	permissionDeny      func(uintptr)
	downloadCancel      func(uintptr)
	schemeURI           func(uintptr) uintptr // borrowed: viewed, never copied, by receive
	schemeMethod        func(uintptr) uintptr
	schemeHeaders       func(uintptr) uintptr
	schemeBody          func(uintptr) uintptr
	schemeFinish        func(uintptr, uintptr)
	schemeFinishError   func(uintptr, uintptr)
	responseNew         func(uintptr, int64) uintptr
	responseStatus      func(uintptr, uint32, uintptr) // NULL reason: the default phrase
	responseContentType func(uintptr, string)
	responseHeaders     func(uintptr, uintptr)

	// Signal reservation: libc's real-time range and JavaScriptCore's private
	// configuration entry point. Nothing that creates a JavaScriptCore context
	// or evaluates JavaScript is bound.
	rtmin, rtmax func() int32
	configureGC  func(int32) bool
}

var w api
var loadOnce sync.Once
var loadErr error

// configureSymbol is JavaScriptCore's private GC-signal entry point: exported
// by the installed library, absent from its public headers.
const configureSymbol = "JSConfigureSignalForGC"

// load binds every entry point once per process. Libraries stay loaded for
// the process: WebKit registers global types and runs process-wide threads.
func load() error {
	loadOnce.Do(func() { loadErr = bind() })
	return loadErr
}

func bind() error {
	groups := []struct {
		library  string
		symbols  map[string]any
		required bool
	}{
		{"libc.so.6", map[string]any{"__libc_current_sigrtmin": &w.rtmin, "__libc_current_sigrtmax": &w.rtmax}, true},
		{"libglib-2.0.so.0", map[string]any{
			"g_idle_add_full": &w.idleAdd, "g_malloc": &w.malloc, "g_free": &w.free, "g_bytes_new": &w.bytesNew,
			"g_bytes_new_with_free_func": &w.bytesNewWithFree, "g_bytes_get_data": &w.bytesData, "g_bytes_unref": &w.bytesUnref,
			"g_error_new_literal": &w.errorNewLiteral, "g_error_free": &w.errorFree,
			"g_quark_from_string": &w.quarkFromString, "g_quark_to_string": &w.quarkToString,
		}, true},
		{"libgobject-2.0.so.0", map[string]any{
			"g_object_ref": &w.ref, "g_object_ref_sink": &w.refSink, "g_object_unref": &w.unref,
			"g_signal_connect_data": &w.connect, "g_signal_handler_disconnect": &w.disconnect,
		}, true},
		{"libgio-2.0.so.0", map[string]any{
			"g_memory_input_stream_new_from_bytes": &w.memoryStream,
			"g_input_stream_read_bytes_async":      &w.readBytesAsync, "g_input_stream_read_bytes_finish": &w.readBytesFinish,
			"g_cancellable_new": &w.cancellableNew, "g_cancellable_cancel": &w.cancellableCancel,
		}, true},
		{"libgtk-3.so.0", map[string]any{
			"gtk_container_add": &w.containerAdd, "gtk_widget_show": &w.widgetShow, "gtk_widget_destroy": &w.widgetDestroy,
		}, true},
		{"libsoup-3.0.so.0", map[string]any{
			"soup_message_headers_new": &w.headersNew, "soup_message_headers_append": &w.headersAppend,
			"soup_message_headers_iter_init": &w.headersIterInit, "soup_message_headers_iter_next": &w.headersIterNext,
			"soup_cookie_new": &w.cookieNew, "soup_cookie_set_http_only": &w.cookieSetHTTPOnly,
			"soup_cookie_set_same_site_policy": &w.cookieSetSameSite, "soup_cookie_free": &w.cookieFree,
		}, true},
		{"libwebkit2gtk-4.1.so.0", map[string]any{
			"webkit_get_major_version": &w.majorVersion, "webkit_get_minor_version": &w.minorVersion,
			"webkit_get_micro_version":                                      &w.microVersion,
			"webkit_web_context_new_ephemeral":                              &w.contextNewEphemeral,
			"webkit_web_context_get_security_manager":                       &w.securityManager,
			"webkit_security_manager_register_uri_scheme_as_secure":         &w.schemeAsSecure,
			"webkit_security_manager_register_uri_scheme_as_cors_enabled":   &w.schemeAsCORS,
			"webkit_web_context_register_uri_scheme":                        &w.registerScheme,
			"webkit_web_context_get_website_data_manager":                   &w.dataManager,
			"webkit_website_data_manager_get_cookie_manager":                &w.cookieManager,
			"webkit_cookie_manager_add_cookie":                              &w.addCookie,
			"webkit_cookie_manager_add_cookie_finish":                       &w.addCookieFinish,
			"webkit_web_view_new_with_context":                              &w.viewNew,
			"webkit_web_view_get_settings":                                  &w.viewSettings,
			"webkit_settings_set_enable_developer_extras":                   &w.setDeveloperExtras,
			"webkit_settings_set_javascript_can_open_windows_automatically": &w.setOpenWindows,
			"webkit_web_view_load_uri":                                      &w.loadURI,
			"webkit_web_view_stop_loading":                                  &w.stopLoading,
			"webkit_web_view_load_alternate_html":                           &w.loadAlternateHTML,
			"webkit_web_view_get_uri":                                       &w.viewURI,
			"webkit_web_view_get_title":                                     &w.viewTitle,
			"webkit_web_view_get_user_content_manager":                      &w.viewContentManager,
			"webkit_user_content_manager_add_filter":                        &w.addFilter,
			"webkit_user_content_filter_store_new":                          &w.filterStoreNew,
			"webkit_user_content_filter_store_save":                         &w.filterStoreSave,
			"webkit_user_content_filter_store_save_finish":                  &w.filterStoreFinish,
			"webkit_user_content_filter_unref":                              &w.filterUnref,
			"webkit_navigation_policy_decision_get_navigation_action":       &w.navigationAction,
			"webkit_navigation_action_get_request":                          &w.actionRequest,
			"webkit_navigation_action_get_navigation_type":                  &w.actionType,
			"webkit_navigation_action_is_redirect":                          &w.actionIsRedirect,
			"webkit_uri_request_get_uri":                                    &w.requestURI,
			"webkit_uri_request_get_http_method":                            &w.requestMethod,
			"webkit_policy_decision_ignore":                                 &w.policyIgnore,
			"webkit_response_policy_decision_is_mime_type_supported":        &w.mimeSupported,
			"webkit_permission_request_deny":                                &w.permissionDeny,
			"webkit_download_cancel":                                        &w.downloadCancel,
			"webkit_uri_scheme_request_get_uri":                             &w.schemeURI,
			"webkit_uri_scheme_request_get_http_method":                     &w.schemeMethod,
			"webkit_uri_scheme_request_get_http_headers":                    &w.schemeHeaders,
			"webkit_uri_scheme_request_get_http_body":                       &w.schemeBody,
			"webkit_uri_scheme_request_finish_with_response":                &w.schemeFinish,
			"webkit_uri_scheme_request_finish_error":                        &w.schemeFinishError,
			"webkit_uri_scheme_response_new":                                &w.responseNew,
			"webkit_uri_scheme_response_set_status":                         &w.responseStatus,
			"webkit_uri_scheme_response_set_content_type":                   &w.responseContentType,
			"webkit_uri_scheme_response_set_http_headers":                   &w.responseHeaders,
		}, true},
		{"libjavascriptcoregtk-4.1.so.0", map[string]any{configureSymbol: &w.configureGC}, false},
	}
	for _, g := range groups {
		h, err := purego.Dlopen(g.library, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			return fmt.Errorf("WebKitGTK host needs GTK 3, libsoup 3 and WebKitGTK 4.1 (2.40 or newer): load %s: %w", g.library, err)
		}
		for name, target := range g.symbols {
			address, err := purego.Dlsym(h, name)
			if err != nil {
				if !g.required {
					continue // reported by the reservation, which needs it
				}
				return fmt.Errorf("WebKitGTK host needs WebKitGTK 4.1 2.40 or newer: missing %s: %w", name, err)
			}
			purego.RegisterFunc(target, address)
		}
	}
	return nil
}

// version reports the loaded runtime, for diagnostics and evidence.
func version() string {
	if w.majorVersion == nil {
		return "not loaded"
	}
	return fmt.Sprintf("%d.%d.%d", w.majorVersion(), w.minorVersion(), w.microVersion())
}

// cstring copies a borrowed NUL-terminated native string.
func cstring(p uintptr) string {
	if p == 0 {
		return ""
	}
	ptr := unsafe.Add(unsafe.Pointer(nil), p)
	n := 0
	for *(*byte)(unsafe.Add(ptr, n)) != 0 {
		n++
	}
	return string(unsafe.Slice((*byte)(ptr), n))
}

// view returns a borrowed native string as a Go string without copying it.
// It is valid only while the native owner keeps the storage; anything kept
// must be copied before then.
func view(p uintptr) string {
	if p == 0 {
		return ""
	}
	ptr := (*byte)(unsafe.Add(unsafe.Pointer(nil), p))
	n := 0
	for *(*byte)(unsafe.Add(unsafe.Pointer(ptr), n)) != 0 {
		n++
	}
	return unsafe.String(ptr, n)
}

// gerror reads and frees a GError: GQuark domain, gint code, gchar *message.
func gerror(p uintptr) (domain string, code int32, message string) {
	if p == 0 {
		return "", 0, ""
	}
	g := (*struct {
		domain  uint32
		code    int32
		message uintptr
	})(unsafe.Add(unsafe.Pointer(nil), p))
	domain, code, message = w.quarkToString(g.domain), g.code, cstring(g.message)
	w.errorFree(p)
	return domain, code, message
}

// borrowError reads a GError the caller does not own.
func borrowError(p uintptr) (domain string, code int32, message string) {
	if p == 0 {
		return "", 0, ""
	}
	g := (*struct {
		domain  uint32
		code    int32
		message uintptr
	})(unsafe.Add(unsafe.Pointer(nil), p))
	return w.quarkToString(g.domain), g.code, cstring(g.message)
}
