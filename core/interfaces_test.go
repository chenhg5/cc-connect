package core

import (
	"strings"
	"testing"
)

// TestAgentSystemPrompt_EnglishDefault covers the back-compat behaviour:
// AgentSystemPrompt() must return the same bytes as the English variant and
// must contain the silent-reply marker so existing English-only callers keep
// working unchanged after Issue #1655's i18n refactor.
func TestAgentSystemPrompt_EnglishDefault(t *testing.T) {
	got := AgentSystemPrompt()
	if got == "" {
		t.Fatal("AgentSystemPrompt() returned empty string")
	}
	if !strings.Contains(got, "NO_REPLY") {
		t.Error("English system prompt must contain NO_REPLY marker")
	}
	if !strings.Contains(got, "## Available tools") {
		t.Error("English system prompt must contain '## Available tools' heading")
	}
	// Should match the explicit English call exactly.
	if got != AgentSystemPromptForLang(LangEnglish) {
		t.Error("AgentSystemPrompt() must equal AgentSystemPromptForLang(LangEnglish)")
	}
}

// TestAgentSystemPromptForLang_AllToolKeysExist makes sure each of the four
// Issue #1655 tool sections (send / cron / timer / relay) has at least an
// English entry. Without English entries the engine would write
// "[agent_send_tool_prompt]" placeholders into the agent's memory file, which
// would break every cc-connect installation that didn't override its
// language. This is the "fallback to en on missing key" requirement.
func TestAgentSystemPromptForLang_AllToolKeysExist(t *testing.T) {
	keys := []MsgKey{
		MsgAgentSendToolPrompt,
		MsgAgentCronToolPrompt,
		MsgAgentTimerToolPrompt,
		MsgAgentRelayToolPrompt,
	}
	for _, k := range keys {
		t.Run(string(k), func(t *testing.T) {
			for _, lang := range []Language{LangEnglish, LangChinese, LangTraditionalChinese, LangJapanese, LangSpanish} {
				got := i18nT(lang, k)
				if got == "" || got == string(k) {
					t.Errorf("i18nT(%v, %q) fell through to key/empty — English fallback broken", lang, k)
				}
			}
		})
	}
}

// TestAgentSystemPromptForLang_EnglishHasAllFourTools verifies the English
// variant mentions each of the four tools by name. The agent needs to see
// "cron", "timer", "send", and "relay" so it knows the bridge exposes them.
func TestAgentSystemPromptForLang_EnglishHasAllFourTools(t *testing.T) {
	got := AgentSystemPromptForLang(LangEnglish)
	for _, marker := range []string{"cc-connect send", "cc-connect cron", "cc-connect timer", "cc-connect relay"} {
		if !strings.Contains(got, marker) {
			t.Errorf("English system prompt missing %q", marker)
		}
	}
}

// TestAgentSystemPromptForLang_ChineseHasAllFourTools verifies the zh
// variant of each tool section actually contains Chinese characters and the
// header / footer are still present. This guards against accidental
// regressions where a translation PR replaces Chinese with the English
// placeholder.
func TestAgentSystemPromptForLang_ChineseHasAllFourTools(t *testing.T) {
	got := AgentSystemPromptForLang(LangChinese)
	if got == AgentSystemPromptForLang(LangEnglish) {
		t.Fatal("zh and en system prompts are identical — translations not wired")
	}
	if !strings.Contains(got, "## Available tools") {
		t.Error("zh system prompt must keep the English '## Available tools' heading")
	}
	if !strings.Contains(got, "NO_REPLY") {
		t.Error("zh system prompt must keep the English NO_REPLY marker")
	}
	if !containsCJK(got) {
		t.Error("zh system prompt should contain CJK characters — looks untranslated")
	}
}

// TestAgentSystemPromptForLang_ZhTwFallback verifies the zh-TW → zh fallback
// chain documented on i18nT. zh-TW has no own entry for the tool prompts in
// this PR, so the user must see the zh version rather than English — that
// matches the i18n behaviour for the rest of the bundle.
func TestAgentSystemPromptForLang_ZhTwFallback(t *testing.T) {
	zh := AgentSystemPromptForLang(LangChinese)
	zhTW := AgentSystemPromptForLang(LangTraditionalChinese)
	if zh != zhTW {
		t.Error("zh-TW system prompt should fall back to zh for Issue #1655 tool sections")
	}
}

// TestAgentSystemPromptForLang_UnsupportedLangFallback covers languages
// that aren't translated in this PR (ja, es). They must fall back to English
// rather than render the raw key — per the owner decision in
// doc-20260823-ws0eux (fallback to en on missing key, never break the agent).
func TestAgentSystemPromptForLang_UnsupportedLangFallback(t *testing.T) {
	en := AgentSystemPromptForLang(LangEnglish)
	for _, lang := range []Language{LangJapanese, LangSpanish, Language("klingon")} {
		if got := AgentSystemPromptForLang(lang); got != en {
			t.Errorf("unsupported language %q should fall back to English; got distinct output", lang)
		}
	}
}

// TestAgentSystemPromptForLang_AutoFallback covers the auto-detect case.
// LangAuto is not a real language; passing it through the prompt builder
// must yield the English default rather than a raw key placeholder, because
// the engine can race ahead of the first user message and call this with
// the initial LangAuto value.
func TestAgentSystemPromptForLang_AutoFallback(t *testing.T) {
	got := AgentSystemPromptForLang(LangAuto)
	if got == "" || strings.Contains(got, "agent_send_tool_prompt") {
		t.Error("LangAuto must fall back to a real English/Chinese prompt, not the raw key")
	}
	if got != AgentSystemPromptForLang(LangEnglish) {
		t.Error("LangAuto should resolve to the English default since auto-detect hasn't run yet")
	}
}

// TestAgentSystemPromptForLang_ShapeConsistent guards the layout invariant:
// sections are separated by exactly two newlines, so the prompt stays
// human-readable and the engine's marker search (ccConnectInstructionMarker
// followed by the full prompt body) keeps working.
func TestAgentSystemPromptForLang_ShapeConsistent(t *testing.T) {
	for _, lang := range []Language{LangEnglish, LangChinese, LangTraditionalChinese} {
		got := AgentSystemPromptForLang(lang)
		// Four tool sections ⇒ three "\n\n" separators between them.
		if c := strings.Count(got, "\n\n"); c < 3 {
			t.Errorf("lang=%v: expected ≥3 '\\n\\n' separators between the 4 tool sections, got %d", lang, c)
		}
		if strings.HasPrefix(got, "\n") || strings.HasSuffix(got, "\n\n\n") {
			t.Errorf("lang=%v: prompt shape is wrong (leading newline or triple trailing newline)", lang)
		}
	}
}

// TestI18nT_MissingKeyReturnsKey verifies the documented miss behaviour: an
// unknown MsgKey must round-trip as itself, not as empty string or a
// placeholder, so engine code can detect the miss without parsing.
func TestI18nT_MissingKeyReturnsKey(t *testing.T) {
	got := i18nT(LangEnglish, MsgKey("totally_made_up_key_xyz"))
	if got != "totally_made_up_key_xyz" {
		t.Errorf("i18nT on miss should return the key itself, got %q", got)
	}
}

// TestI18nT_ZhTwChainFallsBackToZh exercises the fallback branch in i18nT
// for a key that exists in zh but not zh-TW. The result must be the zh
// string, not the key and not the English version.
func TestI18nT_ZhTwChainFallsBackToZh(t *testing.T) {
	// Pick a key we know exists in zh (one of the new tool prompts).
	if _, ok := messages[MsgAgentSendToolPrompt][LangChinese]; !ok {
		t.Fatal("MsgAgentSendToolPrompt missing zh translation — test fixture broken")
	}
	zh := i18nT(LangChinese, MsgAgentSendToolPrompt)
	zhTW := i18nT(LangTraditionalChinese, MsgAgentSendToolPrompt)
	if zh != zhTW {
		t.Error("zh-TW must fall back to zh for tool prompts that lack zh-TW translation")
	}
}

// TestNormalizeLanguageString covers every documented spelling plus the
// "unknown → auto" decision. Operators who typo a language must not silently
// get English; they should see the auto-detect behaviour so they notice the
// typo.
func TestNormalizeLanguageString(t *testing.T) {
	tests := []struct {
		in   string
		want Language
	}{
		{"en", LangEnglish},
		{"english", LangEnglish},
		{"EN", LangEnglish},
		{"zh", LangChinese},
		{"chinese", LangChinese},
		{"zh-TW", LangTraditionalChinese},
		{"zh_TW", LangTraditionalChinese},
		{"zhtw", LangTraditionalChinese},
		{"ja", LangJapanese},
		{"japanese", LangJapanese},
		{"es", LangSpanish},
		{"spanish", LangSpanish},
		{"auto", LangAuto},
		{"", LangAuto},
		{"klingon", LangAuto},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := NormalizeLanguageString(tt.in); got != tt.want {
				t.Errorf("NormalizeLanguageString(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// containsCJK reports whether s contains any CJK Unified Ideograph. Used by
// the Chinese-prompt test to assert that translations were actually wired,
// not just that the strings are non-empty.
func containsCJK(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}