package codex

import "testing"

func TestParseExtraEnv(t *testing.T) {
	got := parseExtraEnv(map[string]any{
		"CODEX_HOME": "/tmp/codex-home",
		"ZETA":       "last",
		"ALPHA":      123,
	})

	want := []string{
		"ALPHA=123",
		"CODEX_HOME=/tmp/codex-home",
		"ZETA=last",
	}

	if len(got) != len(want) {
		t.Fatalf("len(parseExtraEnv) = %d, want %d, got=%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseExtraEnv[%d] = %q, want %q, got=%v", i, got[i], want[i], got)
		}
	}
}
