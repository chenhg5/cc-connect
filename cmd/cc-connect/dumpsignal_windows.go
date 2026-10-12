//go:build windows

package main

// handleDumpSignal is a no-op on Windows (no SIGUSR1).
func handleDumpSignal() {}
