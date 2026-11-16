//go:build !windows

// The body-probe driver drives real Windows processes and windows; it
// builds elsewhere only so go vet ./... covers the module.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "the body-probe driver runs only on Windows")
	os.Exit(2)
}
