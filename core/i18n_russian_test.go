package core

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
)

func TestRussianMessagesComplete(t *testing.T) {
	// A lower bound catches accidental removal while allowing future keys.
	if len(messages) < 462 {
		t.Fatalf("message dictionary shrank to %d keys", len(messages))
	}
	// Compare the entire directive, including positions, width, precision,
	// flags and literal %%. A verb-only comparison misses %[1]s regressions.
	directive := regexp.MustCompile(`%(?:\[\d+\])?[+#0 -]*(?:\d+|\*)?(?:\.(?:\d+|\*))?(?:\[\d+\])?[a-zA-Z%]`)
	i := NewI18n(LangRussian)
	for key, langs := range messages {
		t.Run(string(key), func(t *testing.T) {
			ru, ok := langs[LangRussian]
			if !ok || strings.TrimSpace(ru) == "" {
				t.Fatal("missing Russian translation")
			}
			if got := i.T(key); got != ru {
				t.Fatalf("T used fallback: %q", got)
			}
			en := langs[LangEnglish]
			if !reflect.DeepEqual(directive.FindAllString(en, -1), directive.FindAllString(ru, -1)) {
				t.Errorf("format directives differ: EN=%v RU=%v", directive.FindAllString(en, -1), directive.FindAllString(ru, -1))
			}
			if strings.Count(en, "\n") != strings.Count(ru, "\n") {
				t.Error("message line structure differs")
			}
			if strings.Count(en, "```") != strings.Count(ru, "```") {
				t.Error("code fences differ")
			}
		})
	}
}

func TestRussianMessageFormatting(t *testing.T) {
	i := NewI18n(LangRussian)
	got := i.Tf(MsgModelSwitchSession, "model-a", " (reason)", "model-b")
	for _, want := range []string{"`model-a`", " (reason)", "`model-b`", "`/model model-a`"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "%!") {
		t.Fatal(got)
	}
	if got := i.Tf(MsgReplyFooterRemaining, 42); got != "осталось 42%" {
		t.Fatal(got)
	}
}

func TestRussianPromptsPreserveToolSyntax(t *testing.T) {
	flags := regexp.MustCompile(`--[a-z][a-z-]*`)
	for _, key := range []MsgKey{MsgAgentSendToolPrompt, MsgAgentCronToolPrompt, MsgAgentTimerToolPrompt, MsgAgentRelayToolPrompt} {
		en, ru := messages[key][LangEnglish], messages[key][LangRussian]
		for _, flag := range flags.FindAllString(en, -1) {
			if strings.Count(en, flag) != strings.Count(ru, flag) {
				t.Errorf("%s: flag %s changed", key, flag)
			}
		}
		for _, token := range []string{"cc-connect", "CC_PROJECT", "CC_SESSION_KEY", "NO_REPLY", "reuse", "new-per-run", "cron_expr", "timeout_mins", "true / false", "df -h", "ipconfig", "2026-05-16T09:00", "0 9 * * *", "4 19 14 6 *"} {
			if strings.Count(en, token) != strings.Count(ru, token) {
				t.Errorf("%s: literal %q changed", key, token)
			}
		}
	}
	for key, tokens := range map[MsgKey][]string{
		MsgHelp:                 {"/shell [--timeout <sec>]", "!cmd", "[[commands]]", "[[aliases]]", "default / edit / plan / yolo", "allow_from", "admin_from"},
		MsgProviderAddUsage:     {`"api_key"`, `"base_url"`, `"model"`},
		MsgCommandsAddExecUsage: {"--work-dir", "{{args}}"},
		MsgCommandsAddUsage:     {"{{1}}"},
		MsgPermissionPrompt:     {"**разрешить**", "**отклонить**", "**разрешить всё**", "без подтверждения"},
	} {
		for _, token := range tokens {
			if !strings.Contains(messages[key][LangRussian], token) {
				t.Errorf("%s: missing %q", key, token)
			}
		}
	}
}

func TestRussianNormalizationAndDetection(t *testing.T) {
	for _, alias := range []string{"ru", "russian", "русский", "RU", "Русский", " russian "} {
		if got := NormalizeLanguageString(alias); got != LangRussian {
			t.Errorf("NormalizeLanguageString(%q) = %q", alias, got)
		}
	}
	for _, text := range []string{"Привет", "Ёжик", "Проверь core/i18n.go", "Разрешить всё"} {
		if got := DetectLanguage(text); got != LangRussian {
			t.Errorf("DetectLanguage(%q) = %q", text, got)
		}
	}
	// Shared Cyrillic is inherently ambiguous; distinctive letters must not
	// be classified as Russian. No unsupported Ukrainian locale is invented.
	for _, text := range []string{"Привіт", "Це українська мова", "Є питання", "Ґрунт", "Беларуская мова: ў"} {
		if got := DetectLanguage(text); got == LangRussian {
			t.Errorf("misclassified %q as Russian", text)
		}
	}
}

func TestRussianAutoDetectionSurvivesTechnicalReplies(t *testing.T) {
	i := NewI18n(LangAuto)
	i.DetectAndSet("Привет, проверь проект")
	for _, text := range []string{"", "123", "ok", "allow all", "/status", "go test ./...", "git diff HEAD", "npm run build", "cargo build release", "return nil nil", "fmt.Println(42)", "```go\nreturn nil\n```", "https://example.com"} {
		i.DetectAndSet(text)
		if i.CurrentLang() != LangRussian {
			t.Fatalf("%q reset Russian detection", text)
		}
	}
	i.DetectAndSet("Please explain this function")
	if i.CurrentLang() != LangEnglish {
		t.Fatal("plain English sentence should switch the automatic language")
	}
	for _, explicit := range []Language{LangRussian, LangEnglish, LangChinese} {
		i.SetLang(explicit)
		for _, text := range []string{"Привет", "Hello world", "你好", "¿Cómo estás?"} {
			i.DetectAndSet(text)
			if i.CurrentLang() != explicit {
				t.Fatalf("explicit %s overridden by %q", explicit, text)
			}
		}
	}
}

func TestRussianI18nConcurrentAccess(t *testing.T) {
	i := NewI18n(LangAuto)
	i.SetSaveFunc(func(Language) error { return nil })
	var wg sync.WaitGroup
	for g := 0; g < 12; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				switch g % 4 {
				case 0:
					i.DetectAndSet("Привет")
					i.DetectAndSet("git diff HEAD")
				case 1:
					i.SetLang(LangRussian)
					i.SetLang(LangAuto)
				case 2:
					_ = i.Tf(MsgError, fmt.Errorf("test"))
					_ = i.CurrentLang()
				case 3:
					_ = FormatDoctorResults([]DoctorCheckResult{{Name: "CPU", Status: DoctorPass}}, i)
				}
			}
		}(g)
	}
	wg.Wait()
}

func TestRussianAgentSystemPromptComposition(t *testing.T) {
	got := AgentSystemPromptForLang(LangRussian)
	if !strings.HasPrefix(got, agentSystemPromptHeader) || !strings.HasSuffix(got, agentSystemPromptFooter) {
		t.Fatal("shared system prompt header/footer changed")
	}
	for _, key := range []MsgKey{MsgAgentSendToolPrompt, MsgAgentCronToolPrompt, MsgAgentTimerToolPrompt, MsgAgentRelayToolPrompt} {
		if !strings.Contains(got, messages[key][LangRussian]) {
			t.Errorf("system prompt missing Russian section %s", key)
		}
	}
}
