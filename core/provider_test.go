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
