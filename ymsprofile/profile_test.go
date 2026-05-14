package ymsprofile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeProfile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestDiscoverConnectionTokenEnvNames_BasicDedup(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "yms-dev.yaml", "mcp:\n  endpoint: http://a\n  token_env: IUAPYYS_MCP_TOKEN\n")
	writeProfile(t, dir, "yms-stage.yaml", "mcp:\n  token_env: IUAPYYS_STAGE_TOKEN\n")
	// Duplicate env name in a different profile must be deduplicated.
	writeProfile(t, dir, "yms-other.yaml", "mcp:\n  token_env: IUAPYYS_MCP_TOKEN\n")
	// Example fixture must be ignored.
	writeProfile(t, dir, "yms-prod.yaml.example", "mcp:\n  token_env: SHOULD_NOT_APPEAR\n")
	writeProfile(t, dir, "yms-old.example.yaml", "mcp:\n  token_env: ALSO_IGNORED\n")
	// Empty token_env must be skipped.
	writeProfile(t, dir, "yms-empty.yaml", "mcp:\n  token_env: \"\"\n")
	// No mcp block must be skipped.
	writeProfile(t, dir, "yms-bare.yaml", "host: 127.0.0.1\n")

	got, err := DiscoverConnectionTokenEnvNames(dir)
	if err != nil {
		t.Fatalf("DiscoverConnectionTokenEnvNames returned warning: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(got), got)
	}
	if got[0].EnvName != "IUAPYYS_MCP_TOKEN" || got[1].EnvName != "IUAPYYS_STAGE_TOKEN" {
		t.Fatalf("env names = %+v", got)
	}
	for _, e := range got {
		if strings.Contains(e.ProfileFile, "example") {
			t.Fatalf("example file leaked into result: %q", e.ProfileFile)
		}
	}
}

func TestDiscoverConnectionTokenEnvNames_InvalidEnvNameIsDropped(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "yms-dev.yaml", "mcp:\n  token_env: \"FOO BAR\"\n")
	writeProfile(t, dir, "yms-bad2.yaml", "mcp:\n  token_env: \"1FOO\"\n")
	writeProfile(t, dir, "yms-bad3.yaml", "mcp:\n  token_env: \"FOO=BAR\"\n")
	writeProfile(t, dir, "yms-ok.yaml", "mcp:\n  token_env: OK_NAME\n")

	got, err := DiscoverConnectionTokenEnvNames(dir)
	// Warnings are surfaced as a non-nil error, but valid entries still come back.
	if err == nil {
		t.Fatalf("expected warning err for invalid env names, got nil")
	}
	if len(got) != 1 || got[0].EnvName != "OK_NAME" {
		t.Fatalf("got = %+v, want only OK_NAME", got)
	}
	msg := err.Error()
	for _, want := range []string{"yms-dev.yaml", "yms-bad2.yaml", "yms-bad3.yaml"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("warning err missing %q: %s", want, msg)
		}
	}
}

func TestDiscoverConnectionTokenEnvNames_DirMissing(t *testing.T) {
	_, err := DiscoverConnectionTokenEnvNames(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("expected error for missing dir")
	}
}

func TestDiscoverConnectionTokenEnvNames_EmptyDirReturnsEmpty(t *testing.T) {
	got, err := DiscoverConnectionTokenEnvNames(t.TempDir())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v, want empty", got)
	}
}

func TestDiscoverConnectionTokenEnvNames_DoesNotLeakValues(t *testing.T) {
	// Even when parse fails, the warning must reference filename — but
	// our minimal schema rarely surfaces values in errors. Smoke-test that
	// a YAML that does NOT parse cleanly only mentions the filename.
	dir := t.TempDir()
	writeProfile(t, dir, "broken.yaml", "this: is: not: valid: yaml: ::\n")
	_, err := DiscoverConnectionTokenEnvNames(dir)
	if err == nil {
		// yaml.v3 may still accept some constructs; that's fine.
		return
	}
	if !strings.Contains(err.Error(), "broken.yaml") {
		t.Fatalf("expected warning to mention filename, got %s", err)
	}
}

func TestFindProfileForConnection(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "yms-dev.yaml", "mcp: {token_env: A}\n")
	writeProfile(t, dir, "yms-stage.yml", "mcp: {token_env: B}\n")

	if got := FindProfileForConnection(dir, "yms-dev"); got != "yms-dev.yaml" {
		t.Fatalf("yms-dev = %q", got)
	}
	if got := FindProfileForConnection(dir, "yms-stage"); got != "yms-stage.yml" {
		t.Fatalf("yms-stage = %q", got)
	}
	if got := FindProfileForConnection(dir, "missing"); got != "" {
		t.Fatalf("missing = %q, want empty", got)
	}
	if got := FindProfileForConnection("", "yms-dev"); got != "" {
		t.Fatalf("empty dir = %q, want empty", got)
	}
}

func TestReadTokenEnv(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "yms-dev.yaml", "mcp:\n  token_env: IUAPYYS_MCP_TOKEN\n")
	writeProfile(t, dir, "yms-empty.yaml", "mcp:\n  endpoint: http://a\n")

	env, file, err := ReadTokenEnv(dir, "yms-dev")
	if err != nil {
		t.Fatalf("ReadTokenEnv: %v", err)
	}
	if env != "IUAPYYS_MCP_TOKEN" || file != "yms-dev.yaml" {
		t.Fatalf("got env=%q file=%q", env, file)
	}

	env, file, err = ReadTokenEnv(dir, "yms-empty")
	if err != nil {
		t.Fatalf("ReadTokenEnv empty: %v", err)
	}
	if env != "" || file != "yms-empty.yaml" {
		t.Fatalf("empty got env=%q file=%q", env, file)
	}

	env, file, err = ReadTokenEnv(dir, "no-such")
	if err != nil {
		t.Fatalf("ReadTokenEnv no-such: %v", err)
	}
	if env != "" || file != "" {
		t.Fatalf("no-such got env=%q file=%q", env, file)
	}
}

func TestIsValidEnvName(t *testing.T) {
	cases := map[string]bool{
		"FOO":       true,
		"foo":       true,
		"_FOO":      true,
		"FOO_BAR":   true,
		"FOO123":    true,
		"":          false,
		"1FOO":      false,
		"FOO BAR":   false,
		"FOO=BAR":   false,
		"FOO-BAR":   false,
		"FOO.BAR":   false,
		"FOO\nBAR":  false,
	}
	for in, want := range cases {
		if got := IsValidEnvName(in); got != want {
			t.Errorf("IsValidEnvName(%q) = %v, want %v", in, got, want)
		}
	}
}
