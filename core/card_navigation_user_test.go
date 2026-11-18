package core

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// Keep the existing default-tab rendering test on the production navigation
// path without retaining a second, non-user-aware help renderer in production.
func (e *Engine) renderHelpCard() *Card {
	return e.handleCardNavForUser("nav:/help", "test:chat:user", "user")
}

func TestHandleCardNav_RoleDisabledCommandFollowsClicker(t *testing.T) {
	for _, sessionKey := range []string{"test:chat1", "test:chat1:root:thread1", "test:chat1:u_admin", "test:chat1:u_member"} {
		t.Run(sessionKey, func(t *testing.T) {
			agent := &stubProviderAgent{providers: []ProviderConfig{{Name: "a"}, {Name: "b"}}, active: "a"}
			e := NewEngine("test", agent, []Platform{&stubPlatformEngine{n: "test"}}, "", LangEnglish)
			urm := NewUserRoleManager()
			urm.Configure("", []RoleInput{
				{Name: "admin", UserIDs: []string{"u_admin"}},
				{Name: "member", UserIDs: []string{"u_member"}, DisabledCommands: []string{"provider"}},
			})
			e.SetUserRoles(urm)

			for _, action := range []string{"nav:/provider", "act:/provider b", "act:/provider/add-other"} {
				card := e.handleCardNavForUser(action, sessionKey, "u_member")
				if card == nil || !strings.Contains(card.RenderText(), "Command `/provider` is disabled") {
					t.Fatalf("%s: card = %v, want disabled notice", action, card)
				}
				if agent.active != "a" {
					t.Fatalf("%s: member switched provider to %q", action, agent.active)
				}
			}
			if _, ok := findCardAction(e.handleCardNavForUser("nav:/help agent", sessionKey, "u_member"), "nav:/provider"); ok {
				t.Fatal("member's help card should hide provider")
			}
			if _, ok := findCardAction(e.handleCardNavForUser("nav:/help agent", sessionKey, "u_admin"), "nav:/provider"); !ok {
				t.Fatal("admin's help card should include provider")
			}
			_ = e.handleCardNavForUser("act:/provider b", sessionKey, "u_admin")
			if agent.active != "b" {
				t.Fatalf("admin provider = %q, want b", agent.active)
			}
			_ = e.handleCardNavForUser("act:/provider a", sessionKey, "u_member")
			if agent.active != "b" {
				t.Fatalf("member switched provider after admin click to %q", agent.active)
			}
		})
	}
}

func TestHandleCardNav_PrivilegeFollowsClicker(t *testing.T) {
	for _, sessionKey := range []string{"test:chat1", "test:chat1:root:thread1", "test:chat1:u_admin", "test:chat1:u_member"} {
		t.Run(sessionKey, func(t *testing.T) {
			initial, target := t.TempDir(), t.TempDir()
			dataDir := t.TempDir()
			agent := &stubWorkDirAgent{workDir: initial}
			e := NewEngine("test", agent, nil, filepath.Join(dataDir, "sessions.json"), LangEnglish)
			t.Cleanup(func() { _ = e.Stop() })
			e.SetAdminFrom("u_admin")
			e.SetDirHistory(NewDirHistory(dataDir))
			e.dirHistory.Add("test", target)
			e.dirHistory.Add("test", initial)

			for _, action := range []string{"nav:/dir", "act:/dir select 2", "act:/dir reset", "act:/dir prev"} {
				card := e.handleCardNavForUser(action, sessionKey, "u_member")
				want := fmt.Sprintf(e.i18n.T(MsgAdminRequired), "/dir")
				if card == nil || !strings.Contains(card.RenderText(), want) {
					t.Fatalf("%s: card = %v, want admin-required notice", action, card)
				}
				if agent.workDir != initial {
					t.Fatalf("%s: member changed directory to %q", action, agent.workDir)
				}
			}
			card := e.handleCardNavForUser("act:/dir select 2", sessionKey, "u_admin")
			if card == nil || agent.workDir != target {
				t.Fatalf("admin click: card = %v, directory = %q, want %q", card, agent.workDir, target)
			}
			card = e.handleCardNavForUser("act:/dir select 2", sessionKey, "u_member")
			if card == nil || !strings.Contains(card.RenderText(), "requires admin privilege") || agent.workDir != target {
				t.Fatalf("member reused admin card: card = %v, directory = %q", card, agent.workDir)
			}
		})
	}
}

func TestHandleCardNav_PrivilegedSubcommandsUseClicker(t *testing.T) {
	e := NewEngine("test", &stubAgent{}, nil, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	e.SetAdminFrom("u_admin")
	for _, action := range []string{"nav:/commands addexec", "act:/commands addex", "nav:/cron addexec", "act:/cron addex"} {
		card := e.handleCardNavForUser(action, "test:chat1:u_admin", "u_member")
		if card == nil || !strings.Contains(card.RenderText(), "requires admin privilege") {
			t.Fatalf("%s: card = %v, want admin-required notice", action, card)
		}
	}
}

func TestHandleCardNav_LegacyCannotBorrowSessionOwnerIdentity(t *testing.T) {
	agent := &stubProviderAgent{providers: []ProviderConfig{{Name: "a"}, {Name: "b"}}, active: "a"}
	e := NewEngine("test", agent, nil, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	roles := NewUserRoleManager()
	roles.Configure("", []RoleInput{
		{Name: "admin", UserIDs: []string{"u_admin"}},
		{Name: "member", UserIDs: []string{"u_member"}, DisabledCommands: []string{"provider"}},
	})
	e.SetUserRoles(roles)
	card := e.handleCardNav("act:/provider b", "test:chat1:u_admin")
	if card == nil || !strings.Contains(card.RenderText(), e.i18n.T(MsgCardActorRequired)) || agent.active != "a" {
		t.Fatalf("legacy callback borrowed card owner's role: card = %v, provider = %q", card, agent.active)
	}
	e.SetUserRoles(nil)
	e.SetAdminFrom("*")
	card = e.handleCardNav("nav:/dir", "test:chat1:u_admin")
	if card == nil || !strings.Contains(card.RenderText(), e.i18n.T(MsgCardActorRequired)) {
		t.Fatalf("legacy callback without actor gained privilege: card = %v", card)
	}
}

// userAwareCardJourneyPlatform exposes both APIs to verify that the engine
// prefers the user-aware callback without changing legacy adapters.
type userAwareCardJourneyPlatform struct {
	stubCardPlatform
	navigation       UserAwareCardNavigationHandler
	legacyNavigation CardNavigationHandler
}

func (p *userAwareCardJourneyPlatform) SetUserAwareCardNavigationHandler(h UserAwareCardNavigationHandler) {
	p.navigation = h
}

func (p *userAwareCardJourneyPlatform) SetCardNavigationHandler(h CardNavigationHandler) {
	p.legacyNavigation = h
}

func TestCardNavigation_RegistrationPrefersClickerAwareHandler(t *testing.T) {
	p := &userAwareCardJourneyPlatform{stubCardPlatform: stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.onPlatformReady(p)
	if p.navigation == nil || p.legacyNavigation != nil {
		t.Fatal("engine must register the user-aware callback instead of the legacy callback")
	}
}

func TestCardNavigation_RegistrationSupportsLegacyHandler(t *testing.T) {
	p := &reasoningJourneyPlatform{stubCardPlatform: stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.onPlatformReady(p)
	if p.navigation == nil || p.navigation("nav:/help", "test:chat:user") == nil {
		t.Fatal("legacy adapter must keep receiving usable two-argument callbacks")
	}
}

func TestBridge_CardNavigationUsesCommandIdentity(t *testing.T) {
	bs := NewBridgeServerInsecure(0, "", "/bridge/ws", nil)
	bp := bs.NewPlatform("test")
	e := NewEngine("test", &stubAgent{}, []Platform{bp}, "", LangEnglish)
	bs.RegisterEngine("test", e, bp)
	var gotAction, gotKey, gotUserID string
	bp.SetUserAwareCardNavigationHandler(func(action, sessionKey, userID string) *Card {
		gotAction, gotKey, gotUserID = action, sessionKey, userID
		return nil
	})
	a := &bridgeAdapter{server: bs}
	raw := []byte(`{"type":"card_action","session_key":"bridge:shared","action":"nav:/help"}`)
	a.handleCardAction(raw)
	if gotAction != "nav:/help" || gotKey != "bridge:shared" || gotUserID != "web-admin" {
		t.Fatalf("navigation = (%q, %q, %q), want (nav:/help, bridge:shared, web-admin)", gotAction, gotKey, gotUserID)
	}
	// A later legacy registration must still replace the callback.
	gotAction, gotKey = "", ""
	bp.SetCardNavigationHandler(func(action, sessionKey string) *Card {
		gotAction, gotKey = action, sessionKey
		return nil
	})
	a.handleCardAction(raw)
	if gotAction != "nav:/help" || gotKey != "bridge:shared" {
		t.Fatalf("legacy navigation = (%q, %q)", gotAction, gotKey)
	}
}
