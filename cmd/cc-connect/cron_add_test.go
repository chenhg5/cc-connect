package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

// TestPrintCronAddUsage_DocumentsSilent is a help-text guard for the
// `cc-connect cron add --silent` flag. The flag is parsed in runCronAdd
// (cmd/cc-connect/cron.go) and sets body["silent"] = true on the /cron/add
// request; if printCronAddUsage ever drifts and stops mentioning it, users
// who run `cc-connect cron add --help` will not discover the feature even
// though it still works.
//
// This complements TestParseCronEditValue (cron_edit_test.go), which guards
// the symmetric `cron edit <id> silent` path. The behavioural round-trip
// (CLI flag → request body → server → CronJob.Silent) is covered by the
// CronAddRequest / CronJob tests in core/api_test.go and core/cron_test.go.
func TestPrintCronAddUsage_DocumentsSilent(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	printCronAddUsage()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	help := string(out)

	if !strings.Contains(help, "--silent") {
		t.Errorf("printCronAddUsage does not document --silent; got:\n%s", help)
	}
	if !strings.Contains(help, "Suppress cron start notification") {
		t.Errorf("printCronAddUsage does not describe --silent; got:\n%s", help)
	}
	if !strings.Contains(help, "cc-connect cron add --cron \"0 9 * * *\" --prompt \"Daily standup reminder\" --silent") {
		t.Errorf("printCronAddUsage does not include --silent example; got:\n%s", help)
	}
}