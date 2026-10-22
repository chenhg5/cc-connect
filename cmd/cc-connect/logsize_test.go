package main

import (
	"testing"

	"github.com/chenhg5/cc-connect/daemon"
)

func TestResolveLogMaxSize_FlagWinsOverEnv(t *testing.T) {
	t.Setenv("CC_LOG_MAX_SIZE", "20MB")
	got, src := resolveLogMaxSize("5MB")
	if src != logSizeSourceFlag {
		t.Fatalf("src = %q, want %q", src, logSizeSourceFlag)
	}
	want := int64(5) * 1024 * 1024
	if got != want {
		t.Fatalf("got %d, want %d", got, want)
	}
}

func TestResolveLogMaxSize_EnvWinsOverDefault(t *testing.T) {
	t.Setenv("CC_LOG_MAX_SIZE", "10MB")
	got, src := resolveLogMaxSize("")
	if src != logSizeSourceEnv {
		t.Fatalf("src = %q, want %q", src, logSizeSourceEnv)
	}
	if got != int64(daemon.DefaultLogMaxSize) {
		t.Fatalf("got %d, want default %d", got, daemon.DefaultLogMaxSize)
	}
}

func TestResolveLogMaxSize_FallsBackToDefault(t *testing.T) {
	t.Setenv("CC_LOG_MAX_SIZE", "")
	got, src := resolveLogMaxSize("")
	if src != logSizeSourceDefault {
		t.Fatalf("src = %q, want %q", src, logSizeSourceDefault)
	}
	if got != int64(daemon.DefaultLogMaxSize) {
		t.Fatalf("got %d, want default %d", got, daemon.DefaultLogMaxSize)
	}
}

func TestResolveLogMaxSize_InvalidFlagFallsBackToEnv(t *testing.T) {
	t.Setenv("CC_LOG_MAX_SIZE", "10MB")
	got, src := resolveLogMaxSize("not-a-number")
	if src != logSizeSourceEnv {
		t.Fatalf("src = %q, want %q (invalid flag should fall through)", src, logSizeSourceEnv)
	}
	if got != int64(daemon.DefaultLogMaxSize) {
		t.Fatalf("got %d, want %d", got, daemon.DefaultLogMaxSize)
	}
}

func TestResolveLogMaxSize_InvalidEnvFallsBackToDefault(t *testing.T) {
	t.Setenv("CC_LOG_MAX_SIZE", "garbage")
	got, src := resolveLogMaxSize("")
	if src != logSizeSourceDefault {
		t.Fatalf("src = %q, want %q (invalid env should fall through to default)", src, logSizeSourceDefault)
	}
	if got != int64(daemon.DefaultLogMaxSize) {
		t.Fatalf("got %d, want default %d", got, daemon.DefaultLogMaxSize)
	}
}

func TestPreScanLogMaxSizeFlag(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no flag", []string{"run", "thing"}, ""},
		{"flag with separate value", []string{"--log-max-size", "10MB", "run"}, "10MB"},
		{"flag with = form", []string{"--log-max-size=10MB", "run"}, "10MB"},
		{"flag with = form, no space", []string{"--log-max-size=512K"}, "512K"},
		{"flag at end, no value", []string{"--log-max-size"}, ""},
		{"flag with empty value", []string{"--log-max-size", ""}, ""},
		{"unrelated --flag-prefix", []string{"--max-size", "10MB"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := preScanLogMaxSizeFlag(tc.args)
			if got != tc.want {
				t.Fatalf("preScanLogMaxSizeFlag(%v) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

func TestResolveLogMaxSize_PriorityRegressionForIssue1222(t *testing.T) {
	// Issue #1222 scenario: user sets CC_LOG_MAX_SIZE=10MB but v1.3.3-beta.4
	// used strconv.ParseInt which silently dropped the value, falling back
	// to the default 10MB. The default happened to match in this case, so
	// the symptom in the issue (logs grow to 30MB+) is presumably an
	// interaction with the rotatation post-write check. Either way, the
	// env var must now be parsed and reported, not silently dropped.
	t.Setenv("CC_LOG_MAX_SIZE", "10MB")
	got, src := resolveLogMaxSize("")
	if src != logSizeSourceEnv {
		t.Fatalf("src = %q, want %q (env var must be honoured)", src, logSizeSourceEnv)
	}
	if got != int64(daemon.DefaultLogMaxSize) {
		t.Fatalf("got %d, want %d (10MB == DefaultLogMaxSize)", got, daemon.DefaultLogMaxSize)
	}
}
