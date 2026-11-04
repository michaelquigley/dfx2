package win32

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
	"unsafe"
)

// Offsets follow the Windows SDK WinUser.h declarations under the Windows x64
// LLP64 ABI. Running here checks Go layout, not native ABI execution. The same
// test must execute on Windows before accepting the backend.
func TestWindowsX64Layouts(t *testing.T) {
	for _, c := range []struct {
		name      string
		got, want uintptr
	}{
		{"WNDCLASSEXW.sizeof", unsafe.Sizeof(classEx{}), 80},
		{"WNDCLASSEXW.lpfnWndProc", unsafe.Offsetof(classEx{}.procedure), 8},
		{"WNDCLASSEXW.hInstance", unsafe.Offsetof(classEx{}.instance), 24},
		{"WNDCLASSEXW.lpszClassName", unsafe.Offsetof(classEx{}.className), 64},
		{"WNDCLASSEXW.hIconSm", unsafe.Offsetof(classEx{}.smallIcon), 72},
		{"MSG.sizeof", unsafe.Sizeof(message{}), 48},
		{"MSG.wParam", unsafe.Offsetof(message{}.wparam), 16},
		{"MSG.lParam", unsafe.Offsetof(message{}.lparam), 24},
		{"MSG.time", unsafe.Offsetof(message{}.time), 32},
		{"MSG.pt", unsafe.Offsetof(message{}.position), 36},
		{"MSG.lPrivate", unsafe.Offsetof(message{}.private), 44},
		{"CREATESTRUCTW.sizeof", unsafe.Sizeof(createStruct{}), 80},
		{"CREATESTRUCTW.lpCreateParams", unsafe.Offsetof(createStruct{}.params), 0},
		{"CREATESTRUCTW.style", unsafe.Offsetof(createStruct{}.style), 48},
		{"CREATESTRUCTW.lpszName", unsafe.Offsetof(createStruct{}.name), 56},
		{"CREATESTRUCTW.dwExStyle", unsafe.Offsetof(createStruct{}.extendedStyle), 72},
		{"RECT.sizeof", unsafe.Sizeof(rect{}), 16},
		{"POINT.sizeof", unsafe.Sizeof(point{}), 8},
	} {
		if c.got != c.want {
			t.Errorf("%s=%d want %d", c.name, c.got, c.want)
		}
	}
}

// syscall.Proc.Call is untyped. Check its arity against the SDK declarations,
// even during a Linux preflight when the Windows bridge cannot execute.
func TestSyscallArgumentCounts(t *testing.T) {
	expected := map[string]int{
		"RegisterClassExW": 1, "UnregisterClassW": 2, "CreateWindowExW": 12,
		"DestroyWindow": 1, "DefWindowProcW": 4, "GetWindowLongPtrW": 2,
		"SetWindowLongPtrW": 3, "GetMessageW": 4, "PeekMessageW": 5,
		"TranslateMessage": 1, "DispatchMessageW": 1, "PostMessageW": 4,
		"SetWindowTextW": 2, "GetDpiForWindow": 1, "AdjustWindowRectExForDpi": 5,
		"SetWindowPos": 7, "ShowWindow": 2, "LoadCursorW": 2, "GetClientRect": 2,
		"GetCurrentThreadId": 0, "GetModuleHandleW": 1, "SetLastError": 1,
		"RtlMoveMemory": 3, "SetProcessDpiAwarenessContext": 1,
		"GetThreadDpiAwarenessContext": 0, "GetAwarenessFromDpiAwarenessContext": 1,
	}
	file, err := parser.ParseFile(token.NewFileSet(), "native_windows_amd64.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Call" {
			return true
		}
		index, ok := selector.X.(*ast.IndexExpr)
		if !ok {
			return true
		}
		literal, ok := index.Index.(*ast.BasicLit)
		if !ok {
			return true
		}
		name, err := strconv.Unquote(literal.Value)
		if err != nil {
			t.Error(err)
			return true
		}
		want, known := expected[name]
		if !known {
			t.Errorf("SDK declaration check missing for %s", name)
			return true
		}
		seen[name] = true
		if len(call.Args) != want {
			t.Errorf("%s: %d arguments, SDK declares %d", name, len(call.Args), want)
		}
		return true
	})
	for name := range expected {
		if !seen[name] {
			t.Errorf("SDK signature check has no bridge call for %s", name)
		}
	}
}
