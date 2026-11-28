package config

import "testing"

// TestEffectiveAfterAskQuestion covers the resolution order for the
// [projects.display].after_ask_question switch:
// per-project value wins over the global one, and the default is
// "same_card" (upstream's one-card-per-turn behaviour).
func TestEffectiveAfterAskQuestion(t *testing.T) {
	newCard := AfterAskQuestionNewCard
	sameCard := AfterAskQuestionSameCard
	upper := "NEW_CARD"

	tests := []struct {
		name string
		cfg  *Config
		proj *ProjectConfig
		want string
	}{
		{
			name: "default is same_card",
			cfg:  &Config{},
			proj: nil,
			want: AfterAskQuestionSameCard,
		},
		{
			name: "global new_card",
			cfg:  &Config{Display: DisplayConfig{AfterAskQuestion: &newCard}},
			proj: nil,
			want: AfterAskQuestionNewCard,
		},
		{
			name: "global same_card",
			cfg:  &Config{Display: DisplayConfig{AfterAskQuestion: &sameCard}},
			proj: nil,
			want: AfterAskQuestionSameCard,
		},
		{
			name: "project overrides global",
			cfg:  &Config{Display: DisplayConfig{AfterAskQuestion: &sameCard}},
			proj: &ProjectConfig{Display: &DisplayConfig{AfterAskQuestion: &newCard}},
			want: AfterAskQuestionNewCard,
		},
		{
			name: "project only",
			cfg:  &Config{},
			proj: &ProjectConfig{Display: &DisplayConfig{AfterAskQuestion: &newCard}},
			want: AfterAskQuestionNewCard,
		},
		{
			name: "value is case and space insensitive",
			cfg:  &Config{Display: DisplayConfig{AfterAskQuestion: &upper}},
			proj: nil,
			want: AfterAskQuestionNewCard,
		},
		{
			name: "unknown value falls back to the default",
			cfg:  &Config{Display: DisplayConfig{AfterAskQuestion: ptr("bogus")}},
			proj: nil,
			want: AfterAskQuestionSameCard,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EffectiveAfterAskQuestion(tt.cfg, tt.proj); got != tt.want {
				t.Fatalf("EffectiveAfterAskQuestion() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestValidateDisplayConfigAfterAskQuestion pins the accepted values.
func TestValidateDisplayConfigAfterAskQuestion(t *testing.T) {
	for _, ok := range []string{AfterAskQuestionSameCard, AfterAskQuestionNewCard, "NEW_CARD"} {
		if err := validateDisplayConfig("display", &DisplayConfig{AfterAskQuestion: &ok}); err != nil {
			t.Fatalf("validateDisplayConfig(%q) = %v, want nil", ok, err)
		}
	}
	bad := "bogus"
	err := validateDisplayConfig("display", &DisplayConfig{AfterAskQuestion: &bad})
	if err == nil {
		t.Fatal("validateDisplayConfig accepted an unknown after_ask_question value")
	}
}

func ptr(s string) *string { return &s }
