package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestPrintCronUsage_IncludesExec(t *testing.T) {
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	t.Cleanup(func() {
		os.Stdout = oldStdout
	})

	printCronUsage()
	_ = w.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if !strings.Contains(buf.String(), "exec <id>") {
		t.Fatalf("usage = %q, want exec subcommand", buf.String())
	}
}
