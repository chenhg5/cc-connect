package main

import (
	"strings"
	"testing"
)

// TestResolveCronDefaultSessionMode covers the guard used when loading
// [cron].session_mode: recognized aliases pass, typos are rejected so the
// daemon can warn instead of silently falling back to reuse.
func TestResolveCronDefaultSessionMode(t *testing.T) {
	for _, mode := range []string{"", "  ", "reuse", "new_per_run", "new-per-run", "new_topic", "new-topic", "topic"} {
		got, err := resolveCronDefaultSessionMode(mode)
		if err != nil {
			t.Errorf("resolveCronDefaultSessionMode(%q) = %v, want nil", mode, err)
		}
		if mode == "" || strings.TrimSpace(mode) == "" {
			if got != "" {
				t.Errorf("resolveCronDefaultSessionMode(%q) = %q, want empty", mode, got)
			}
		}
	}

	_, err := resolveCronDefaultSessionMode("new_topics")
	if err == nil {
		t.Fatal("resolveCronDefaultSessionMode(new_topics) = nil, want error")
	}
	if !strings.Contains(err.Error(), "new_topic") {
		t.Errorf("error %q should list the valid modes", err.Error())
	}
}
