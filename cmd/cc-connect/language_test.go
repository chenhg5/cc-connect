package main

import (
	"testing"

	"github.com/timmyagentic/cc-connect-next/core"
)

func TestConfigLanguageDefaultsToChinese(t *testing.T) {
	if got := configLanguage(""); got != core.LangChinese {
		t.Fatalf("configLanguage(\"\") = %q, want Chinese default", got)
	}
}

func TestConfigLanguageValues(t *testing.T) {
	tests := []struct {
		raw  string
		want core.Language
	}{
		{"zh", core.LangChinese},
		{"chinese", core.LangChinese},
		{"zh-TW", core.LangTraditionalChinese},
		{"zh_TW", core.LangTraditionalChinese},
		{"zhtw", core.LangTraditionalChinese},
		{"ja", core.LangJapanese},
		{"japanese", core.LangJapanese},
		{"es", core.LangSpanish},
		{"spanish", core.LangSpanish},
		{"en", core.LangEnglish},
		{"english", core.LangEnglish},
		{"auto", core.LangAuto},
		{"something-else", core.LangAuto},
	}
	for _, tt := range tests {
		if got := configLanguage(tt.raw); got != tt.want {
			t.Errorf("configLanguage(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}
