package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

// TestParseCronEditValue covers the value-coercion that runCronEdit applies
// before sending the JSON body to the management API. The server's
// updateJobField type-switch (core/cron.go) requires bools as JSON booleans
// and timeout_mins as a JSON number — sending a string falls through to a
// reflection-based string-field setter that doesn't apply to *bool / *int,
// and the user gets back "unknown or invalid field: <field>".
//
// Pre-fix bug: `silent` was missing from the bool case, so
// `cc-connect cron edit <id> silent true` shipped {"value": "true"} and the
// server rejected it. printCronEditUsage already documents `silent` as a
// bool, so the CLI parsing has to match.
func TestParseCronEditValue(t *testing.T) {
	tests := []struct {
		name      string
		field     string
		input     string
		want      any
		wantError bool
	}{
		// Bool fields — all three must round-trip as bool, not string.
		{"enabled true", "enabled", "true", true, false},
		{"enabled false", "enabled", "false", false, false},
		{"mute true", "mute", "true", true, false},
		{"silent true (regression)", "silent", "true", true, false},
		{"silent false (regression)", "silent", "false", false, false},
		{"silent invalid", "silent", "yes please", nil, true},

		// Numeric field.
		{"timeout_mins zero", "timeout_mins", "0", 0, false},
		{"timeout_mins positive", "timeout_mins", "60", 60, false},
		{"timeout_mins invalid", "timeout_mins", "soon", nil, true},

		// String fields fall through unchanged.
		{"cron_expr string", "cron_expr", "0 6 * * *", "0 6 * * *", false},
		{"description string", "description", "Daily standup", "Daily standup", false},
		{"session_mode string", "session_mode", "reuse", "reuse", false},
		{"model string", "model", "haiku", "haiku", false},
		{"model empty clears", "model", "", "", false},
		{"unknown field defaults to string", "made_up_field", "anything", "anything", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCronEditValue(tt.field, tt.input)
			if tt.wantError {
				if err == nil {
					t.Fatalf("parseCronEditValue(%q, %q) = (%v, nil), want error", tt.field, tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCronEditValue(%q, %q) returned unexpected error: %v", tt.field, tt.input, err)
			}
			if got != tt.want {
				t.Errorf("parseCronEditValue(%q, %q) = %v (%T), want %v (%T)",
					tt.field, tt.input, got, got, tt.want, tt.want)
			}
		})
	}
}

// TestPrintCronEditUsage_DocumentsModel guards that the editable model field
// (handled by core updateJobField) is discoverable, including how to clear it.
func TestPrintCronEditUsage_DocumentsModel(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	printCronEditUsage()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	help := string(out)
	for _, want := range []string{"  model ", `cron edit abc123 model ""`} {
		if !strings.Contains(help, want) {
			t.Errorf("printCronEditUsage missing %q; got:\n%s", want, help)
		}
	}
}
