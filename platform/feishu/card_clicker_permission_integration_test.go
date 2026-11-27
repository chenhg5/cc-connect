package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

// Only external boundaries are stubbed: callbacks use the real Feishu adapter,
// engine registration, role lookup, authorization and card rendering. The bare
// adapter has no SDK client, credentials, transport or running connection.
type cardClickerAgent struct {
	providers []core.ProviderConfig
	active    string
	workDir   string
}

func (a *cardClickerAgent) Name() string { return "card-test" }
func (a *cardClickerAgent) StartSession(context.Context, string) (core.AgentSession, error) {
	return nil, fmt.Errorf("unexpected agent start in card callback test")
}
func (a *cardClickerAgent) ListSessions(context.Context) ([]core.AgentSessionInfo, error) {
	return nil, nil
}
func (a *cardClickerAgent) Stop() error                                  { return nil }
func (a *cardClickerAgent) SetProviders(providers []core.ProviderConfig) { a.providers = providers }
func (a *cardClickerAgent) ListProviders() []core.ProviderConfig         { return a.providers }
func (a *cardClickerAgent) GetActiveProvider() *core.ProviderConfig {
	for i := range a.providers {
		if a.providers[i].Name == a.active {
			return &a.providers[i]
		}
	}
	return nil
}
func (a *cardClickerAgent) SetActiveProvider(name string) bool {
	for _, provider := range a.providers {
		if provider.Name == name {
			a.active = name
			return true
		}
	}
	return false
}
func (a *cardClickerAgent) GetWorkDir() string    { return a.workDir }
func (a *cardClickerAgent) SetWorkDir(dir string) { a.workDir = dir }

func clickPermissionCard(t *testing.T, p *Platform, userID, action, sessionKey string) map[string]any {
	t.Helper()
	return clickPermissionCardCallback(t, p, userID, &callback.CallBackAction{Value: map[string]any{
		"action": action, "session_key": sessionKey,
	}})
}

func clickPermissionCardCallback(t *testing.T, p *Platform, userID string, action *callback.CallBackAction) map[string]any {
	t.Helper()
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: userID},
		Action:   action,
		Context:  &callback.Context{OpenChatID: "oc_shared", OpenMessageID: "om_shared_card"},
	}}
	resp, err := p.onCardAction(event)
	if err != nil || resp == nil || resp.Card == nil {
		t.Fatalf("callback %#v by %s: response = %#v, error = %v", action, userID, resp, err)
	}
	data, err := json.Marshal(resp.Card.Data)
	if err != nil {
		t.Fatal(err)
	}
	var card map[string]any
	if err := json.Unmarshal(data, &card); err != nil {
		t.Fatal(err)
	}
	return card
}

// Use the matched control's own callback payload. Select options are sent in
// Action.Option, with session_key from that select's Value, not another button.
func permissionCardCallback(t *testing.T, card map[string]any, action, wantKey string) *callback.CallBackAction {
	t.Helper()
	var found *callback.CallBackAction
	var visit func(any)
	visit = func(value any) {
		if found != nil {
			return
		}
		switch v := value.(type) {
		case map[string]any:
			payload, _ := v["value"].(map[string]any)
			if payload["action"] == action {
				found = &callback.CallBackAction{Value: payload}
				return
			}
			if v["tag"] == "select_static" {
				options, _ := v["options"].([]any)
				for _, rawOption := range options {
					option, _ := rawOption.(map[string]any)
					if option["value"] == action {
						found = &callback.CallBackAction{Value: payload, Option: action}
						return
					}
				}
			}
			for _, child := range v {
				visit(child)
			}
		case []any:
			for _, child := range v {
				visit(child)
			}
		}
	}
	visit(card)
	if found == nil {
		t.Fatalf("rendered card has no action %q", action)
	}
	if key, _ := found.Value["session_key"].(string); key != wantKey {
		t.Fatalf("action %q has session key %q, want %q", action, key, wantKey)
	}
	return found
}

func permissionCardContains(t *testing.T, card map[string]any, text string) bool {
	t.Helper()
	data, err := json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(string(data), text)
}

func TestCardAction_SharedCardPermissionsFollowActualClicker(t *testing.T) {
	for _, mode := range []string{"per-user", "shared-channel", "thread"} {
		t.Run(mode, func(t *testing.T) {
			p := &Platform{platformName: "feishu", shareSessionInChannel: mode == "shared-channel", threadIsolation: mode == "thread"}
			initial, target, dataDir := t.TempDir(), t.TempDir(), t.TempDir()
			agent := &cardClickerAgent{providers: []core.ProviderConfig{{Name: "a"}, {Name: "b"}}, active: "a", workDir: initial}
			// The engine does not start the adapter or any process.
			e := core.NewEngine("card-test", agent, nil, filepath.Join(dataDir, "sessions.json"), core.LangEnglish)
			t.Cleanup(func() { _ = e.Stop() })
			roles := core.NewUserRoleManager()
			roles.Configure("", []core.RoleInput{
				{Name: "admin", UserIDs: []string{"ou_admin"}},
				{Name: "member", UserIDs: []string{"ou_member"}, DisabledCommands: []string{"provider"}},
			})
			e.SetUserRoles(roles)
			e.SetAdminFrom("ou_admin")
			history := core.NewDirHistory(dataDir)
			history.Add("card-test", target)
			history.Add("card-test", initial)
			e.SetDirHistory(history)
			e.OnPlatformReady(p)
			ownerKey := func(owner string) string {
				switch mode {
				case "shared-channel":
					return "feishu:oc_shared"
				case "thread":
					return "feishu:oc_shared:root:om_root"
				default:
					return "feishu:oc_shared:" + owner
				}
			}

			// An admin creates a provider card. A member clicking that exact
			// card must neither navigate into provider controls nor switch it.
			adminHelp := clickPermissionCard(t, p, "ou_admin", "nav:/help agent", ownerKey("ou_admin"))
			adminKey := ownerKey("ou_admin")
			providerNav := permissionCardCallback(t, adminHelp, "nav:/provider", adminKey)
			adminProvider := clickPermissionCardCallback(t, p, "ou_admin", providerNav)
			for _, action := range []string{"nav:/provider", "act:/provider b"} {
				control := providerNav
				if strings.HasPrefix(action, "act:") {
					control = permissionCardCallback(t, adminProvider, action, adminKey)
				}
				denied := clickPermissionCardCallback(t, p, "ou_member", control)
				if !permissionCardContains(t, denied, "Command `/provider` is disabled") || agent.active != "a" {
					t.Fatalf("member used admin's provider card: action = %s, provider = %q", action, agent.active)
				}
			}

			// A member creates help without a provider button. An admin clicks
			// its tab, receives admin controls, and switches the provider.
			memberHelp := clickPermissionCard(t, p, "ou_member", "nav:/help agent", ownerKey("ou_member"))
			if permissionCardContains(t, memberHelp, "nav:/provider") {
				t.Fatal("member's help exposed disabled provider controls")
			}
			memberKey := ownerKey("ou_member")
			control := permissionCardCallback(t, memberHelp, "nav:/help agent", memberKey)
			adminHelp = clickPermissionCardCallback(t, p, "ou_admin", control)
			control = permissionCardCallback(t, adminHelp, "nav:/provider", memberKey)
			adminProvider = clickPermissionCardCallback(t, p, "ou_admin", control)
			control = permissionCardCallback(t, adminProvider, "act:/provider b", memberKey)
			clickPermissionCardCallback(t, p, "ou_admin", control)
			if agent.active != "b" {
				t.Fatal("admin clicking member's card was not evaluated as admin")
			}

			// Neither role disables /dir: these assertions independently prove
			// admin_from is enforced using the clicker, before side effects.
			adminHelp = clickPermissionCard(t, p, "ou_admin", "nav:/help system", adminKey)
			dirNav := permissionCardCallback(t, adminHelp, "nav:/dir", adminKey)
			adminDir := clickPermissionCardCallback(t, p, "ou_admin", dirNav)
			for _, action := range []string{"nav:/dir", "act:/dir select 2"} {
				control = dirNav
				if strings.HasPrefix(action, "act:") {
					control = permissionCardCallback(t, adminDir, action, adminKey)
				}
				denied := clickPermissionCardCallback(t, p, "ou_member", control)
				if !permissionCardContains(t, denied, "requires admin privilege") || agent.workDir != initial {
					t.Fatalf("member used admin's directory card: action = %s, directory = %q", action, agent.workDir)
				}
			}
			memberHelp = clickPermissionCard(t, p, "ou_member", "nav:/help system", ownerKey("ou_member"))
			control = permissionCardCallback(t, memberHelp, "nav:/dir", memberKey)
			adminDir = clickPermissionCardCallback(t, p, "ou_admin", control)
			control = permissionCardCallback(t, adminDir, "act:/dir select 2", memberKey)
			clickPermissionCardCallback(t, p, "ou_admin", control)
			if agent.workDir != target {
				t.Fatalf("admin clicking member's card: directory = %q, want %q", agent.workDir, target)
			}
		})
	}
}
