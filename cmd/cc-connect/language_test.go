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

// A fixed default makes the accepted spellings matter: before it, an
// unrecognized value degraded to auto-detect and the user still got their own
// language back. Now it silently means Chinese, so surrounding whitespace,
// casing, and the regional spellings people actually write must resolve.
func TestConfigLanguageAcceptsNaturalSpellings(t *testing.T) {
	tests := []struct {
		raw  string
		want core.Language
	}{
		{" zh ", core.LangChinese},
		{"ZH", core.LangChinese},
		{"zh-CN", core.LangChinese},
		{"zh_CN", core.LangChinese},
		{"zh-Hans", core.LangChinese},
		{"cn", core.LangChinese},
		{"EN", core.LangEnglish},
		{"en-US", core.LangEnglish},
		{"en_GB", core.LangEnglish},
		{"zh-tw", core.LangTraditionalChinese},
		{"zh-Hant", core.LangTraditionalChinese},
		{"JP", core.LangJapanese},
		{"ja-JP", core.LangJapanese},
		{"es-ES", core.LangSpanish},
		{"AUTO", core.LangAuto},
	}
	for _, tt := range tests {
		if got := configLanguage(tt.raw); got != tt.want {
			t.Errorf("configLanguage(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}
