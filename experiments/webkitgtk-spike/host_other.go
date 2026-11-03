//go:build !linux

package main

import "errors"

func runHost(config) error { return errors.New("this experiment's native host is Linux only") }
