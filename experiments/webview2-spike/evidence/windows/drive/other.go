//go:build !windows

// Command drive is the Windows audition driver; elsewhere it only says so.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "drive: the audition driver runs only on Windows")
	os.Exit(2)
}
