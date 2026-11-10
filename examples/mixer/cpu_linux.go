package main

import (
	"syscall"
	"time"
)

// cpuTime is this process's user plus system CPU time, for timing observations.
func cpuTime() time.Duration {
	var u syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &u) != nil {
		return 0
	}
	return time.Duration(u.Utime.Nano() + u.Stime.Nano())
}
