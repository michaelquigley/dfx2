//go:build linux

package main

import (
	"fmt"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Signatures/ownership checked against the installed GTK 3, WebKit2-4.1,
// GLib/Gio and Soup-3.0 headers and GIR. Runtime loading needs no headers.
var n struct {
	gtkInitCheck                                                            func(uintptr, uintptr) int32
	gtkWindowNew                                                            func(int32) uintptr
	gtkWindowSetTitle                                                       func(uintptr, string)
	gtkWindowSetDefaultSize                                                 func(uintptr, int32, int32)
	gtkWindowResize                                                         func(uintptr, int32, int32)
	gtkWindowIconify, gtkWindowDeiconify                                    func(uintptr)
	gtkContainerAdd                                                         func(uintptr, uintptr)
	gtkWidgetShowAll, gtkWidgetHide, gtkWidgetDestroy, gtkWidgetGrabFocus   func(uintptr)
	gtkWidgetGetScaleFactor                                                 func(uintptr) int32
	gtkMain, gtkMainQuit                                                    func()
	gSignalConnectData                                                      func(uintptr, string, uintptr, uintptr, uintptr, int32) uint64
	gObjectRef                                                              func(uintptr) uintptr
	gObjectUnref                                                            func(uintptr)
	gIdleAddFull                                                            func(int32, uintptr, uintptr, uintptr) uint32
	gSourceRemove                                                           func(uint32) int32
	gBytesNew                                                               func(unsafe.Pointer, uintptr) uintptr
	gBytesGetData                                                           func(uintptr, *uintptr) unsafe.Pointer
	gBytesUnref                                                             func(uintptr)
	gMemoryInputStreamNewFromBytes                                          func(uintptr) uintptr
	gInputStreamReadBytesAsync                                              func(uintptr, uintptr, int32, uintptr, uintptr, uintptr)
	gInputStreamReadBytesFinish                                             func(uintptr, uintptr, *uintptr) uintptr
	gCancellableNew                                                         func() uintptr
	gCancellableCancel                                                      func(uintptr)
	gErrorFree                                                              func(uintptr)
	gFree                                                                   func(uintptr)
	webkitGetMajorVersion, webkitGetMinorVersion, webkitGetMicroVersion     func() uint32
	webkitWebContextNewEphemeral                                            func() uintptr
	webkitWebContextGetSecurityManager                                      func(uintptr) uintptr
	webkitSecurityManagerRegisterURISchemeAsSecure                          func(uintptr, string)
	webkitSecurityManagerRegisterURISchemeAsCorsEnabled                     func(uintptr, string)
	webkitWebContextRegisterURIScheme                                       func(uintptr, string, uintptr, uintptr, uintptr)
	webkitWebViewNewWithContext                                             func(uintptr) uintptr
	webkitWebViewGetSettings                                                func(uintptr) uintptr
	webkitSettingsSetEnableDeveloperExtras                                  func(uintptr, int32)
	webkitSettingsSetJavascriptCanOpenWindowsAutomatically                  func(uintptr, int32)
	webkitWebViewLoadURI                                                    func(uintptr, string)
	webkitWebViewStopLoading                                                func(uintptr)
	webkitWebViewGetURI                                                     func(uintptr) string
	webkitWebViewGetInspector                                               func(uintptr) uintptr
	webkitWebInspectorShow                                                  func(uintptr)
	webkitWebViewEvaluateJavascript                                         func(uintptr, string, int64, uintptr, uintptr, uintptr, uintptr, uintptr)
	webkitWebViewEvaluateJavascriptFinish                                   func(uintptr, uintptr, *uintptr) uintptr
	jscValueToString                                                        func(uintptr) uintptr
	webkitWebViewGetSnapshot                                                func(uintptr, int32, int32, uintptr, uintptr, uintptr)
	webkitWebViewGetSnapshotFinish                                          func(uintptr, uintptr, *uintptr) uintptr
	cairoSurfaceWriteToPNG                                                  func(uintptr, string) int32
	cairoSurfaceDestroy                                                     func(uintptr)
	webkitNavigationPolicyDecisionGetNavigationAction                       func(uintptr) uintptr
	webkitNavigationActionGetRequest                                        func(uintptr) uintptr
	webkitURIRequestGetURI                                                  func(uintptr) string
	webkitPolicyDecisionIgnore                                              func(uintptr)
	webkitURISchemeRequestGetURI, webkitURISchemeRequestGetHTTPMethod       func(uintptr) string
	webkitURISchemeRequestGetHTTPHeaders, webkitURISchemeRequestGetHTTPBody func(uintptr) uintptr
	webkitURISchemeRequestFinishWithResponse                                func(uintptr, uintptr)
	webkitURISchemeResponseNew                                              func(uintptr, int64) uintptr
	webkitURISchemeResponseSetStatus                                        func(uintptr, uint32, string)
	webkitURISchemeResponseSetContentType                                   func(uintptr, string)
	webkitURISchemeResponseSetHTTPHeaders                                   func(uintptr, uintptr)
	soupMessageHeadersNew                                                   func(int32) uintptr
	soupMessageHeadersAppend                                                func(uintptr, string, string)
	soupMessageHeadersIterInit                                              func(*[3]uintptr, uintptr)
	soupMessageHeadersIterNext                                              func(*[3]uintptr, *uintptr, *uintptr) int32
}

func loadNative() error {
	groups := []struct {
		name     string
		bindings map[string]any
	}{
		{"libgtk-3.so.0", map[string]any{
			"gtk_init_check": &n.gtkInitCheck, "gtk_window_new": &n.gtkWindowNew,
			"gtk_window_set_title": &n.gtkWindowSetTitle, "gtk_window_set_default_size": &n.gtkWindowSetDefaultSize,
			"gtk_window_resize": &n.gtkWindowResize, "gtk_window_iconify": &n.gtkWindowIconify, "gtk_window_deiconify": &n.gtkWindowDeiconify,
			"gtk_container_add": &n.gtkContainerAdd, "gtk_widget_show_all": &n.gtkWidgetShowAll,
			"gtk_widget_hide": &n.gtkWidgetHide, "gtk_widget_destroy": &n.gtkWidgetDestroy,
			"gtk_widget_grab_focus": &n.gtkWidgetGrabFocus, "gtk_widget_get_scale_factor": &n.gtkWidgetGetScaleFactor,
			"gtk_main": &n.gtkMain, "gtk_main_quit": &n.gtkMainQuit,
		}},
		{"libgobject-2.0.so.0", map[string]any{"g_signal_connect_data": &n.gSignalConnectData, "g_object_ref": &n.gObjectRef, "g_object_unref": &n.gObjectUnref}},
		{"libglib-2.0.so.0", map[string]any{"g_idle_add_full": &n.gIdleAddFull, "g_source_remove": &n.gSourceRemove,
			"g_bytes_new": &n.gBytesNew, "g_bytes_get_data": &n.gBytesGetData, "g_bytes_unref": &n.gBytesUnref,
			"g_error_free": &n.gErrorFree, "g_free": &n.gFree}},
		{"libgio-2.0.so.0", map[string]any{"g_memory_input_stream_new_from_bytes": &n.gMemoryInputStreamNewFromBytes,
			"g_input_stream_read_bytes_async": &n.gInputStreamReadBytesAsync, "g_input_stream_read_bytes_finish": &n.gInputStreamReadBytesFinish,
			"g_cancellable_new": &n.gCancellableNew, "g_cancellable_cancel": &n.gCancellableCancel}},
		{"libwebkit2gtk-4.1.so.0", map[string]any{
			"webkit_get_major_version": &n.webkitGetMajorVersion, "webkit_get_minor_version": &n.webkitGetMinorVersion, "webkit_get_micro_version": &n.webkitGetMicroVersion,
			"webkit_web_context_new_ephemeral":                              &n.webkitWebContextNewEphemeral,
			"webkit_web_context_get_security_manager":                       &n.webkitWebContextGetSecurityManager,
			"webkit_security_manager_register_uri_scheme_as_secure":         &n.webkitSecurityManagerRegisterURISchemeAsSecure,
			"webkit_security_manager_register_uri_scheme_as_cors_enabled":   &n.webkitSecurityManagerRegisterURISchemeAsCorsEnabled,
			"webkit_web_context_register_uri_scheme":                        &n.webkitWebContextRegisterURIScheme,
			"webkit_web_view_new_with_context":                              &n.webkitWebViewNewWithContext,
			"webkit_web_view_get_settings":                                  &n.webkitWebViewGetSettings,
			"webkit_settings_set_enable_developer_extras":                   &n.webkitSettingsSetEnableDeveloperExtras,
			"webkit_settings_set_javascript_can_open_windows_automatically": &n.webkitSettingsSetJavascriptCanOpenWindowsAutomatically,
			"webkit_web_view_load_uri":                                      &n.webkitWebViewLoadURI, "webkit_web_view_stop_loading": &n.webkitWebViewStopLoading,
			"webkit_web_view_get_uri": &n.webkitWebViewGetURI, "webkit_web_view_get_inspector": &n.webkitWebViewGetInspector,
			"webkit_web_inspector_show":                  &n.webkitWebInspectorShow,
			"webkit_web_view_evaluate_javascript":        &n.webkitWebViewEvaluateJavascript,
			"webkit_web_view_evaluate_javascript_finish": &n.webkitWebViewEvaluateJavascriptFinish,
			"webkit_web_view_get_snapshot":               &n.webkitWebViewGetSnapshot, "webkit_web_view_get_snapshot_finish": &n.webkitWebViewGetSnapshotFinish,
			"webkit_navigation_policy_decision_get_navigation_action": &n.webkitNavigationPolicyDecisionGetNavigationAction,
			"webkit_navigation_action_get_request":                    &n.webkitNavigationActionGetRequest, "webkit_uri_request_get_uri": &n.webkitURIRequestGetURI,
			"webkit_policy_decision_ignore":                  &n.webkitPolicyDecisionIgnore,
			"webkit_uri_scheme_request_get_uri":              &n.webkitURISchemeRequestGetURI,
			"webkit_uri_scheme_request_get_http_method":      &n.webkitURISchemeRequestGetHTTPMethod,
			"webkit_uri_scheme_request_get_http_headers":     &n.webkitURISchemeRequestGetHTTPHeaders,
			"webkit_uri_scheme_request_get_http_body":        &n.webkitURISchemeRequestGetHTTPBody,
			"webkit_uri_scheme_request_finish_with_response": &n.webkitURISchemeRequestFinishWithResponse,
			"webkit_uri_scheme_response_new":                 &n.webkitURISchemeResponseNew,
			"webkit_uri_scheme_response_set_status":          &n.webkitURISchemeResponseSetStatus,
			"webkit_uri_scheme_response_set_content_type":    &n.webkitURISchemeResponseSetContentType,
			"webkit_uri_scheme_response_set_http_headers":    &n.webkitURISchemeResponseSetHTTPHeaders,
		}},
		{"libjavascriptcoregtk-4.1.so.0", map[string]any{"jsc_value_to_string": &n.jscValueToString}},
		{"libsoup-3.0.so.0", map[string]any{"soup_message_headers_new": &n.soupMessageHeadersNew, "soup_message_headers_append": &n.soupMessageHeadersAppend,
			"soup_message_headers_iter_init": &n.soupMessageHeadersIterInit, "soup_message_headers_iter_next": &n.soupMessageHeadersIterNext}},
		{"libcairo.so.2", map[string]any{"cairo_surface_write_to_png": &n.cairoSurfaceWriteToPNG, "cairo_surface_destroy": &n.cairoSurfaceDestroy}},
	}
	for _, group := range groups {
		h, err := purego.Dlopen(group.name, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			return fmt.Errorf("load %s (GTK 3/WebKitGTK 4.1 >=2.40 required): %w", group.name, err)
		}
		for name, dest := range group.bindings {
			addr, err := purego.Dlsym(h, name)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			purego.RegisterFunc(dest, addr)
		}
	}
	return nil
}

// Native strings are borrowed only during this copy. unsafe.Add avoids
// hiding arithmetic on pointers inside uintptr conversions.
func cstring(p uintptr) string {
	if p == 0 {
		return ""
	}
	ptr := unsafe.Add(unsafe.Pointer(nil), p)
	var b []byte
	for i := 0; ; i++ {
		c := *(*byte)(unsafe.Add(ptr, i))
		if c == 0 {
			break
		}
		b = append(b, c)
	}
	return string(b)
}

func nativeError(p uintptr) error {
	if p == 0 {
		return nil
	}
	// GError: GQuark (uint32), gint (int32), gchar *message.
	g := (*struct {
		domain  uint32
		code    int32
		message uintptr
	})(unsafe.Add(unsafe.Pointer(nil), p))
	err := fmt.Errorf("GLib error %d/%d: %s", g.domain, g.code, cstring(g.message))
	n.gErrorFree(p)
	return err
}
