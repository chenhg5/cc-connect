package core

import (
	"sync"
	"testing"
)

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

// TestI18n_ConcurrentAccess is a regression test for a real data race on
// I18n's lang/detected fields. cc-connect fans out platform message
// handlers concurrently; each one calls DetectAndSet (writes detected),
// while typing/reply paths call T / CurrentLang concurrently (read
// lang/detected). Without a mutex `go test -race` flagged real races on
// these fields. This test exercises the same access pattern under -race
// so a future edit that drops the mutex breaks loudly instead of
// silently re-introducing the race.
func TestI18n_ConcurrentAccess(t *testing.T) {
	i := NewI18n(LangAuto)
	i.SetSaveFunc(func(Language) error { return nil })

	const goroutines = 16
	const iterations = 200

	texts := []string{"hello world", "你好世界", "こんにちは", "¡Hola amigo!"}

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for n := 0; n < iterations; n++ {
				switch (g + n) % 4 {
				case 0:
					i.DetectAndSet(texts[n%len(texts)])
				case 1:
					_ = i.T(MsgStarting)
				case 2:
					_ = i.CurrentLang()
				case 3:
					_ = i.IsZhLike()
				}
			}
		}(g)
	}
	wg.Wait()
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
