package core

import (
	"fmt"
	"testing"
)

type localizedStartupPlatform struct {
	stubPlatformEngine
	i18n *I18n
}

func (p *localizedStartupPlatform) SetI18n(i18n *I18n) { p.i18n = i18n }

func (p *localizedStartupPlatform) Start(MessageHandler) error {
	if p.i18n == nil || p.i18n.CurrentLang() != LangSpanish {
		return fmt.Errorf("platform language was not supplied before startup")
	}
	return nil
}

func TestEngineStart_SharesLanguageWithPlatformNotices(t *testing.T) {
	p := &localizedStartupPlatform{stubPlatformEngine: stubPlatformEngine{n: "localized"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangSpanish)
	defer e.Stop()
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	e.i18n.SetLang(LangJapanese)
	if p.i18n.CurrentLang() != LangJapanese {
		t.Fatal("platform notices did not follow runtime language changes")
	}
}
