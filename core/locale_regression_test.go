package core

import (
	"errors"
	"strings"
	"testing"
)

func TestAutoLanguage_DetectionDoesNotPersist(t *testing.T) {
	i := NewI18n(LangAuto)
	saves := 0
	i.SetSaveFunc(func(Language) error { saves++; return nil })
	i.DetectAndSet("中文消息")
	i.DetectAndSet("English message")
	if saves != 0 {
		t.Fatalf("automatic detection persisted %d times; restart would lock the language", saves)
	}
}

func TestMessageLocale_PreservesCommandInputAndSnapshot(t *testing.T) {
	env := newCUJEnv(t)
	env.engine.i18n.SetLang(LangAuto)
	for _, tc := range []struct {
		text string
		lang Language
	}{
		{`/help D:/中文`, LangEnglish}, {"/帮助", LangChinese}, {"/語言", LangTraditionalChinese}, {"/ayuda", LangSpanish},
	} {
		msg := &Message{SessionKey: "alice", Content: tc.text, ExtraContent: "引用中文"}
		env.engine.resolveMessageLanguage(msg)
		if msg.Language != tc.lang {
			t.Errorf("%s => %s, want %s", tc.text, msg.Language, tc.lang)
		}
	}
	msg := &Message{SessionKey: "alice", Content: "请检查工具"}
	env.engine.resolveMessageLanguage(msg)
	snapshot := env.engine.messageI18n(msg)
	env.userSends("bob", "/help")
	if snapshot.CurrentLang() != LangChinese {
		t.Fatal("other chat changed the running task locale")
	}
	card := env.engine.handleCardNav(LocalizedCardAction("nav:/help sessions", LangChinese), "test:alice")
	if card == nil || card.Language != LangChinese || !strings.Contains(card.RenderText(), "会话") {
		t.Fatalf("card lost locale: %#v", card)
	}
}

func TestManualLanguage_OnlyExplicitChangesPersist(t *testing.T) {
	i := NewI18n(LangAuto)
	var saved []Language
	i.SetSaveFunc(func(l Language) error { saved = append(saved, l); return errors.New("test error") })
	i.DetectAndSet("中文")
	i.SetLang(LangChinese)
	i.SetLang(LangAuto)
	i.DetectAndSet("English")
	if len(saved) != 2 || saved[0] != LangChinese || saved[1] != LangAuto {
		t.Fatalf("saved %v", saved)
	}
}

func TestCUJ_L1_CommandsFollowInputAndManualOverride(t *testing.T) {
	env := newCUJEnv(t)
	env.engine.i18n.SetLang(LangAuto)
	for _, step := range []struct{ input, want string }{
		{"/帮助", "/帮助"},
		{"/help", "/help"},
		{"/语言 中文", "中文"},
		{"/help", "/帮助"},
		{"/语言 auto", "auto"},
		{"/help", "/help"},
	} {
		env.plat.clearSent()
		env.userSends("alice", step.input)
		if got := env.lastSent(); !strings.Contains(strings.ToLower(got), strings.ToLower(step.want)) {
			t.Errorf("%s reply = %q, missing %q", step.input, got, step.want)
		}
	}
}
