package droid

import (
	"reflect"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestNormalizeAuto(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"low", "low"},
		{"LOW", "low"},
		{"medium", "medium"},
		{"high", "high"},
		{"invalid", ""},
	}

	for _, tt := range tests {
		if got := normalizeAuto(tt.in); got != tt.want {
			t.Fatalf("normalizeAuto(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestNormalizeReasoningEffort(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"none", "none"},
		{"off", "off"},
		{"medium", "medium"},
		{"xhigh", "xhigh"},
		{"MAX", "max"},
		{"invalid", ""},
	}

	for _, tt := range tests {
		if got := normalizeReasoningEffort(tt.in); got != tt.want {
			t.Fatalf("normalizeReasoningEffort(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSetMode(t *testing.T) {
	a := &Agent{}

	a.SetMode("default")
	if got := a.GetMode(); got != "default" {
		t.Fatalf("GetMode() = %q, want default", got)
	}

	a.SetMode("low")
	if got := a.GetMode(); got != "low" {
		t.Fatalf("GetMode() = %q, want low", got)
	}

	a.SetMode("medium")
	if got := a.GetMode(); got != "medium" {
		t.Fatalf("GetMode() = %q, want medium", got)
	}

	a.SetMode("high")
	if got := a.GetMode(); got != "high" {
		t.Fatalf("GetMode() = %q, want high", got)
	}

	a.SetMode("yolo")
	if got := a.GetMode(); got != "yolo" {
		t.Fatalf("GetMode() = %q, want yolo", got)
	}
}

func TestProvidersAndModelSelection(t *testing.T) {
	a := &Agent{model: "gpt-5.4", activeIdx: -1}
	providers := []core.ProviderConfig{
		{Name: "default", Model: "custom:provider-model", APIKey: "k1", BaseURL: "https://example.invalid", Env: map[string]string{"FOO": "bar"}},
	}

	a.SetProviders(providers)
	if !a.SetActiveProvider("default") {
		t.Fatalf("SetActiveProvider(default) = false, want true")
	}

	if got := a.GetModel(); got != "custom:provider-model" {
		t.Fatalf("GetModel() = %q, want custom:provider-model", got)
	}

	env := a.providerEnvLocked()
	want := []string{"OPENAI_API_KEY=k1", "OPENAI_BASE_URL=https://example.invalid", "FOO=bar"}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("providerEnvLocked() = %#v, want %#v", env, want)
	}

	a.SetActiveProvider("")
	if got := a.GetModel(); got != "gpt-5.4" {
		t.Fatalf("GetModel() after clear = %q, want gpt-5.4", got)
	}
}

func TestCompressCommand(t *testing.T) {
	a := &Agent{}
	if got := a.CompressCommand(); got != "/compress" {
		t.Fatalf("CompressCommand() = %q, want /compress", got)
	}
}
