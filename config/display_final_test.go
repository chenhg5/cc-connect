package config

import (
	"strings"
	"testing"
)

// mode = "final" is accepted in both [display] and [projects.display].
func TestValidateDisplayConfig_AcceptsFinal(t *testing.T) {
	final := DisplayModeFinal
	if err := validateDisplayConfig("display", &DisplayConfig{Mode: &final}); err != nil {
		t.Fatalf("final rejected: %v", err)
	}
	bogus := "silent"
	err := validateDisplayConfig("projects[0].display", &DisplayConfig{Mode: &bogus})
	if err == nil || !strings.Contains(err.Error(), `"final"`) {
		t.Fatalf("an unknown mode must be rejected naming final among the choices, got %v", err)
	}
}

// Final output hides thinking, tools and the reply footer whatever the other
// display fields say, and the project's mode wins over the global one.
func TestEffectiveDisplay_Final(t *testing.T) {
	tru := true
	final, full := DisplayModeFinal, DisplayModeFull
	tests := []struct {
		name     string
		cfg      Config
		proj     ProjectConfig
		wantMode string
	}{
		{
			name:     "global final",
			cfg:      Config{Display: DisplayConfig{Mode: &final, ThinkingMessages: &tru, ToolMessages: &tru, ReplyFooter: &tru}},
			wantMode: DisplayModeFinal,
		},
		{
			name: "project final over global full",
			cfg:  Config{Display: DisplayConfig{Mode: &full}},
			proj: ProjectConfig{
				ReplyFooter: &tru,
				Display:     &DisplayConfig{Mode: &final, ThinkingMessages: &tru, ToolMessages: &tru},
			},
			wantMode: DisplayModeFinal,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode, tm, tool, _, _, _, footer, _ := EffectiveDisplay(&tt.cfg, &tt.proj)
			if mode != tt.wantMode || tm || tool || footer {
				t.Fatalf("mode=%q thinking=%t tool=%t footer=%t; want %q with everything else off", mode, tm, tool, footer, tt.wantMode)
			}
		})
	}

	// A project that sets its own mode leaves global final behind.
	cfg := Config{Display: DisplayConfig{Mode: &final}}
	proj := ProjectConfig{Display: &DisplayConfig{Mode: &full}}
	if mode, tm, tool, _, _, _, footer, _ := EffectiveDisplay(&cfg, &proj); mode != DisplayModeFull || !tm || !tool || !footer {
		t.Fatalf("project full over global final: mode=%q thinking=%t tool=%t footer=%t", mode, tm, tool, footer)
	}
}
