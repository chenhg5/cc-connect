package core

import "strings"

// Resolve against the original input, before aliases, arguments and quoted
// context can obscure the language the user actually chose.
func inputLanguage(text string) Language {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "/") {
		text = strings.Fields(text)[0]
		name := strings.TrimPrefix(text, "/")
		for _, lang := range []Language{LangChinese, LangTraditionalChinese, LangJapanese, LangSpanish} {
			for _, translations := range localizedCommandNames {
				if name == translations[lang] {
					return lang
				}
			}
		}
	}
	return DetectLanguage(text)
}

func (e *Engine) resolveMessageLanguage(msg *Message) {
	e.i18n.mu.Lock()
	fixed := e.i18n.lang
	if fixed != LangAuto {
		msg.Language = fixed
	} else if msg.Language == LangAuto {
		msg.Language = inputLanguage(msg.Content)
	}
	if fixed == LangAuto {
		e.i18n.detected = msg.Language
	}
	e.i18n.mu.Unlock()
	e.messageLanguages.Store(msg.SessionKey, msg.Language)
}

func (e *Engine) messageI18n(msg *Message) *I18n {
	if msg.Language != LangAuto {
		return NewI18n(msg.Language)
	}
	return e.localI18n()
}

func (e *Engine) localI18n(locale ...*I18n) *I18n {
	if len(locale) > 0 && locale[0] != nil {
		return locale[0]
	}
	return NewI18n(e.i18n.CurrentLang())
}

func (e *Engine) stateI18n(state *interactiveState) *I18n {
	state.mu.Lock()
	lang := state.language
	state.mu.Unlock()
	if lang != LangAuto {
		return NewI18n(lang)
	}
	return e.localI18n()
}

func (e *Engine) sessionI18n(key string) *I18n {
	if lang, ok := e.messageLanguages.Load(key); ok {
		return NewI18n(lang.(Language))
	}
	return e.localI18n()
}

// Card navigation preserves the card's locale rather than detecting the
// canonical English command that the button carries.
func LocalizedCardAction(action string, lang Language) string {
	if lang == LangAuto {
		return action
	}
	prefix, body, ok := strings.Cut(action, ":")
	if !ok {
		return action
	}
	return prefix + ":locale=" + string(lang) + ";" + body
}
