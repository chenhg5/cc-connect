package core

import (
	"strings"
	"testing"
)

func TestLocalizedCommands_PreserveArgumentsAndEnglish(t *testing.T) {
	for command, translations := range localizedCommandNames {
		for lang, name := range translations {
			if got := normalizeLocalizedCommand("/" + name + " argument"); got != "/"+command+" argument" {
				t.Errorf("%s command %s resolves to %s", lang, name, got)
			}
		}
	}
	for _, tc := range []struct{ input, want string }{
		{`/模型 "gpt-6.1-sol"`, `/model "gpt-6.1-sol"`},
		{"/語言 繁體", "/lang 繁體"},
		{"/言語 日本語", "/lang 日本語"},
		{"/ayuda", "/help"},
		{"/暂停", "/stop"},
		{"/目录\t'D:\\My Project'", "/dir\t'D:\\My Project'"},
		{"请暂停一下任务", "请暂停一下任务"},
		{"/model gpt-6.1-sol", "/model gpt-6.1-sol"},
		{"/custom-command x", "/custom-command x"},
	} {
		if got := normalizeLocalizedCommand(tc.input); got != tc.want {
			t.Errorf("normalizeLocalizedCommand(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
	if help := NewI18n(LangChinese).T(MsgHelp); !strings.Contains(help, "/帮助") || !strings.Contains(help, "/推理") {
		t.Fatalf("Chinese help still advertises English commands: %s", help)
	}
	if help := NewI18n(LangEnglish).T(MsgHelp); !strings.Contains(help, "/help") || strings.Contains(help, "/帮助") {
		t.Fatal("English help changed")
	}
	if help := NewI18n(LangChinese).T(MsgHelp); !strings.Contains(help, ".claude/skills/<name>/SKILL.md") {
		t.Fatal("localizing command names changed a real skill path")
	}
}
