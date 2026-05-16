package ymsagent

import (
	"fmt"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// renderAutoRestoreFailure produces a user-visible, multi-language error
// message for the surface event when hidden /connect <profile> fails.
//
// Design choice (see plan §"设计原则"): cc-connect's core/i18n.go must
// not learn about yms-rca, so this lookup is package-local.
//
// Language resolution order:
//  1. CC_LANG in extraEnv — explicit operator override (covers zh-TW,
//     which DetectLanguage can't distinguish from zh by character set).
//  2. LANG in extraEnv — POSIX locale; "zh_CN.UTF-8" → "zh".
//  3. core.DetectLanguage(prompt) — auto-detect from the user message.
//
// Supported: en, zh, zh-TW, ja, es. Anything else falls back to en.
func renderAutoRestoreFailure(extraEnv []string, prompt, profile string, cause error) string {
	lang := resolveLanguage(extraEnv, prompt)
	tmpl := autoRestoreTemplates[lang]
	if tmpl == "" {
		tmpl = autoRestoreTemplates[core.LangEnglish]
	}
	detail := ""
	if cause != nil {
		detail = cause.Error()
	}
	return fmt.Sprintf(tmpl, profile, detail, profile)
}

// resolveLanguage picks the rendering language using the order documented
// on renderAutoRestoreFailure. Exposed for testability.
func resolveLanguage(extraEnv []string, prompt string) core.Language {
	if v := envLookup(extraEnv, "CC_LANG"); v != "" {
		if lang := normalizeLang(v); lang != "" {
			return lang
		}
	}
	if v := envLookup(extraEnv, "LANG"); v != "" {
		if lang := normalizeLang(v); lang != "" {
			return lang
		}
	}
	return core.DetectLanguage(prompt)
}

// normalizeLang maps a CC_LANG / LANG value to one of the supported
// core.Language constants. Returns "" if the input doesn't map.
//
// Examples:
//
//	"zh-TW"        -> LangTraditionalChinese
//	"zh_TW.UTF-8"  -> LangTraditionalChinese
//	"zh-Hant"      -> LangTraditionalChinese
//	"zh_CN.UTF-8"  -> LangChinese
//	"zh"           -> LangChinese
//	"ja_JP.UTF-8"  -> LangJapanese
//	"es_ES.UTF-8"  -> LangSpanish
//	"en_US.UTF-8"  -> LangEnglish
//	"C"            -> "" (no mapping; caller falls back)
func normalizeLang(raw string) core.Language {
	s := strings.TrimSpace(raw)
	if s == "" || strings.EqualFold(s, "C") || strings.EqualFold(s, "POSIX") {
		return ""
	}
	// Strip codeset suffix: "zh_TW.UTF-8" → "zh_TW".
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	// Normalise separators: "zh_TW" → "zh-TW".
	s = strings.ReplaceAll(s, "_", "-")
	lower := strings.ToLower(s)
	switch {
	case lower == "zh-tw" || lower == "zh-hant" || strings.HasPrefix(lower, "zh-hk") || strings.HasPrefix(lower, "zh-mo"):
		return core.LangTraditionalChinese
	case strings.HasPrefix(lower, "zh"):
		return core.LangChinese
	case strings.HasPrefix(lower, "ja"):
		return core.LangJapanese
	case strings.HasPrefix(lower, "es"):
		return core.LangSpanish
	case strings.HasPrefix(lower, "en"):
		return core.LangEnglish
	}
	return ""
}

// envLookup returns the value of key= in env, or "" if absent / empty.
func envLookup(env []string, key string) string {
	prefix := key + "="
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, prefix); ok {
			return v
		}
	}
	return ""
}

// autoRestoreTemplates carries one template per supported language.
// Format args (in order): profile, detail, profile.
var autoRestoreTemplates = map[core.Language]string{
	core.LangEnglish:            "Auto-restore of last profile `%s` failed: %s\n\nPlease re-run:\n/connect %s",
	core.LangChinese:            "自动恢复上次 profile `%s` 失败：%s\n\n请重新执行：\n/connect %s",
	core.LangTraditionalChinese: "自動恢復上次 profile `%s` 失敗：%s\n\n請重新執行：\n/connect %s",
	core.LangJapanese:           "前回の profile `%s` の自動復元に失敗しました：%s\n\n再実行してください：\n/connect %s",
	core.LangSpanish:            "Falló la restauración automática del último profile `%s`: %s\n\nPor favor vuelva a ejecutar:\n/connect %s",
}
