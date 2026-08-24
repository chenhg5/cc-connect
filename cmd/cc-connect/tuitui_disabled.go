//go:build no_tuitui

package main

import (
	"fmt"
	"os"
)

func runTuiTui(args []string) {
	fmt.Fprintln(os.Stderr, "Error: TuiTui support was excluded from this build (no_tuitui).")
	os.Exit(1)
}
