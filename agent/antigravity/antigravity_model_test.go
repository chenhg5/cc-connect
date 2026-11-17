package antigravity

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestConfiguredModels_BoundaryConditions(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{Models: []core.ModelOption{{Name: "first"}}},
			{Models: []core.ModelOption{{Name: "second"}}},
		},
	}

	tests := []struct {
		name      string
		activeIdx int
		wantNil   bool
		wantName  string
	}{
		{name: "negative index", activeIdx: -1, wantNil: true},
		{name: "out of range", activeIdx: 2, wantNil: true},
		{name: "valid index", activeIdx: 1, wantName: "second"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a.activeIdx = tt.activeIdx
			got := a.configuredModels()
			if tt.wantNil {
				if got != nil {
					t.Fatalf("configuredModels() = %v, want nil", got)
				}
				return
			}
			if len(got) != 1 || got[0].Name != tt.wantName {
				t.Fatalf("configuredModels() = %v, want %q", got, tt.wantName)
			}
		})
	}
}

func TestGetModel_PrefersActiveProviderModel(t *testing.T) {
	a := &Agent{
		model: "gemini-2.5-flash",
		providers: []core.ProviderConfig{
			{Name: "google", Model: "gemini-2.5-pro"},
		},
		activeIdx: 0,
	}

	if got := a.GetModel(); got != "gemini-2.5-pro" {
		t.Fatalf("GetModel() = %q, want gemini-2.5-pro", got)
	}
}

func TestParseAgyModels(t *testing.T) {
	out := "Fetching available models...\n" +
		"gemini-3.8-flash-high\tGemini 3.8 Flash (High)\n" +
		"claude-sonnet-4-6\tClaude Sonnet 4.6 (Thinking)\n" +
		"\n"
	got := parseAgyModels(out)
	if len(got) != 2 {
		t.Fatalf("parseAgyModels = %+v, want 2 models", got)
	}
	if got[0].Name != "gemini-3.8-flash-high" || got[0].Desc != "Gemini 3.8 Flash (High)" {
		t.Fatalf("first model = %+v", got[0])
	}
	if got[1].Name != "claude-sonnet-4-6" {
		t.Fatalf("second model = %+v", got[1])
	}
}

func TestAvailableModels_UsesAgyModels(t *testing.T) {
	script := filepath.Join(t.TempDir(), "agy")
	body := "#!/bin/sh\n[ \"$1\" = models ] || exit 1\nprintf 'Fetching available models...\\nmodel-a\\tModel A\\n'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	a := &Agent{cmd: script, activeIdx: -1}
	got := a.AvailableModels(context.Background())
	if len(got) != 1 || got[0].Name != "model-a" {
		t.Fatalf("AvailableModels = %+v, want model-a from agy", got)
	}
}

func TestReasoningEffort(t *testing.T) {
	script := writeFakeAgy(t, "  --effort <low|medium|high>  Reasoning effort\n", "")
	a := &Agent{cmd: script, activeIdx: -1}
	a.SetReasoningEffort("HIGH")
	if got := a.GetReasoningEffort(); got != "high" {
		t.Fatalf("effort = %q, want high", got)
	}
	a.SetReasoningEffort("bogus")
	if got := a.GetReasoningEffort(); got != "" {
		t.Fatalf("effort = %q, want empty for unknown value", got)
	}
}
