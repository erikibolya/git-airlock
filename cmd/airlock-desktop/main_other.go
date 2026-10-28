//go:build !windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "airlock-desktop is Windows-only; use `airlock ui` on this platform.")
	os.Exit(1)
}
