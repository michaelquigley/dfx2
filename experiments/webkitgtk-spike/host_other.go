//go:build !linux

package main

import "errors"

func runHost(config) error { return errors.New("this experiment's native host is Linux only") }

func runSignalProbe(string, string, bool, string, int) error {
	return errors.New("the signal probe is Linux only")
}

type feasConfig struct {
	filter, csp, cookie, cookieFail bool
	root                            string
	vmfree                          bool
	reserve                         int
	xcpu                            bool
}

func runFeasibility(feasConfig) error { return errors.New("the feasibility host is Linux only") }

func runReservationProbe(string, int) error { return errors.New("the signal probe is Linux only") }

func runHandlerSafetyProbe(string, int, string, bool, string) error {
	return errors.New("the signal probe is Linux only")
}
