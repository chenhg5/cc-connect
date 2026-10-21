package main

import (
	"testing"

	"github.com/chenhg5/cc-connect/daemon"
)

func TestResolveLogMaxBackups_FlagWinsOverEnv(t *testing.T) {
	t.Setenv("CC_LOG_MAX_BACKUPS", "5")
	got, src := resolveLogMaxBackups("2")
	if src != logBackupsSourceFlag {
		t.Fatalf("src = %q, want %q", src, logBackupsSourceFlag)
	}
	if got != 2 {
		t.Fatalf("got %d, want 2", got)
	}
}

func TestResolveLogMaxBackups_EnvWinsOverDefault(t *testing.T) {
	t.Setenv("CC_LOG_MAX_BACKUPS", "7")
	got, src := resolveLogMaxBackups("")
	if src != logBackupsSourceEnv {
		t.Fatalf("src = %q, want %q", src, logBackupsSourceEnv)
	}
	if got != 7 {
		t.Fatalf("got %d, want 7", got)
	}
}

func TestResolveLogMaxBackups_FallsBackToDefault(t *testing.T) {
	t.Setenv("CC_LOG_MAX_BACKUPS", "")
	got, src := resolveLogMaxBackups("")
	if src != logBackupsSourceDefault {
		t.Fatalf("src = %q, want %q", src, logBackupsSourceDefault)
	}
	if got != daemon.DefaultLogMaxBackups {
		t.Fatalf("got %d, want default %d", got, daemon.DefaultLogMaxBackups)
	}
}

func TestResolveLogMaxBackups_InvalidFlagFallsBackToEnv(t *testing.T) {
	t.Setenv("CC_LOG_MAX_BACKUPS", "4")
	got, src := resolveLogMaxBackups("not-a-number")
	if src != logBackupsSourceEnv {
		t.Fatalf("src = %q, want %q (invalid flag should fall through)", src, logBackupsSourceEnv)
	}
	if got != 4 {
		t.Fatalf("got %d, want 4", got)
	}
}

func TestResolveLogMaxBackups_InvalidEnvFallsBackToDefault(t *testing.T) {
	t.Setenv("CC_LOG_MAX_BACKUPS", "garbage")
	got, src := resolveLogMaxBackups("")
	if src != logBackupsSourceDefault {
		t.Fatalf("src = %q, want %q (invalid env should fall through to default)", src, logBackupsSourceDefault)
	}
	if got != daemon.DefaultLogMaxBackups {
		t.Fatalf("got %d, want default %d", got, daemon.DefaultLogMaxBackups)
	}
}

func TestResolveLogMaxBackups_ZeroFlagFallsBackToEnv(t *testing.T) {
	// Passing "0" through --log-max-backups is interpreted as "no
	// override" so the env wins. The flag's own default is also 0
	// (declared as flag.Int(..., 0, ...)) so we cannot distinguish
	// "unset" from "0" in the flag form — but the env var form must
	// still take precedence.
	t.Setenv("CC_LOG_MAX_BACKUPS", "9")
	got, src := resolveLogMaxBackups("0")
	if src != logBackupsSourceEnv {
		t.Fatalf("src = %q, want %q", src, logBackupsSourceEnv)
	}
	if got != 9 {
		t.Fatalf("got %d, want 9", got)
	}
}

func TestPreScanLogMaxBackupsFlag(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no flag", []string{"run", "thing"}, ""},
		{"flag with separate value", []string{"--log-max-backups", "5", "run"}, "5"},
		{"flag with = form", []string{"--log-max-backups=5", "run"}, "5"},
		{"flag with = form, no space", []string{"--log-max-backups=10"}, "10"},
		{"flag at end, no value", []string{"--log-max-backups"}, ""},
		{"flag with empty value", []string{"--log-max-backups", ""}, ""},
		{"unrelated --max-backups-prefix", []string{"--max-backups", "5"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := preScanLogMaxBackupsFlag(tc.args)
			if got != tc.want {
				t.Fatalf("preScanLogMaxBackupsFlag(%v) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

func TestResolveLogMaxBackups_PriorityRegressionForIssue1222(t *testing.T) {
	// Issue #1222 follow-up: CC_LOG_MAX_BACKUPS must be honoured when
	// set, not silently dropped. This pins the "flag > env > default"
	// priority so future refactors of the resolver don't regress the
	// behaviour users got from PR #1243.
	t.Setenv("CC_LOG_MAX_BACKUPS", "3")
	got, src := resolveLogMaxBackups("")
	if src != logBackupsSourceEnv {
		t.Fatalf("src = %q, want %q (env var must be honoured)", src, logBackupsSourceEnv)
	}
	if got != daemon.DefaultLogMaxBackups {
		t.Fatalf("got %d, want %d (3 == DefaultLogMaxBackups)", got, daemon.DefaultLogMaxBackups)
	}
}
