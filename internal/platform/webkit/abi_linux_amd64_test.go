package webkit

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// Compare every public binding with the installed development headers. It
// compiles no C and calls nothing native. JSConfigureSignalForGC is private
// (absent from the headers) and libc's real-time range functions are glibc
// internals; both are bound by name only.
func TestBindingsMatchHeaders(t *testing.T) {
	var files []string
	for _, pattern := range []string{"/usr/include/webkitgtk-4.1/webkit/*.h", "/usr/include/libsoup-3.0/libsoup/*.h"} {
		matches, _ := filepath.Glob(pattern)
		files = append(files, matches...)
	}
	files = append(files, "/usr/include/glib-2.0/glib/gmain.h", "/usr/include/glib-2.0/glib/gmem.h", "/usr/include/glib-2.0/glib/gbytes.h",
		"/usr/include/glib-2.0/glib/gerror.h", "/usr/include/glib-2.0/glib/gquark.h", "/usr/include/glib-2.0/gobject/gobject.h",
		"/usr/include/glib-2.0/gobject/gsignal.h", "/usr/include/glib-2.0/gio/gmemoryinputstream.h", "/usr/include/glib-2.0/gio/ginputstream.h",
		"/usr/include/glib-2.0/gio/gcancellable.h", "/usr/include/gtk-3.0/gtk/gtkcontainer.h", "/usr/include/gtk-3.0/gtk/gtkwidget.h")
	var corpus strings.Builder
	for _, path := range files {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			t.Skip("native development headers absent:", path)
		}
		if err != nil {
			t.Fatal(err)
		}
		corpus.Write(data)
	}
	if len(files) < 20 {
		t.Skip("WebKitGTK 4.1 or libsoup 3 development headers absent")
	}
	bindings := map[string]any{
		"g_object_ref": w.ref, "g_object_ref_sink": w.refSink, "g_object_unref": w.unref,
		"g_signal_connect_data": w.connect, "g_signal_handler_disconnect": w.disconnect, "g_idle_add_full": w.idleAdd,
		"g_malloc": w.malloc, "g_free": w.free, "g_bytes_new": w.bytesNew, "g_bytes_new_with_free_func": w.bytesNewWithFree,
		"g_bytes_get_data": w.bytesData, "g_bytes_unref": w.bytesUnref, "g_memory_input_stream_new_from_bytes": w.memoryStream,
		"g_input_stream_read_bytes_async": w.readBytesAsync, "g_input_stream_read_bytes_finish": w.readBytesFinish,
		"g_cancellable_new": w.cancellableNew, "g_cancellable_cancel": w.cancellableCancel,
		"g_error_new_literal": w.errorNewLiteral, "g_error_free": w.errorFree, "g_quark_from_string": w.quarkFromString,
		"g_quark_to_string": w.quarkToString, "gtk_container_add": w.containerAdd, "gtk_widget_show": w.widgetShow,
		"gtk_widget_destroy": w.widgetDestroy, "soup_message_headers_new": w.headersNew, "soup_message_headers_append": w.headersAppend,
		"soup_message_headers_iter_init": w.headersIterInit, "soup_message_headers_iter_next": w.headersIterNext,
		"soup_cookie_new": w.cookieNew, "soup_cookie_set_http_only": w.cookieSetHTTPOnly,
		"soup_cookie_set_same_site_policy": w.cookieSetSameSite, "soup_cookie_free": w.cookieFree,
		"webkit_get_major_version": w.majorVersion, "webkit_get_minor_version": w.minorVersion, "webkit_get_micro_version": w.microVersion,
		"webkit_web_context_new_ephemeral": w.contextNewEphemeral, "webkit_web_context_get_security_manager": w.securityManager,
		"webkit_security_manager_register_uri_scheme_as_secure":       w.schemeAsSecure,
		"webkit_security_manager_register_uri_scheme_as_cors_enabled": w.schemeAsCORS,
		"webkit_web_context_register_uri_scheme":                      w.registerScheme,
		"webkit_web_context_get_website_data_manager":                 w.dataManager,
		"webkit_website_data_manager_get_cookie_manager":              w.cookieManager,
		"webkit_cookie_manager_add_cookie":                            w.addCookie, "webkit_cookie_manager_add_cookie_finish": w.addCookieFinish,
		"webkit_web_view_new_with_context": w.viewNew, "webkit_web_view_get_settings": w.viewSettings,
		"webkit_settings_set_enable_developer_extras":                   w.setDeveloperExtras,
		"webkit_settings_set_javascript_can_open_windows_automatically": w.setOpenWindows,
		"webkit_web_view_load_uri":                                      w.loadURI, "webkit_web_view_stop_loading": w.stopLoading,
		"webkit_web_view_load_alternate_html": w.loadAlternateHTML, "webkit_web_view_get_uri": w.viewURI,
		"webkit_web_view_get_title": w.viewTitle, "webkit_web_view_get_user_content_manager": w.viewContentManager,
		"webkit_user_content_manager_add_filter": w.addFilter, "webkit_user_content_filter_store_new": w.filterStoreNew,
		"webkit_user_content_filter_store_save": w.filterStoreSave, "webkit_user_content_filter_store_save_finish": w.filterStoreFinish,
		"webkit_user_content_filter_unref":                        w.filterUnref,
		"webkit_navigation_policy_decision_get_navigation_action": w.navigationAction,
		"webkit_navigation_action_get_request":                    w.actionRequest, "webkit_navigation_action_get_navigation_type": w.actionType,
		"webkit_navigation_action_is_redirect": w.actionIsRedirect, "webkit_uri_request_get_uri": w.requestURI,
		"webkit_uri_request_get_http_method": w.requestMethod, "webkit_policy_decision_ignore": w.policyIgnore,
		"webkit_response_policy_decision_is_mime_type_supported": w.mimeSupported,
		"webkit_permission_request_deny":                         w.permissionDeny, "webkit_download_cancel": w.downloadCancel,
		"webkit_uri_scheme_request_get_uri": w.schemeURI, "webkit_uri_scheme_request_get_http_method": w.schemeMethod,
		"webkit_uri_scheme_request_get_http_headers": w.schemeHeaders, "webkit_uri_scheme_request_get_http_body": w.schemeBody,
		"webkit_uri_scheme_request_finish_with_response": w.schemeFinish, "webkit_uri_scheme_request_finish_error": w.schemeFinishError,
		"webkit_uri_scheme_response_new": w.responseNew, "webkit_uri_scheme_response_set_status": w.responseStatus,
		"webkit_uri_scheme_response_set_content_type": w.responseContentType,
		"webkit_uri_scheme_response_set_http_headers": w.responseHeaders,
	}
	// A parenthesized name, as in "void (g_free) (gpointer mem);", keeps a
	// function-like macro of the same name from expanding.
	stripName := regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*\s*$`)
	text := corpus.String()
	for name, binding := range bindings {
		pattern := regexp.MustCompile(`(?s)([A-Za-z_][A-Za-z0-9_]*\s*\**)\s*\(?` + name + `\)?\s*\(([^();]*)\)\s*(G_GNUC_[A-Z_]+(\([0-9, ]*\))?\s*)*;`)
		match := pattern.FindStringSubmatch(text)
		if match == nil {
			t.Errorf("declaration not found: %s", name)
			continue
		}
		typ := reflect.TypeOf(binding)
		if strings.TrimSpace(match[1]) == "void" {
			if typ.NumOut() != 0 {
				t.Errorf("%s: void return bound as %v", name, typ)
			}
		} else if typ.NumOut() != 1 || !matchesC(match[1], typ.Out(0).Kind()) {
			t.Errorf("%s: return %s bound as %v", name, match[1], typ)
		}
		parameters := strings.Split(match[2], ",")
		if strings.TrimSpace(match[2]) == "void" {
			parameters = nil
		}
		if typ.NumIn() != len(parameters) {
			t.Errorf("%s: parameter count: header %d Go %d", name, len(parameters), typ.NumIn())
			continue
		}
		for i, parameter := range parameters {
			ctype := strings.TrimSpace(stripName.ReplaceAllString(parameter, ""))
			if !matchesC(ctype, typ.In(i).Kind()) {
				t.Errorf("%s parameter %d: %s bound as %v", name, i, ctype, typ.In(i))
			}
		}
	}
}

func matchesC(ctype string, kind reflect.Kind) bool {
	ctype = strings.Join(strings.Fields(strings.ReplaceAll(ctype, "const ", "")), " ")
	if strings.Contains(ctype, "*") {
		return kind == reflect.Uintptr || kind == reflect.UnsafePointer || kind == reflect.Pointer ||
			(kind == reflect.String && strings.Contains(ctype, "char") && !strings.Contains(ctype, "**"))
	}
	switch ctype {
	case "int", "gint", "gboolean", "GConnectFlags", "SoupMessageHeadersType", "SoupSameSitePolicy", "WebKitNavigationType":
		return kind == reflect.Int32
	case "guint", "guint32", "GQuark":
		return kind == reflect.Uint32
	case "gulong":
		return kind == reflect.Uint64
	case "gint64", "goffset":
		return kind == reflect.Int64
	case "gsize":
		return kind == reflect.Uintptr
	case "gconstpointer":
		return kind == reflect.Uintptr || kind == reflect.UnsafePointer
	case "gpointer", "GCallback", "GSourceFunc", "GDestroyNotify", "GClosureNotify", "GAsyncReadyCallback",
		"WebKitURISchemeRequestCallback":
		return kind == reflect.Uintptr
	}
	return false
}
