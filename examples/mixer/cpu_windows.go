package main

import (
	"syscall"
	"time"
)

// cpuTime is this process's user plus kernel CPU time, for timing observations.
func cpuTime() time.Duration {
	var creation, exit, kernel, user syscall.Filetime
	h, err := syscall.GetCurrentProcess()
	if err != nil || syscall.GetProcessTimes(h, &creation, &exit, &kernel, &user) != nil {
		return 0
	}
	ticks := func(f syscall.Filetime) int64 { return int64(f.HighDateTime)<<32 | int64(f.LowDateTime) }
	return time.Duration(ticks(kernel)+ticks(user)) * 100 // 100 ns units
}
