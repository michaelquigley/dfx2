//go:build windows

// Command ctrlbreak runs the lifetime fixture in its own process group on this
// console, waits for applied title updates on both windows, then delivers a
// CTRL_BREAK_EVENT to that group through the console subsystem. It relays the
// fixture's stdout/stderr and exits with the fixture's exit code.
//
// The event comes from GenerateConsoleCtrlEvent, not from a keyboard. It
// exercises the console-to-process delivery that Go maps to os.Interrupt; it
// does not prove terminal input, console close, logoff or shutdown behavior.
//
// A separate module, like the experiments, so the root module never builds
// it. Run it from the audition script, which gives it a console; without one,
// GenerateConsoleCtrlEvent fails and the fixture is killed and reported.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

const ctrlBreakEvent = 1

func main() {
	os.Exit(run())
}

func run() int {
	fixture := flag.String("fixture", "", "path to the lifetime fixture executable")
	updates := flag.Int("updates", 3, "applied title updates per window before signaling")
	flag.Parse()
	if *fixture == "" {
		fmt.Fprintln(os.Stderr, "ctrlbreak: -fixture is required")
		return 2
	}
	// The event targets the fixture's group only; this process is not in it.
	// Ignore interrupts anyway so a stray event cannot end the relay early.
	signal.Ignore(os.Interrupt)

	cmd := exec.Command(*fixture, flag.Args()...)
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ctrlbreak:", err)
		return 2
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "ctrlbreak:", err)
		return 2
	}
	pid := cmd.Process.Pid

	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	emit := func(event string, fields map[string]any) {
		line := map[string]any{"event": event, "time": time.Now().Format(time.RFC3339Nano), "pid": pid}
		for k, v := range fields {
			line[k] = v
		}
		b, _ := json.Marshal(line)
		out.Write(b)
		out.WriteByte('\n')
		out.Flush()
	}

	applied := map[string]int{}
	sent := false
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		out.Write(line)
		out.WriteByte('\n')
		out.Flush()
		if sent {
			continue
		}
		var ev struct {
			Event  string `json:"event"`
			Window string `json:"window"`
		}
		if json.Unmarshal(line, &ev) != nil || ev.Event != "update" {
			continue
		}
		applied[ev.Window]++
		if applied["A"] < *updates || applied["B"] < *updates {
			continue
		}
		sent = true
		r, _, callErr := syscall.NewLazyDLL("kernel32.dll").NewProc("GenerateConsoleCtrlEvent").Call(ctrlBreakEvent, uintptr(pid))
		if r == 0 {
			emit("ctrl-break-failed", map[string]any{"error": callErr.Error()})
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return 3
		}
		emit("ctrl-break-sent", map[string]any{"applied_a": applied["A"], "applied_b": applied["B"]})
	}
	err = cmd.Wait()
	if !sent {
		emit("fixture-exited-before-signal", map[string]any{"error": fmt.Sprint(err)})
		return 3
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ctrlbreak:", err)
		return 2
	}
	return 0
}
