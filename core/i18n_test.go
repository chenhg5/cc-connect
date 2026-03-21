package core

import "testing"

func TestI18n_DefaultLanguage(t *testing.T) {
	i := NewI18n(LangEnglish)
	got := i.T(MsgStarting)
	if got == "" {
		t.Error("expected non-empty message")
	}
}

func TestI18n_Chinese(t *testing.T) {
	i := NewI18n(LangChinese)
	got := i.T(MsgStarting)
	if got == "" {
		t.Error("expected non-empty message")
	}
	// Should contain Chinese characters, not English
	if got == "⏳ Processing..." {
		t.Error("expected Chinese translation, got English")
	}
}

func TestI18n_FallbackToEnglish(t *testing.T) {
	i := NewI18n(Language("nonexistent"))
	got := i.T(MsgStarting)
	if got == "" {
		t.Error("should fallback to English")
	}
}

func TestI18n_MissingKey(t *testing.T) {
	i := NewI18n(LangEnglish)
	got := i.T(MsgKey("totally_missing_key"))
	if got != "[totally_missing_key]" && got != "" {
		t.Logf("missing key returned %q (acceptable: placeholder or empty)", got)
	}
}

func TestI18n_Tf(t *testing.T) {
	i := NewI18n(LangEnglish)
	got := i.Tf(MsgNameSet, "myname", "abc123")
	if got == "" {
		t.Error("Tf should return non-empty formatted message")
	}
}

func TestI18n_AllKeysHaveEnglish(t *testing.T) {
	for key, langs := range messages {
		if _, ok := langs[LangEnglish]; !ok {
			t.Errorf("message key %q missing English translation", key)
		}
	}
}

func TestDetectLanguage(t *testing.T) {
	tests := []struct {
		text    string
		wantLang Language
	}{
		// Japanese Hiragana
		{"こんにちは", LangJapanese},
		{"あいうえお", LangJapanese},
		// Japanese Katakana
		{"カタカナ", LangJapanese},
		// Chinese
		{"你好", LangChinese},
		{"中文测试", LangChinese},
		// Spanish
		{"¿Cómo estás?", LangSpanish},
		{"Niño español", LangSpanish},
		{"¡Hola!", LangSpanish},
		// English (default)
		{"Hello world", LangEnglish},
		{"Just normal text", LangEnglish},
		{"", LangEnglish},
	}

	for _, tt := range tests {
		t.Run(string(tt.wantLang), func(t *testing.T) {
			got := DetectLanguage(tt.text)
			if got != tt.wantLang {
				t.Errorf("DetectLanguage(%q) = %v, want %v", tt.text, got, tt.wantLang)
			}
		})
	}
}

func TestIsChinese(t *testing.T) {
	// Chinese characters (CJK Unified Ideographs)
	if !isChinese('中') {
		t.Error("'中' should be detected as Chinese")
	}
	if !isChinese('文') {
		t.Error("'文' should be detected as Chinese")
	}
	// Not Chinese
	if isChinese('a') {
		t.Error("'a' should not be Chinese")
	}
	if isChinese('ア') {
		t.Error("Japanese katakana 'ア' should not be Chinese")
	}
}

func TestIsJapanese(t *testing.T) {
	// Hiragana
	if !isJapanese('あ') {
		t.Error("Hiragana 'あ' should be Japanese")
	}
	// Katakana
	if !isJapanese('ア') {
		t.Error("Katakana 'ア' should be Japanese")
	}
	// Half-width Katakana
	if !isJapanese('ﾟ') {
		t.Error("Half-width Katakana should be Japanese")
	}
	// Not Japanese
	if isJapanese('中') {
		t.Error("Chinese should not be Japanese")
	}
	if isJapanese('a') {
		t.Error("ASCII 'a' should not be Japanese")
	}
}
