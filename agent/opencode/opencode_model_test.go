package opencode

import (
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

func TestNormalizeMode(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"yolo", "yolo"},
		{"YOLO", "yolo"},
		{"auto", "yolo"},
		{"AUTO", "yolo"},
		{"force", "yolo"},
		{"bypasspermissions", "yolo"},
		{"default", "default"},
		{"DEFAULT", "default"},
		{"", "default"},
		{"unknown", "default"},
		{"  yolo  ", "yolo"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeMode(tt.input)
			if got != tt.expected {
				t.Errorf("normalizeMode(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestAgent_Name(t *testing.T) {
	a := &Agent{}
	if got := a.Name(); got != "opencode" {
		t.Errorf("Name() = %q, want %q", got, "opencode")
	}
}

func TestAgent_SetModel(t *testing.T) {
	a := &Agent{}
	a.SetModel("gpt-4")
	if got := a.GetModel(); got != "gpt-4" {
		t.Errorf("GetModel() = %q, want %q", got, "gpt-4")
	}
}

func TestAgent_SetMode(t *testing.T) {
	a := &Agent{}
	a.SetMode("yolo")
	if got := a.GetMode(); got != "yolo" {
		t.Errorf("GetMode() = %q, want %q", got, "yolo")
	}
}

func TestAgent_GetActiveProvider(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{Name: "openai"},
			{Name: "anthropic"},
		},
		activeIdx: 1,
	}
	got := a.GetActiveProvider()
	if got == nil {
		t.Fatal("GetActiveProvider() returned nil")
	}
	if got.Name != "anthropic" {
		t.Errorf("GetActiveProvider().Name = %q, want %q", got.Name, "anthropic")
	}
}

func TestAgent_GetActiveProvider_NoActive(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{Name: "openai"},
		},
		activeIdx: -1,
	}
	if got := a.GetActiveProvider(); got != nil {
		t.Errorf("GetActiveProvider() = %v, want nil", got)
	}
}

func TestAgent_ListProviders(t *testing.T) {
	providers := []core.ProviderConfig{
		{Name: "openai"},
		{Name: "anthropic"},
	}
	a := &Agent{providers: providers}
	got := a.ListProviders()
	if len(got) != 2 {
		t.Errorf("ListProviders() returned %d providers, want 2", len(got))
	}
}

func TestAgent_SetActiveProvider(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{Name: "openai"},
			{Name: "anthropic"},
		},
	}
	if !a.SetActiveProvider("anthropic") {
		t.Error("SetActiveProvider(\"anthropic\") returned false")
	}
	if got := a.GetActiveProvider(); got == nil || got.Name != "anthropic" {
		t.Errorf("GetActiveProvider().Name = %q, want %q", got.Name, "anthropic")
	}
}

func TestAgent_SetActiveProvider_Invalid(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{Name: "openai"},
		},
	}
	if a.SetActiveProvider("nonexistent") {
		t.Error("SetActiveProvider(\"nonexistent\") returned true, want false")
	}
}

// verify Agent implements core.Agent
var _ core.Agent = (*Agent)(nil)
