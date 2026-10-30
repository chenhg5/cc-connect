package claudecode

import (
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// TestBuildClaudeChildEnv_PreservesConfigEnvNoIsolation exercises the
// agent-mode cron default path: no run_as_user, no allowlist filtering, so
// [projects.agent.options.env] entries must reach cmd.Env verbatim. Includes
// the OTEL_* entries from issue #1589.
func TestBuildClaudeChildEnv_PreservesConfigEnvNoIsolation(t *testing.T) {
	osEnv := []string{
		"PATH=/usr/bin:/bin",
		"HOME=/root",
		"CLAUDECODE=1", // must be stripped
	}
	extraEnv := []string{
		"CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1",
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318",
		"OTEL_TRACES_EXPORTER=otlp",
		"OTEL_SERVICE_NAME=oh-my-kb",
		"OTEL_LOGS_EXPORTER=otlp",
		"OTEL_METRICS_EXPORTER=otlp",
		"OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf",
		"OTEL_LOG_USER_PROMPTS=1",
		"OTEL_LOG_TOOL_DETAILS=1",
	}

	got := buildClaudeChildEnv(osEnv, extraEnv, core.SpawnOptions{}, "/tmp")

	if hasEnvKey(got, "CLAUDECODE") {
		t.Errorf("CLAUDECODE must be stripped from inherited env, got %v", got)
	}
	if gotKey := getEnvValue(got, "PATH"); gotKey != "/usr/bin:/bin" {
		t.Errorf("PATH = %q, want /usr/bin:/bin", gotKey)
	}
	if gotKey := getEnvValue(got, "HOME"); gotKey != "/root" {
		t.Errorf("HOME = %q, want /root", gotKey)
	}
	if gotKey := getEnvValue(got, "CC_CONNECT_PERMISSION_HOOK_SKIP"); gotKey != "1" {
		t.Errorf("CC_CONNECT_PERMISSION_HOOK_SKIP = %q, want 1", gotKey)
	}
	for _, entry := range extraEnv {
		key := strings.SplitN(entry, "=", 2)[0]
		if gotKey := getEnvValue(got, key); gotKey == "" {
			t.Errorf("configEnv key %s missing from child env: %v", key, got)
		}
	}
}

// TestBuildClaudeChildEnv_PreservesConfigEnvUnderIsolation is the regression
// net for issue #1589. With run_as_user active and a minimal allowlist that
// does not list OTEL_*, configEnv values (which include OTEL_*) must still
// reach the child — they're the user's documented configuration and the
// allowlist only governs inherited env.
func TestBuildClaudeChildEnv_PreservesConfigEnvUnderIsolation(t *testing.T) {
	osEnv := []string{
		"PATH=/usr/bin:/bin",
		"HOME=/root",
		"OTEL_SERVICE_NAME=inherited-from-supervisor",
	}
	extraEnv := []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318",
		"OTEL_TRACES_EXPORTER=otlp",
		"OTEL_SERVICE_NAME=oh-my-kb",
		"OTEL_LOGS_EXPORTER=otlp",
		"OTEL_METRICS_EXPORTER=otlp",
		"OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf",
		"OTEL_LOG_USER_PROMPTS=1",
		"OTEL_LOG_TOOL_DETAILS=1",
		"CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1",
	}
	spawnOpts := core.SpawnOptions{
		RunAsUser: "someuser",
		// Empty EnvAllowlist: DefaultEnvAllowlist only has LANG/LC_*/TERM.
		// Without the #1589 fix, every OTEL_* configEnv key gets stripped
		// because the allowlist is the single source of truth post-filter.
		WorkDir: "/workspace/proj",
	}

	got := buildClaudeChildEnv(osEnv, extraEnv, spawnOpts, "/workspace/proj")

	if hasEnvKey(got, "CLAUDECODE") {
		t.Errorf("CLAUDECODE must be stripped, got %v", got)
	}
	// CC_RUNAS_CHDIR is in the merged allowlist when WorkDir is set; it must
	// survive.
	if gotKey := getEnvValue(got, core.RunAsChdirEnv); gotKey != "/workspace/proj" {
		t.Errorf("%s = %q, want /workspace/proj", core.RunAsChdirEnv, gotKey)
	}
	// Every configEnv key, including OTEL_* (case-insensitive), must reach
	// the child. This is the case-insensitive full-section assertion the PM
	// acceptance criteria requires.
	for _, entry := range extraEnv {
		key := strings.SplitN(entry, "=", 2)[0]
		wantValue := strings.SplitN(entry, "=", 2)[1]
		gotValue := getEnvValue(got, key)
		if gotValue == "" {
			t.Errorf("configEnv key %s missing under run_as_user isolation: %v", key, got)
			continue
		}
		// Case-insensitive comparison for OTEL_* to guard against future
		// case-normalisation regressions in the env pipeline.
		if !strings.EqualFold(gotValue, wantValue) {
			t.Errorf("configEnv key %s = %q, want %q (case-insensitive)", key, gotValue, wantValue)
		}
	}
}

// TestBuildClaudeChildEnv_DropsInheritedOTELUnderIsolation documents the
// existing behavior: env inherited from the supervisor process is subject
// to the run_as_user allowlist and gets stripped if not listed. This is the
// security boundary; only explicit [projects.agent.options.env] entries
// survive.
func TestBuildClaudeChildEnv_DropsInheritedOTELUnderIsolation(t *testing.T) {
	osEnv := []string{
		"OTEL_SERVICE_NAME=inherited-from-supervisor",
		"PATH=/usr/bin:/bin",
	}
	spawnOpts := core.SpawnOptions{RunAsUser: "someuser"}

	got := buildClaudeChildEnv(osEnv, nil, spawnOpts, "")

	if gotKey := getEnvValue(got, "OTEL_SERVICE_NAME"); gotKey != "" {
		t.Errorf("inherited OTEL_SERVICE_NAME must be stripped by IsolationMode filter, got %q", gotKey)
	}
	if gotKey := getEnvValue(got, "PATH"); gotKey != "" {
		t.Errorf("inherited PATH must be stripped by IsolationMode filter, got %q", gotKey)
	}
}

func hasEnvKey(env []string, key string) bool {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

func getEnvValue(env []string, key string) string {
	prefix := strings.ToUpper(key) + "="
	for _, e := range env {
		upper := strings.ToUpper(e)
		if strings.HasPrefix(upper, prefix) {
			idx := strings.IndexByte(e, '=')
			if idx >= 0 {
				return e[idx+1:]
			}
		}
	}
	return ""
}
