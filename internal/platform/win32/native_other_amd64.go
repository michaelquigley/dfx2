//go:build !windows

package win32

import "errors"

func systemAPI() (*native, error) { return nil, errors.New("Win32 native calls require Windows/amd64") }
