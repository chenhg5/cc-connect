package core

import (
	"strings"
	"testing"
	"time"
)

func TestRussianLanguagePickers(t *testing.T) {
	e := newTestEngine()
	for _, alias := range []string{"ru", "russian", "русский", "РУССКИЙ"} {
		p := &stubPlatformEngine{n: "plain"}
		e.i18n.SetLang(LangEnglish)
		e.cmdLang(p, &Message{SessionKey: "test:ru"}, []string{alias})
		if e.i18n.CurrentLang() != LangRussian || !strings.Contains(strings.Join(p.getSent(), "\n"), "Русский") {
			t.Fatalf("/lang %s failed: %v", alias, p.getSent())
		}
		e.i18n.SetLang(LangEnglish)
		e.executeCardAction("/lang", alias, "test:ru")
		if e.i18n.CurrentLang() != LangRussian {
			t.Fatalf("card /lang %s failed", alias)
		}
	}
	p := &stubPlatformEngine{n: "plain"}
	e.cmdLang(p, &Message{SessionKey: "test:ru"}, nil)
	if !strings.Contains(strings.Join(p.getSent(), "\n"), "Русский: `/lang ru`") {
		t.Fatal("plain picker is missing Russian")
	}
	inline := &stubInlineButtonPlatform{stubPlatformEngine: stubPlatformEngine{n: "inline"}}
	e.cmdLang(inline, &Message{SessionKey: "test:ru"}, nil)
	found := false
	for _, row := range inline.buttonRows {
		for _, button := range row {
			if button.Data == "cmd:/lang ru" && button.Text == "Русский" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("inline picker is missing Russian")
	}
	found = false
	for _, element := range e.renderLangCard().Elements {
		if selectElement, ok := element.(CardSelect); ok {
			for _, option := range selectElement.Options {
				if option.Value == "act:/lang ru" && option.Text == "Русский" && selectElement.InitValue == option.Value {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("card picker is missing selected Russian option")
	}
}

func TestRussianPermissionResponsesAreExplicit(t *testing.T) {
	for _, response := range []string{"разрешить", "разрешить!", "разрешить?", " РАЗРЕШИТЬ ", "@bot_name разрешить", "разрешить @bot_name"} {
		if !isAllowResponse(response) || isApproveAllResponse(response) || isDenyResponse(response) {
			t.Errorf("invalid allow classification: %q", response)
		}
	}
	for _, response := range []string{"разрешить всё", "разрешить всё!", "разрешить всё?", "разрешить все", " РАЗРЕШИТЬ ВСЁ ", "@bot_name разрешить всё", "разрешить все @bot_name"} {
		if !isApproveAllResponse(response) || isDenyResponse(response) {
			t.Errorf("invalid approve-all classification: %q", response)
		}
	}
	if !isDenyResponse("отклонить!") || !isDenyResponse(" ОТКЛОНИТЬ ") || !isDenyResponse("@bot_name отклонить") || isAllowResponse("отклонить") {
		t.Fatal("Russian deny not recognized")
	}
	for _, response := range []string{"не разрешить", "@bot_name не разрешить", "не разрешить всё", "@bot_name не разрешить всё", "нельзя разрешить", "не разрешить всё?", "разрешить или отклонить", "разрешитьвсё", "разрешить все файлы", "@bot_name разрешить все файлы", "неразрешить", "да", "всё"} {
		if isAllowResponse(response) || isApproveAllResponse(response) {
			t.Errorf("unsafe Russian approval: %q", response)
		}
	}
}

func TestRussianPermissionResponseReachesAgent(t *testing.T) {
	for _, tc := range []struct {
		response, behavior string
		key                MsgKey
	}{
		{"разрешить", "allow", MsgPermissionAllowed},
		{"отклонить", "deny", MsgPermissionDenied},
		{"разрешить всё", "allow", MsgPermissionApproveAll},
		{"разрешить все", "allow", MsgPermissionApproveAll},
	} {
		t.Run(tc.response, func(t *testing.T) {
			e := newTestEngine()
			e.i18n.SetLang(LangRussian)
			p := &stubPlatformEngine{n: "plain"}
			recorder := &recordingAgentSession{}
			state := &interactiveState{agentSession: recorder, platform: p, pending: &pendingPermission{RequestID: "ru-request", ToolName: "Bash", Resolved: make(chan struct{})}}
			e.interactiveStates["test:ru"] = state
			msg := &Message{SessionKey: "test:ru", Content: tc.response}
			if !e.handlePendingPermission(p, msg, tc.response, msg.SessionKey) {
				t.Fatal("permission reply not handled")
			}
			if state.approveAll != isApproveAllResponse(tc.response) || state.pending != nil {
				t.Fatal("wrong session-wide permission state")
			}
			if recorder.calls != 1 || recorder.lastResult.Behavior != tc.behavior {
				t.Fatalf("agent permission response: %+v", recorder)
			}
			if !strings.Contains(strings.Join(p.getSent(), "\n"), e.i18n.T(tc.key)) {
				t.Fatalf("missing Russian confirmation: %v", p.getSent())
			}
		})
	}
}

func TestRussianPermissionButtons(t *testing.T) {
	e := newTestEngine()
	e.i18n.SetLang(LangRussian)
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "card"}}
	e.sendPermissionPrompt(p, "ctx", e.i18n.Tf(MsgPermissionPrompt, "Bash", "echo test"), "Bash", "echo test")
	if len(p.sentCards) != 1 {
		t.Fatal("missing permission card")
	}
	buttons := p.sentCards[0].CollectButtons()
	if len(buttons) < 2 || len(buttons[0]) < 2 || len(buttons[1]) < 1 {
		t.Fatalf("incomplete permission buttons: %+v", buttons)
	}
	if buttons[0][0].Text != "Разрешить" || buttons[0][0].Data != "perm:allow" || buttons[0][1].Text != "Отклонить" || buttons[0][1].Data != "perm:deny" || buttons[1][0].Data != "perm:allow_all" || buttons[1][0].Text != "Авторазрешение" {
		t.Fatalf("permission buttons: %+v", buttons)
	}
}

func TestRussianDiagnosticNamesAndOutput(t *testing.T) {
	for name := range checkNameZh {
		if got := localizeCheckName(name, LangRussian); got == name && name != "Git" && name != "SQLite3" && name != "HTTPS (Anthropic)" {
			t.Errorf("untranslated check: %s", name)
		}
	}
	for name, want := range map[string]string{"Agent CLI (test)": "CLI агента (test)", "Platform (test)": "Платформа (test)", "TestAgent Auth": "Авторизация TestAgent", "Custom probe": "Custom probe"} {
		if got := localizeCheckName(name, LangRussian); got != want {
			t.Errorf("%s: %s", name, got)
		}
	}
	if got := formatElapsed(time.Minute, true, LangRussian); !strings.Contains(got, "Выполняется") || !strings.Contains(got, "мин") {
		t.Error(got)
	}
	if got := formatDurationI18n(25*time.Hour, LangRussian); got != "1 д 1 ч 0 мин" {
		t.Error(got)
	}
	if got := usageWindowLabel(LangRussian, 18000); got != "Лимит за 5 ч" {
		t.Error(got)
	}
}

func TestRussianNegatedPermissionDoesNotReachAgent(t *testing.T) {
	e := newTestEngine()
	e.i18n.SetLang(LangRussian)
	p := &stubPlatformEngine{n: "plain"}
	recorder := &recordingAgentSession{}
	pending := &pendingPermission{RequestID: "ru-negative", ToolName: "Bash", Resolved: make(chan struct{})}
	state := &interactiveState{agentSession: recorder, platform: p, pending: pending}
	e.interactiveStates["test:ru"] = state
	for _, text := range []string{"не разрешить", "@bot_name не разрешить", "не разрешить всё", "@bot_name не разрешить всё", "разрешить или отклонить"} {
		msg := &Message{SessionKey: "test:ru", Content: text}
		e.handlePendingPermission(p, msg, text, msg.SessionKey)
		if recorder.calls != 0 || state.pending != pending || state.approveAll {
			t.Fatalf("ambiguous response granted permission: %q", text)
		}
	}
}
