//go:build !linux && !windows

package main

import "time"

// cpuTime is unavailable here; Run fails earlier on unsupported targets.
func cpuTime() time.Duration { return 0 }
