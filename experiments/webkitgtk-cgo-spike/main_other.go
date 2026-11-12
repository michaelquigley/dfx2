//go:build !(linux && cgo)

// Package main is the cgo comparison host; it needs Linux and CGO_ENABLED=1.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "this experiment needs Linux and CGO_ENABLED=1")
	os.Exit(1)
}
