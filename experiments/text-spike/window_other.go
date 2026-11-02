//go:build !linux

package main

import "errors"

func runWindow(*app, string, int, int) error {
	return errors.New("the spike's window is Linux only; use -png")
}
