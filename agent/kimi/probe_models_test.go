package kimi

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseKimiModelList_OneModelPerLine covers the simplest layout the
// modern kimi-code CLI might emit: a single identifier per line. This is
// the format we expect the `kimi model list` subcommand to use once #1795
// ships; the parser should accept it without any header lines.
func TestParseKimiModelList_OneModelPerLine(t *testing.T) {
	out := `k3
k3-256k
kimi-k2-6
kimi-k2-7-preview
`
	got := parseKimiModelList(out)
	require.Len(t, got, 4)
	assert.Equal(t, "k3", got[0].Name)
	assert.Empty(t, got[0].Desc)
	assert.Equal(t, "kimi-k2-7-preview", got[3].Name)
}

// TestParseKimiModelList_IdWithDescription pins the alternate "id <tab>
// description" layout the CLI is free to pick. The first whitespace-
// delimited token on each line is treated as the identifier and the
// remainder of the line becomes the description (with edge punctuation
// like outer parentheses trimmed so the picker reads cleanly).
func TestParseKimiModelList_IdWithDescription(t *testing.T) {
	out := "k3\tDefault (latest)\nk3-256k\tLong context (256k tokens)\nkimi-k2-6-preview\tStable K2.6\n"
	got := parseKimiModelList(out)
	require.Len(t, got, 3)
	assert.Equal(t, "k3", got[0].Name)
	assert.Equal(t, "Default (latest)", got[0].Desc,
		"description is everything after the identifier, with outer punctuation trimmed")
	assert.Equal(t, "k3-256k", got[1].Name)
	assert.Equal(t, "Long context (256k tokens)", got[1].Desc,
		"inner parentheses inside the description are preserved")
	assert.Equal(t, "kimi-k2-6-preview", got[2].Name)
	assert.Equal(t, "Stable K2.6", got[2].Desc)
}

// TestParseKimiModelList_SkipsHeaderAndFooter covers the case where the CLI
// prints a header line ("Available models:") followed by model rows. The
// header must be ignored while the model rows parse normally.
func TestParseKimiModelList_SkipsHeaderAndFooter(t *testing.T) {
	out := `Available models:
  k3           Default
  k3-256k      Long context
  kimi-k2-6    K2.6

Run 'kimi model use <name>' to switch.
`
	got := parseKimiModelList(out)
	require.Len(t, got, 3)
	assert.Equal(t, "k3", got[0].Name)
	assert.Equal(t, "k3-256k", got[1].Name)
	assert.Equal(t, "kimi-k2-6", got[2].Name)
}

// TestParseKimiModelList_Deduplicates repeats a model identifier across
// rows (e.g. once in a "Recently used" section and once in "Available")
// and verifies the parser surfaces each one only once.
func TestParseKimiModelList_Deduplicates(t *testing.T) {
	out := `Recently used:
  k3
Available:
  k3
  kimi-k2-6
`
	got := parseKimiModelList(out)
	require.Len(t, got, 2)
	assert.Equal(t, "k3", got[0].Name)
	assert.Equal(t, "kimi-k2-6", got[1].Name)
}

// TestParseKimiModelList_NamespacedAliases ensures the regex accepts
// namespaced forms like "kimi-code/k3" — the kimi-code CLI has shipped
// provider-prefixed aliases in some builds and we don't want the parser
// to silently drop them.
func TestParseKimiModelList_NamespacedAliases(t *testing.T) {
	out := "kimi-code/k3\nkimi-code/k3-256k\n"
	got := parseKimiModelList(out)
	require.Len(t, got, 2)
	assert.Equal(t, "kimi-code/k3", got[0].Name)
	assert.Equal(t, "kimi-code/k3-256k", got[1].Name)
}

// TestParseKimiModelList_IgnoresPunctuationNoise guards against the
// parser picking up stray characters ("*", "-", "* k3") that a future
// CLI layout might emit as decoration. Only well-formed identifiers
// survive.
func TestParseKimiModelList_IgnoresPunctuationNoise(t *testing.T) {
	out := `
*
-

k3
kimi-k2-6-preview
`
	got := parseKimiModelList(out)
	require.Len(t, got, 2)
	assert.Equal(t, "k3", got[0].Name)
	assert.Equal(t, "kimi-k2-6-preview", got[1].Name)
}

// TestParseKimiModelList_EmptyAndCommentLines tolerates blank lines and
// shell-style comment lines so a future CLI that emits # headers doesn't
// break the picker.
func TestParseKimiModelList_EmptyAndCommentLines(t *testing.T) {
	out := `
# This is a comment header
k3

# blank line above, then another model
kimi-k2-6
`
	got := parseKimiModelList(out)
	require.Len(t, got, 2)
	assert.Equal(t, "k3", got[0].Name)
	assert.Equal(t, "kimi-k2-6", got[1].Name)
}

// TestProbeKimiModels_MissingBinary covers the most common failure path
// on machines that don't have kimi-code CLI installed at all: the probe
// must return nil and log nothing louder than Debug.
func TestProbeKimiModels_MissingBinary(t *testing.T) {
	got := probeKimiModels(context.Background(),
		"kimi-binary-that-does-not-exist-1795-test",
		200*time.Millisecond)
	assert.Nil(t, got, "missing binary must yield nil so caller falls back to built-in list")
}

// TestProbeKimiModels_StubHelperScript points PATH at a temporary dir
// containing a fake `kimi` script that emits a parseable model list.
// This validates the end-to-end probe: exec.LookPath, exec.CommandContext,
// and parseKimiModelList working together. The fake script must produce
// zero exit code for `kimi model list` so the probe treats its stdout as
// valid. (The other candidate subcommands exit non-zero so the probe
// correctly stops after the first hit.)
func TestProbeKimiModels_StubHelperScript(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)

	script := `#!/bin/sh
case "$1 $2" in
  "model list")
    echo "k3"
    echo "k3-256k"
    echo "kimi-k2-6-preview"
    exit 0
    ;;
  *)
    echo "unknown subcommand: $*" >&2
    exit 64
    ;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "kimi"), []byte(script), 0o755))

	got := probeKimiModels(context.Background(), "kimi", 2*time.Second)
	require.Len(t, got, 3, "stub script returns 3 models on `kimi model list`")
	assert.Equal(t, "k3", got[0].Name)
	assert.Equal(t, "k3-256k", got[1].Name)
	assert.Equal(t, "kimi-k2-6-preview", got[2].Name)
}

// TestProbeKimiModels_StubHelperScript_FallsBackToSecondCandidate points
// PATH at a stub whose `kimi model list` exits non-zero (subcommand
// missing) but `kimi models list` succeeds. The probe must keep trying
// candidates and ultimately return models from the second attempt.
func TestProbeKimiModels_StubHelperScript_FallsBackToSecondCandidate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)

	script := `#!/bin/sh
case "$1 $2 $3" in
  "model list ")
    echo "no models" >&2
    exit 64
    ;;
  "models list ")
    echo "kimi-k2-6"
    echo "kimi-k2-7"
    exit 0
    ;;
  "models ")
    echo "kimi-k2-6"
    echo "kimi-k2-7"
    exit 0
    ;;
  *)
    echo "unknown: $*" >&2
    exit 64
    ;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "kimi"), []byte(script), 0o755))

	got := probeKimiModels(context.Background(), "kimi", 2*time.Second)
	require.NotEmpty(t, got, "probe must keep trying candidates past the first failure")
	names := make([]string, 0, len(got))
	for _, m := range got {
		names = append(names, m.Name)
	}
	assert.Contains(t, names, "kimi-k2-6")
	assert.Contains(t, names, "kimi-k2-7")
}

// TestProbeKimiModels_StubEmitsOnlyHeader verifies the probe gives up
// cleanly when the CLI emits a header but no parseable identifiers. We
// don't want an empty picker just because the user happens to have a
// kimi-code build whose `model list` output we haven't learned yet.
func TestProbeKimiModels_StubEmitsOnlyHeader(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)

	script := `#!/bin/sh
case "$1 $2" in
  "model list")
    echo "Available models:"
    echo "Run 'kimi help' for details."
    exit 0
    ;;
  *)
    exit 64
    ;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "kimi"), []byte(script), 0o755))

	got := probeKimiModels(context.Background(), "kimi", 2*time.Second)
	assert.Nil(t, got, "header-only output must NOT be promoted to a model list (#1795)")
}

// TestKimiBuiltinModels_RefreshedWithK3 is the regression test for #1795:
// the static fallback list must contain current model aliases (k3,
// k3-256k) so that users whose installed CLI doesn't expose the `model
// list` subcommand still see up-to-date options. K2 / K2.5 are kept at
// the bottom for backward compatibility.
func TestKimiBuiltinModels_RefreshedWithK3(t *testing.T) {
	models := kimiBuiltinModels()
	require.NotEmpty(t, models)

	names := make([]string, 0, len(models))
	for _, m := range models {
		names = append(names, m.Name)
	}
	assert.Contains(t, names, "k3", "k3 must be in the refreshed fallback list (#1795)")
	assert.Contains(t, names, "k3-256k", "k3-256k must be in the refreshed fallback list (#1795)")
	assert.Contains(t, names, "kimi-k2-5", "K2.5 entries must stay for backward compatibility")
	assert.Contains(t, names, "kimi-k2-0711", "K2 entries must stay for backward compatibility")

	// Newest models first, oldest last — so the picker's default
	// selection lands on a current model.
	idx := indexOf(models, "k3")
	for _, older := range []string{"kimi-k2-5", "kimi-k2-0711"} {
		assert.Less(t, idx, indexOf(models, older),
			"%s should appear before %s in the picker ordering", "k3", older)
	}
}

func indexOf(models []core.ModelOption, name string) int {
	for i, m := range models {
		if m.Name == name {
			return i
		}
	}
	return -1
}

// TestAgentAvailableModels_PreferProviderModels pins the resolution order
// documented in AvailableModels: configured provider models always beat
// both the probe result and the built-in fallback. Even when the probe
// succeeds, a user-supplied [projects.agent.options.providers] entry
// must take priority — that's how cc-connect users currently opt out of
// drift-sensitive defaults.
func TestAgentAvailableModels_PreferProviderModels(t *testing.T) {
	a := &Agent{
		workDir:    "/tmp",
		activeIdx:  0,
		discovered: []core.ModelOption{{Name: "k3", Desc: "from probe"}},
		providers: []core.ProviderConfig{
			{
				Name: "moonshot",
				Models: []core.ModelOption{
					{Name: "custom-model-a", Desc: "from provider config"},
					{Name: "custom-model-b", Desc: "from provider config"},
				},
			},
		},
	}

	got := a.AvailableModels(context.Background())
	require.Len(t, got, 2)
	assert.Equal(t, "custom-model-a", got[0].Name,
		"provider models must win over probe results (#1795)")
	assert.Equal(t, "custom-model-b", got[1].Name)
}

// TestAgentAvailableModels_PreferProbeOverBuiltin is the headline
// regression for #1795: when no provider is configured but the probe
// succeeded, the picker must follow the installed CLI rather than fall
// back to the stale K2/K2.5 list. This is what stops the picker from
// silently downgrading k3 users to kimi-k2-5.
func TestAgentAvailableModels_PreferProbeOverBuiltin(t *testing.T) {
	a := &Agent{
		workDir:   "/tmp",
		activeIdx: -1,
		discovered: []core.ModelOption{
			{Name: "k3", Desc: "current default"},
			{Name: "k3-256k", Desc: "long context"},
			{Name: "kimi-k2-6", Desc: "stable K2.6"},
		},
	}

	got := a.AvailableModels(context.Background())
	require.Len(t, got, 3)
	assert.Equal(t, "k3", got[0].Name)
	assert.Equal(t, "k3-256k", got[1].Name)
	assert.Equal(t, "kimi-k2-6", got[2].Name)
}

// TestAgentAvailableModels_FallbackToBuiltinWhenProbeFailed covers the
// case where the probe returned nil (older kimi-cli dialect, CLI
// unreachable, unparseable output). AvailableModels must still return a
// non-empty list — refreshed to include k3 — so /model never appears
// empty.
func TestAgentAvailableModels_FallbackToBuiltinWhenProbeFailed(t *testing.T) {
	a := &Agent{
		workDir:   "/tmp",
		activeIdx: -1,
		// discovered intentionally nil — simulates probe failure.
	}

	got := a.AvailableModels(context.Background())
	require.NotEmpty(t, got, "fallback must always be non-empty (#1795)")
	names := make([]string, 0, len(got))
	for _, m := range got {
		names = append(names, m.Name)
	}
	assert.Contains(t, names, "k3",
		"fallback must surface k3 even when probe failed, otherwise the picker is effectively empty (#1795)")
}

// TestAgentAvailableModels_ProviderWithEmptyModelsDoesNotShadowProbe
// pins a subtle bug: a configured provider that has zero explicit models
// (e.g. user only set APIKey and BaseURL) must NOT block the probe
// result. Otherwise users who set just an API key would lose the dynamic
// picker.
func TestAgentAvailableModels_ProviderWithEmptyModelsDoesNotShadowProbe(t *testing.T) {
	a := &Agent{
		workDir:   "/tmp",
		activeIdx: 0,
		discovered: []core.ModelOption{
			{Name: "k3"},
			{Name: "kimi-k2-6"},
		},
		providers: []core.ProviderConfig{
			{Name: "moonshot", APIKey: "sk-123", Models: nil},
		},
	}

	got := a.AvailableModels(context.Background())
	require.Len(t, got, 2,
		"empty provider Models must not shadow the probe result (#1795)")
	assert.Equal(t, "k3", got[0].Name)
	assert.Equal(t, "kimi-k2-6", got[1].Name)
}

// TestAgentDiscoveredModels_RaceFree verifies that the discoveredModels
// accessor does not race against SetWorkDir / SetMode writes on the same
// Agent (per the cc-connect engineering rule "Agent sessions are
// accessed from multiple goroutines; protect shared state with
// sync.Mutex"). Run with -race to catch any regression.
func TestAgentDiscoveredModels_RaceFree(t *testing.T) {
	a := &Agent{
		workDir:    "/tmp",
		activeIdx:  -1,
		discovered: []core.ModelOption{{Name: "k3"}},
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			a.SetWorkDir("/tmp/a")
			a.SetMode("default")
		}
	}()
	for i := 0; i < 200; i++ {
		_ = a.discoveredModels()
		_ = a.AvailableModels(context.Background())
	}
	<-done
}
