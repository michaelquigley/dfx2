//go:build windows

// Command stall deliberately leaves a parent and child alive with inherited
// output handles. It is used only by timeout-test.ps1, never by the runtime.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "child" {
		fmt.Fprintln(os.Stderr, "stalled child holds inherited stderr")
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"event": "stalled-child", "pid": os.Getpid(),
		})
	} else {
		self, err := os.Executable()
		if err != nil {
			panic(err)
		}
		child := exec.Command(self, "child")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			panic(err)
		}
	}
	for {
		time.Sleep(time.Hour)
	}
}
