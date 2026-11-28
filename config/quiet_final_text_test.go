package config

import "testing"

func TestEffectivePrependPreToolText(t *testing.T) {
	tru, fal := true, false
	for _, tt := range []struct {
		name    string
		global  *bool
		project *bool
		want    bool
	}{
		{"default", nil, nil, false},
		{"global", &tru, nil, true},
		{"project enables", &fal, &tru, true},
		{"project disables", &tru, &fal, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Display: DisplayConfig{PrependPreToolText: tt.global}}
			proj := &ProjectConfig{Display: &DisplayConfig{PrependPreToolText: tt.project}}
			if got := EffectivePrependPreToolText(cfg, proj); got != tt.want {
				t.Fatalf("effective prepend = %v, want %v", got, tt.want)
			}
		})
	}
}
