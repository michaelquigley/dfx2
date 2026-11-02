//go:build !linux && !windows

package main

import "errors"

func runWindow(*app, string, int, int) error {
	return errors.New("the spike's window is Linux and Windows only; use -png")
}
