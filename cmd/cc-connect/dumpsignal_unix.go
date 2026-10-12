//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/chenhg5/cc-connect/core"
)

// handleDumpSignal logs all goroutine stacks on SIGUSR1 without stopping (maomao fork).
func handleDumpSignal() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR1)
	go func() {
		for range ch {
			core.DumpGoroutines("SIGUSR1")
		}
	}()
}
