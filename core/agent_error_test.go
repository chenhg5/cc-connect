package core

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestAgentDiagnosticError_LocalizedWithoutRawCause(t *testing.T) {
	cause := errors.New("raw credential should stay private")
	err := fmt.Errorf("prompt failed: %w", &AgentDiagnosticError{Key: MsgModelRepositoryPolicyDenied, Cause: cause})
	if !errors.Is(err, cause) {
		t.Fatal("lost cause for internal error handling")
	}
	for _, lang := range []Language{LangEnglish, LangChinese, LangTraditionalChinese, LangJapanese, LangSpanish} {
		i18n := NewI18n(lang)
		text := i18n.ErrorText(err)
		if !strings.Contains(text, "L4") || !strings.Contains(text, "4037") || strings.Contains(text, "credential") {
			t.Fatalf("incorrect diagnostic in %s: %s", lang, text)
		}
	}
	if got := NewI18n(LangChinese).ErrorText(errors.New("ordinary failure")); got != "ordinary failure" {
		t.Fatal(got)
	}
}
