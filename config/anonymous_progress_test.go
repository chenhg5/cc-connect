package config

import (
	"testing"

	"github.com/BurntSushi/toml"
)

func TestAnonymousCardModeConfig(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
	}{
		{"default", "", "legacy"},
		{"rich unchanged", "[display]\ncard_mode='rich'", "rich"},
		{"global", "[display]\ncard_mode='rich-anonymous'", "rich-anonymous"},
		{"normalized", "[display]\ncard_mode=' RICH-ANONYMOUS '", "rich-anonymous"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cfg Config
			if _, err := toml.Decode(tc.input, &cfg); err != nil {
				t.Fatal(err)
			}
			if err := validateDisplayConfig("display", &cfg.Display); err != nil {
				t.Fatal(err)
			}
			if got := EffectiveCardMode(&cfg, nil); got != tc.want {
				t.Fatalf("mode = %q, want %q", got, tc.want)
			}
		})
	}
	anonymous, rich := "rich-anonymous", "rich"
	for _, tc := range []struct{ global, project, want string }{
		{rich, anonymous, anonymous}, {anonymous, rich, rich}, {anonymous, "legacy", "legacy"},
	} {
		cfg := Config{Display: DisplayConfig{CardMode: &tc.global}}
		proj := validProject("test")
		proj.Display = &DisplayConfig{CardMode: &tc.project}
		cfg.Projects = []ProjectConfig{proj}
		if err := cfg.validate(); err != nil {
			t.Fatal(err)
		}
		if got := EffectiveCardMode(&cfg, &proj); got != tc.want {
			t.Fatalf("project mode = %q, want %q", got, tc.want)
		}
	}
}
