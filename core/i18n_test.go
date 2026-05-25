package core

import (
	"strings"
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
		text     string
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

// idleConfirmLangs is the list of 5 supported languages that every idle-confirm
// MsgKey MUST translate into. Driven by design.md §7.
var idleConfirmLangs = []Language{
	LangEnglish,
	LangChinese,
	LangTraditionalChinese,
	LangJapanese,
	LangSpanish,
}

// idleConfirmKeys is the full set of 9 MsgKeys added by REQ-20260521 T-002.
var idleConfirmKeys = []MsgKey{
	MsgIdleConfirmCardTitle,
	MsgIdleConfirmBody,
	MsgIdleConfirmBtnRotate,
	MsgIdleConfirmBtnKeep,
	MsgIdleConfirmFooter,
	MsgIdleConfirmKept,
	MsgIdleConfirmRotated,
	MsgIdleConfirmTimeoutKept,
	MsgIdleConfirmQueuedHint,
}

// TestIdleConfirmMessages_FiveLangsCoverage asserts every one of the 9 new
// idle-confirm MsgKeys has a non-empty translation in all 5 supported
// languages (45 entries total). Backs design.md §7's
// "i18n 5 语言全覆盖" strong constraint.
func TestIdleConfirmMessages_FiveLangsCoverage(t *testing.T) {
	for _, key := range idleConfirmKeys {
		entry, ok := messages[key]
		if !ok {
			t.Errorf("MsgKey %q not registered in messages map", key)
			continue
		}
		for _, lang := range idleConfirmLangs {
			translated, present := entry[lang]
			if !present {
				t.Errorf("MsgKey %q is missing translation for language %q", key, lang)
				continue
			}
			if strings.TrimSpace(translated) == "" {
				t.Errorf("MsgKey %q has empty translation for language %q", key, lang)
			}
		}
	}
}

// TestIdleConfirmFooter_PlaceholderConsistency renders MsgIdleConfirmFooter
// with the contract args (short_id string, timeout_sec int) under all 5
// languages and asserts each rendered string contains both placeholder
// values. Guards against (a) wrong arg order across languages, (b) any
// language accidentally dropping a placeholder.
func TestIdleConfirmFooter_PlaceholderConsistency(t *testing.T) {
	const (
		shortID    = "abc12345"
		timeoutSec = 30
	)
	for _, lang := range idleConfirmLangs {
		i := NewI18n(lang)
		got := i.Tf(MsgIdleConfirmFooter, shortID, timeoutSec)
		if !strings.Contains(got, shortID) {
			t.Errorf("lang=%q: rendered footer %q does not contain short id %q", lang, got, shortID)
		}
		// fmt.Sprintf %d on 30 always produces the literal "30"; assert presence
		// regardless of surrounding wording (en uses "30s", zh uses "30 秒", etc.).
		if !strings.Contains(got, "30") {
			t.Errorf("lang=%q: rendered footer %q does not contain timeout %d", lang, got, timeoutSec)
		}
		// Defensive: an unsubstituted placeholder leaks "%!" — guard explicitly.
		if strings.Contains(got, "%!") {
			t.Errorf("lang=%q: footer has unsubstituted placeholder: %q", lang, got)
		}
	}
}

// TestIdleConfirmRotated_PlaceholderConsistency exercises MsgIdleConfirmRotated
// (%s = "/switch <id>") across all 5 languages and asserts the rendered
// string contains the literal switch-command hint.
func TestIdleConfirmRotated_PlaceholderConsistency(t *testing.T) {
	const switchCmd = "/switch abc12345"
	for _, lang := range idleConfirmLangs {
		i := NewI18n(lang)
		got := i.Tf(MsgIdleConfirmRotated, switchCmd)
		if !strings.Contains(got, switchCmd) {
			t.Errorf("lang=%q: rendered rotated %q does not contain switch cmd %q", lang, got, switchCmd)
		}
		if strings.Contains(got, "%!") {
			t.Errorf("lang=%q: rotated has unsubstituted placeholder: %q", lang, got)
		}
	}
}

// TestIdleConfirmCardTitle_PlaceholderConsistency exercises MsgIdleConfirmCardTitle
// (%s = humanized idle duration) across all 5 languages.
func TestIdleConfirmCardTitle_PlaceholderConsistency(t *testing.T) {
	const duration = "5m"
	for _, lang := range idleConfirmLangs {
		i := NewI18n(lang)
		got := i.Tf(MsgIdleConfirmCardTitle, duration)
		if !strings.Contains(got, duration) {
			t.Errorf("lang=%q: rendered card title %q does not contain duration %q", lang, got, duration)
		}
		if strings.Contains(got, "%!") {
			t.Errorf("lang=%q: card title has unsubstituted placeholder: %q", lang, got)
		}
	}
}

// TestIdleConfirmTimeoutKept_PlaceholderConsistency exercises MsgIdleConfirmTimeoutKept
// (%s = old session short id) across all 5 languages.
func TestIdleConfirmTimeoutKept_PlaceholderConsistency(t *testing.T) {
	const shortID = "abc12345"
	for _, lang := range idleConfirmLangs {
		i := NewI18n(lang)
		got := i.Tf(MsgIdleConfirmTimeoutKept, shortID)
		if !strings.Contains(got, shortID) {
			t.Errorf("lang=%q: rendered timeout-kept %q does not contain short id %q", lang, got, shortID)
		}
		if strings.Contains(got, "%!") {
			t.Errorf("lang=%q: timeout-kept has unsubstituted placeholder: %q", lang, got)
		}
	}
}

// TestIdleConfirmBody_EncouragementStanceAcrossLanguages enforces the
// design.md §7 strong constraint: the encouragement stance ("strongly
// recommend starting fresh when unrelated") MUST NOT be softened in any
// non-EN/ZH language. We assert each language's body contains a marker
// phrase consistent with that stance.
func TestIdleConfirmBody_EncouragementStanceAcrossLanguages(t *testing.T) {
	// Required substring per language. Phrases chosen from the actual
	// translations; if a future edit softens the wording, this test will
	// fire and force a deliberate review.
	wantMarkers := map[Language]string{
		LangEnglish:            "strongly recommend",
		LangChinese:            "强烈建议",
		LangTraditionalChinese: "強烈建議",
		LangJapanese:           "強くおすすめ",
		LangSpanish:            "encarecidamente",
	}
	for lang, marker := range wantMarkers {
		i := NewI18n(lang)
		body := i.T(MsgIdleConfirmBody)
		if !strings.Contains(body, marker) {
			t.Errorf("lang=%q: body missing encouragement marker %q. body=%q", lang, marker, body)
		}
	}
}

// TestIdleConfirmZeroArgKeys_NoRawPlaceholderLeak guards against accidental
// fmt directives in the four 0-arg keys (Body, BtnRotate, BtnKeep, Kept,
// QueuedHint). T() on a zero-arg key MUST return the template verbatim with
// no rendering, so any "%" remaining is suspicious. We allow "%" only when
// it appears as escaped "%%" (unlikely here).
func TestIdleConfirmZeroArgKeys_NoRawPlaceholderLeak(t *testing.T) {
	zeroArgKeys := []MsgKey{
		MsgIdleConfirmBody,
		MsgIdleConfirmBtnRotate,
		MsgIdleConfirmBtnKeep,
		MsgIdleConfirmKept,
		MsgIdleConfirmQueuedHint,
	}
	for _, key := range zeroArgKeys {
		for _, lang := range idleConfirmLangs {
			i := NewI18n(lang)
			got := i.T(key)
			if strings.Contains(got, "%s") || strings.Contains(got, "%d") {
				t.Errorf("key=%q lang=%q: zero-arg translation contains fmt directive: %q", key, lang, got)
			}
		}
	}
}
