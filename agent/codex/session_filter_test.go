package codex

import (
	"reflect"
	"testing"
)

// TestNew_ParsesSessionFilterIgnorePrefixes verifies that
// [projects.agent.options.session_filter] ignore_summary_prefixes is loaded
// into Agent.ignoreSummaryPrefixes. This is the TOML config path that lets
// users extend the built-in default ignore list with their own internal
// prompt prefixes. See #1271.
func TestNew_ParsesSessionFilterIgnorePrefixes(t *testing.T) {
	opts := map[string]any{
		"work_dir": t.TempDir(),
		"cli_path": "go",
		"session_filter": map[string]any{
			"ignore_summary_prefixes": []any{
				"[bot-scheduler]",
				"You are the scheduled task runner",
			},
		},
	}

	a, err := New(opts)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	agent := a.(*Agent)
	agent.mu.RLock()
	got := append([]string(nil), agent.ignoreSummaryPrefixes...)
	agent.mu.RUnlock()

	want := []string{"[bot-scheduler]", "You are the scheduled task runner"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ignoreSummaryPrefixes = %v, want %v", got, want)
	}
}

// TestNew_ParsesSessionFilterIgnorePrefixesStringSlice covers the case
// where the TOML decoder hands us []string directly (e.g. when built
// programmatically rather than parsed from TOML).
func TestNew_ParsesSessionFilterIgnorePrefixesStringSlice(t *testing.T) {
	opts := map[string]any{
		"work_dir": t.TempDir(),
		"cli_path": "go",
		"session_filter": map[string]any{
			"ignore_summary_prefixes": []string{"[bot]"},
		},
	}

	a, err := New(opts)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	agent := a.(*Agent)
	agent.mu.RLock()
	got := append([]string(nil), agent.ignoreSummaryPrefixes...)
	agent.mu.RUnlock()

	if !reflect.DeepEqual(got, []string{"[bot]"}) {
		t.Errorf("ignoreSummaryPrefixes = %v, want [bot]", got)
	}
}

// TestNew_NoSessionFilter verifies absence of the session_filter block
// yields an empty ignoreSummaryPrefixes (the built-in defaults are
// applied at filter time, not stored on the agent).
func TestNew_NoSessionFilter(t *testing.T) {
	opts := map[string]any{
		"work_dir": t.TempDir(),
		"cli_path": "go",
	}

	a, err := New(opts)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	agent := a.(*Agent)
	agent.mu.RLock()
	got := append([]string(nil), agent.ignoreSummaryPrefixes...)
	agent.mu.RUnlock()

	if len(got) != 0 {
		t.Errorf("ignoreSummaryPrefixes = %v, want empty", got)
	}
}

// TestNew_SessionFilterIgnoresEmptyAndWhitespace verifies whitespace-only
// entries are dropped and empty entries don't act as wildcards.
func TestNew_SessionFilterIgnoresEmptyAndWhitespace(t *testing.T) {
	opts := map[string]any{
		"work_dir": t.TempDir(),
		"cli_path": "go",
		"session_filter": map[string]any{
			"ignore_summary_prefixes": []any{"  ", "", "[real-prefix]"},
		},
	}

	a, err := New(opts)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	agent := a.(*Agent)
	agent.mu.RLock()
	got := append([]string(nil), agent.ignoreSummaryPrefixes...)
	agent.mu.RUnlock()

	if !reflect.DeepEqual(got, []string{"[real-prefix]"}) {
		t.Errorf("ignoreSummaryPrefixes = %v, want [real-prefix]", got)
	}
}
