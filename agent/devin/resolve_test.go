package devin

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// fuzzyFixture covers the production names /model was persisting verbatim.
// opus5.5 normalizes to "opus5-5". That is not the token run "opus-5-5"
// inside claude-opus-5-5, so it is a documented no-match.
const fuzzyFixture = `{
  "families": [
    {
      "family_label": "Claude Opus 5.5",
      "family_uid": "claude-opus-5.5",
      "slug": "claude-opus-5.5",
      "aliases": ["opus"],
      "variants": [
        {"model_uid": "claude-opus-5-5-high", "label": "high"},
        {"model_uid": "claude-opus-5-5-medium", "label": "medium"}
      ]
    },
    {
      "family_label": "Claude Sonnet 5.5",
      "family_uid": "claude-sonnet-5.5",
      "slug": "claude-sonnet-5.5",
      "aliases": ["sonnet"],
      "variants": [
        {"model_uid": "claude-sonnet-5-5-medium", "label": "medium"}
      ]
    },
    {
      "family_label": "SWE-2",
      "family_uid": "swe-2",
      "slug": "swe-2",
      "aliases": ["swe"],
      "variants": [
        {"model_uid": "swe-2-medium", "label": "medium"}
      ]
    },
    {
      "family_label": "GPT-6.1 Sol",
      "family_uid": "gpt-6.1-sol",
      "slug": "gpt-6.1-sol",
      "variants": [
        {"model_uid": "gpt-6-1-sol-medium", "label": "medium"}
      ]
    },
    {
      "family_label": "Fusion Opus",
      "family_uid": "fusion-opus",
      "slug": "fusion-opus",
      "variants": [
        {"model_uid": "fusion-opus-medium", "label": "medium"}
      ]
    }
  ]
}`

func TestResolveFuzzy_Names(t *testing.T) {
	_, families, err := parseDevinModels([]byte(fuzzyFixture))
	if err != nil {
		t.Fatal(err)
	}
	options := []string{
		"claude-opus-5-5-medium",
		"claude-sonnet-5-5-medium",
		"swe-2-high",
		"gpt-6-1-sol-medium",
		"fusion-opus-medium",
	}
	agent := &Agent{families: families}
	cases := []struct {
		in     string
		value  string
		effort string
	}{
		{"opus-5.5", "claude-opus-5-5-medium", ""},
		{"Opus 5.5", "claude-opus-5-5-medium", ""},
		{"opus5.5", "", ""},
		{"opus", "claude-opus-5-5-medium", ""},
		{"sonnet-5.5", "claude-sonnet-5-5-medium", ""},
		{"claude-opus-5-5-high", "claude-opus-5-5-medium", "high"},
		{"swe", "swe-2-high", ""},
		{"gpt-6.1", "gpt-6-1-sol-medium", ""},
		{"list", "", ""},
		{"bogus", "", ""},
		{"claude", "", ""},
	}
	for _, tc := range cases {
		value, effort := agent.resolveModelValue(tc.in, options)
		if value != tc.value || effort != tc.effort {
			t.Fatalf("%q = %q %q, want %q %q", tc.in, value, effort, tc.value, tc.effort)
		}
	}
	amb := resolveFuzzy("claude", families, options)
	if !amb.ambiguous || len(amb.candidates) < 2 {
		t.Fatalf("claude = %#v", amb)
	}
	joined := strings.Join(amb.candidates, ", ")
	if !strings.Contains(joined, "claude-opus-5.5") || !strings.Contains(joined, "claude-sonnet-5.5") {
		t.Fatalf("ambiguous candidates = %q", joined)
	}

	noAlias := append([]catalogFamily(nil), families...)
	noAlias[0].Aliases = nil
	opus := resolveFuzzy("opus", noAlias, options)
	if !opus.ok || opus.value != "claude-opus-5-5-medium" {
		t.Fatalf("fusion exclusion opus = %#v", opus)
	}
	fusion := resolveFuzzy("fusion-opus", noAlias, options)
	if !fusion.ok || fusion.value != "fusion-opus-medium" {
		t.Fatalf("fusion-opus = %#v", fusion)
	}
}

func TestValidateModel_CanonicalAndReject(t *testing.T) {
	if _, err := exec.LookPath("true"); err != nil {
		t.Skip("true not in PATH")
	}
	isolateModelCache(t)
	opts, families, err := parseDevinModels([]byte(fuzzyFixture))
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(map[string]any{"command": "true"})
	if err != nil {
		t.Fatal(err)
	}
	agent := a.(*Agent)
	waitModelsIdle(t, agent)
	agent.storeModels(opts, families)
	agent.RememberModelOptions("", []core.ModelOption{
		{Name: "claude-opus-5-5-medium"},
		{Name: "claude-sonnet-5-5-medium"},
		{Name: "swe-2-high"},
		{Name: "gpt-6-1-sol-medium"},
		{Name: "fusion-opus-medium"},
	})

	got, err := agent.ValidateModel(context.Background(), "opus")
	if err != nil || got != "claude-opus-5-5-medium" {
		t.Fatalf("alias = %q %v", got, err)
	}
	// A trailing effort suffix stays as typed so thought_level can be derived.
	got, err = agent.ValidateModel(context.Background(), "claude-opus-5-5-high")
	if err != nil || got != "claude-opus-5-5-high" {
		t.Fatalf("suffix kept = %q %v", got, err)
	}
	if _, err := agent.ValidateModel(context.Background(), "list"); err == nil || !strings.Contains(err.Error(), `unknown Devin model "list"`) {
		t.Fatalf("list err = %v", err)
	}
	if _, err := agent.ValidateModel(context.Background(), "claude"); err == nil || !strings.Contains(err.Error(), "ambiguous:") {
		t.Fatalf("claude err = %v", err)
	}
	if _, err := agent.ValidateModel(context.Background(), "opus5.5"); err == nil || !strings.Contains(err.Error(), `unknown Devin model "opus5.5"`) {
		t.Fatalf("opus5.5 err = %v", err)
	}

	bare, err := New(map[string]any{
		"command":    "true",
		"models_cmd": "devin-models-cmd-missing-xyz",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err = bare.(*Agent).ValidateModel(context.Background(), "list")
	if err != nil || got != "list" {
		t.Fatalf("unknown catalog accept = %q %v", got, err)
	}
}

// TestResolveFuzzy_ExcludesBareFusionFamily mirrors the real catalog, where the
// Fusion family slug is plain "fusion" and its variants embed other model ids
// (e.g. "...claude-opus-5-5-high-sidekick..."). "opus-5.5" must not be
// ambiguous with it.
func TestResolveFuzzy_ExcludesBareFusionFamily(t *testing.T) {
	families := []catalogFamily{
		{Label: "Claude Opus 5.5", UID: "claude-opus-5-5", Slug: "claude-opus-5.5",
			Variants: []string{"claude-opus-5-5-medium", "claude-opus-5-5-high"}},
		{Label: "Fusion", UID: "fusion", Slug: "fusion",
			Variants: []string{"fusion-claude-opus-5-5-high-sidekick-swe-2-medium"}},
	}
	options := []string{"claude-opus-5-5-medium", "fusion-claude-opus-5-5-high-sidekick-swe-2-medium"}
	got := resolveFuzzy("opus-5.5", families, options)
	if !got.ok || got.value != "claude-opus-5-5-medium" {
		t.Fatalf("opus-5.5 = %#v, want claude-opus-5-5-medium", got)
	}
	if f := resolveFuzzy("fusion", families, options); !f.ok {
		t.Fatalf("fusion = %#v, want resolved", f)
	}
}
