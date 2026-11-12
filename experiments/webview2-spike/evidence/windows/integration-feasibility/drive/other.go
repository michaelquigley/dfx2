//go:build !windows

// The feasibility driver drives real Windows processes and windows; it
// builds elsewhere only so go vet ./... covers the module.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "the feasibility driver runs only on Windows")
	os.Exit(2)
}
