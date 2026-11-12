//go:build linux

package main

// SIGXCPU in the VM-free host. WebKitGTK 2.52.6 replaces Go's SIGXCPU
// handler once any SCHED_RR WTF thread exists (RealTimeThreads), which every
// auxiliary-process connection creates. 2.54.1 removed RealTimeThreads. With
// -feas-xcpu the host subscribes to SIGXCPU through os/signal before WebKit
// initializes, so a later replacement shows up as lost delivery, and records
// thread scheduling and resource limits. Probes only: nothing here changes
// a disposition.

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type schedThread struct {
	TID    int    `json:"tid"`
	Name   string `json:"name"`
	Policy string `json:"policy"`
	RTPrio int    `json:"rt_priority,omitempty"`
	Nice   int    `json:"nice,omitempty"`
	// UclampMin is the utilization floor (DVFS), 0-1024, from
	// /proc/self/task/TID/sched; -1 when the kernel does not report it.
	UclampMin int `json:"uclamp_min,omitempty"`
}

var policyNames = map[int]string{0: "OTHER", 1: "FIFO", 2: "RR", 3: "BATCH", 5: "IDLE", 6: "DEADLINE"}

// threads reads every task's policy, real-time priority and nice level.
func schedThreads() []schedThread {
	dir := "/proc/self/task"
	entries, _ := os.ReadDir(dir)
	var out []schedThread
	for _, e := range entries {
		b, err := os.ReadFile(dir + "/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		s := string(b)
		open, close := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
		if open < 0 || close < open {
			continue
		}
		f := strings.Fields(s[close+1:]) // f[0] is field 3, state
		if len(f) < 39 {
			continue
		}
		tid, _ := strconv.Atoi(e.Name())
		nice, _ := strconv.Atoi(f[16])
		rt, _ := strconv.Atoi(f[37])
		pol, _ := strconv.Atoi(f[38])
		name := policyNames[pol&0xff]
		if name == "" {
			name = strconv.Itoa(pol)
		}
		out = append(out, schedThread{TID: tid, Name: s[open+1 : close], Policy: name, RTPrio: rt, Nice: nice, UclampMin: uclampMin(dir + "/" + e.Name())})
	}
	return out
}

func uclampMin(task string) int {
	b, err := os.ReadFile(task + "/sched")
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "uclamp.min" {
			n, _ := strconv.Atoi(strings.TrimSpace(v))
			return n
		}
	}
	return -1
}

// schedView summarizes: thread count, and every thread not at SCHED_OTHER
// with nice 0 and no utilization floor.
func schedView() map[string]any {
	all := schedThreads()
	var special []schedThread
	for _, t := range all {
		if t.Policy != "OTHER" || t.Nice != 0 || t.UclampMin > 0 {
			special = append(special, t)
		}
	}
	return map[string]any{"threads": len(all), "not_default": special}
}

// limitsView reads the limits that real-time and high-priority thread
// support touch.
func limitsView() map[string]string {
	f, err := os.Open("/proc/self/limits")
	if err != nil {
		return nil
	}
	defer f.Close()
	out := map[string]string{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		for _, name := range []string{"Max cpu time", "Max nice priority", "Max realtime priority", "Max realtime timeout"} {
			if strings.HasPrefix(line, name) {
				out[name] = strings.Join(strings.Fields(line[len(name):]), " ")
			}
		}
	}
	return out
}

// nativeThread picks a thread the Go runtime did not create: Go threads keep
// the process's name, WTF and GLib threads set their own.
func nativeThread() (int, string) {
	comm, _ := os.ReadFile("/proc/self/comm")
	self := strings.TrimSpace(string(comm))
	var fallback schedThread
	for _, t := range schedThreads() {
		if t.Name == self {
			continue
		}
		if strings.Contains(t.Name, "IPC") || t.Policy == "RR" {
			return t.TID, t.Name
		}
		if fallback.TID == 0 {
			fallback = t
		}
	}
	return fallback.TID, fallback.Name
}

// xcpuProbe waits for SIGXCPU on the Go subscription. mode "native" sends
// it to one native thread with tgkill; "external" only arms, and the driver
// sends a process-directed signal from outside. The wait runs off the main
// loop, so WebKit's eventfd callback (and its demotion) can still run.
func (f *feas) xcpuProbe(mode string) {
	if f.xcpu == nil {
		f.event("error", "command", "xcpu", "error", "needs -feas-xcpu")
		return
	}
	for len(f.xcpu) > 0 {
		<-f.xcpu
	}
	before := map[string]any{"disposition": signalView(f.sigs, f.c.reserve)["SIGXCPU"], "sched": schedView()}
	target, name := 0, ""
	if mode == "native" {
		target, name = nativeThread()
	}
	f.event("xcpu-armed", "mode", mode, "target_tid", target, "target_name", name)
	go func() {
		if mode == "native" && target != 0 {
			syscall.Tgkill(os.Getpid(), target, syscall.SIGXCPU)
		}
		received := false
		select {
		case <-f.xcpu:
			received = true
		case <-time.After(2 * time.Second):
		}
		f.post.post(func() {
			f.event("xcpu", "mode", mode, "target_tid", target, "target_name", name, "received_by_go", received, "before", before,
				"after", map[string]any{"disposition": signalView(f.sigs, f.c.reserve)["SIGXCPU"], "sched": schedView()})
		})
	}()
}

func xcpuRecord() map[string]any {
	return map[string]any{"sched": schedView(), "limits": limitsView(), "at": fmt.Sprint(time.Now().Format("15:04:05.000"))}
}
