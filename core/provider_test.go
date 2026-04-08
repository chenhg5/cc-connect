package core

import "testing"

func TestGetProviderModels(t *testing.T) {
	providers := []ProviderConfig{
		{Models: []ModelOption{{Name: "one"}}},
		{Models: []ModelOption{{Name: "two"}}},
	}

	tests := []struct {
		name      string
		activeIdx int
		wantNil   bool
		wantName  string
	}{
		{name: "negative index", activeIdx: -1, wantNil: true},
		{name: "empty providers", activeIdx: 0, wantNil: true},
		{name: "out of range", activeIdx: len(providers), wantNil: true},
		{name: "valid index", activeIdx: 1, wantName: "two"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := providers
			if tt.name == "empty providers" {
				input = nil
			}

			got := GetProviderModels(input, tt.activeIdx)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("GetProviderModels() = %v, want nil", got)
				}
				return
			}
			if len(got) != 1 || got[0].Name != tt.wantName {
				t.Fatalf("GetProviderModels() = %v, want %q", got, tt.wantName)
			}
		})
	}
}

func TestGetProviderModel(t *testing.T) {
	providers := []ProviderConfig{
		{Model: "gpt-4.1"},
		{Model: "gpt-5.4"},
	}

	tests := []struct {
		name      string
		activeIdx int
		fallback  string
		want      string
	}{
		{name: "negative index uses fallback", activeIdx: -1, fallback: "default-model", want: "default-model"},
		{name: "out of range uses fallback", activeIdx: len(providers), fallback: "default-model", want: "default-model"},
		{name: "empty provider model uses fallback", activeIdx: 0, fallback: "default-model", want: "gpt-4.1"},
		{name: "active provider model wins", activeIdx: 1, fallback: "default-model", want: "gpt-5.4"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetProviderModel(providers, tt.activeIdx, tt.fallback)
			if got != tt.want {
				t.Fatalf("GetProviderModel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSetProviderModel(t *testing.T) {
	providers := []ProviderConfig{
		{Name: "openai", Model: "gpt-4.1"},
		{Name: "backup", Model: "gpt-4.1-mini"},
	}

	updated, ok := SetProviderModel(providers, "openai", "gpt-5.4")
	if !ok {
		t.Fatal("SetProviderModel() did not find existing provider")
	}
	if updated[0].Model != "gpt-5.4" {
		t.Fatalf("updated provider model = %q, want gpt-5.4", updated[0].Model)
	}
	if providers[0].Model != "gpt-4.1" {
		t.Fatalf("original providers mutated = %q, want gpt-4.1", providers[0].Model)
	}

	updated, ok = SetProviderModel(providers, "missing", "gpt-5.4")
	if ok {
		t.Fatal("SetProviderModel() unexpectedly found missing provider")
	}
	if updated[0].Model != providers[0].Model {
		t.Fatalf("missing provider should leave copy unchanged, got %q want %q", updated[0].Model, providers[0].Model)
	}
}
