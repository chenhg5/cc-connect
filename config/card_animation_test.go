package config

import "testing"

// TestEffectiveCardAnimation covers the resolution order for the
// [projects.display].card_animation switch:
// per-project value wins over the global one, and the default is on.
func TestEffectiveCardAnimation(t *testing.T) {
	tru := true
	fals := false

	tests := []struct {
		name string
		cfg  *Config
		proj *ProjectConfig
		want bool
	}{
		{
			name: "default is on",
			cfg:  &Config{},
			proj: nil,
			want: true,
		},
		{
			name: "global off",
			cfg:  &Config{Display: DisplayConfig{CardAnimation: &fals}},
			proj: nil,
			want: false,
		},
		{
			name: "global on",
			cfg:  &Config{Display: DisplayConfig{CardAnimation: &tru}},
			proj: nil,
			want: true,
		},
		{
			name: "project overrides global",
			cfg:  &Config{Display: DisplayConfig{CardAnimation: &tru}},
			proj: &ProjectConfig{Display: &DisplayConfig{CardAnimation: &fals}},
			want: false,
		},
		{
			name: "project overrides global off",
			cfg:  &Config{Display: DisplayConfig{CardAnimation: &fals}},
			proj: &ProjectConfig{Display: &DisplayConfig{CardAnimation: &tru}},
			want: true,
		},
		{
			name: "project set, global unset",
			cfg:  &Config{},
			proj: &ProjectConfig{Display: &DisplayConfig{CardAnimation: &fals}},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveCardAnimation(tc.cfg, tc.proj); got != tc.want {
				t.Errorf("EffectiveCardAnimation() = %v, want %v", got, tc.want)
			}
		})
	}
}
