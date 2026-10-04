package core

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type sharedSettingsTestAgent struct {
	sharedTestAgent
	model, effort, mode string
	providers           []ProviderConfig
	provider            string
}

func (*sharedSettingsTestAgent) DefaultSettingsOnly() bool { return true }
func (a *sharedSettingsTestAgent) SetModel(v string)       { a.mu.Lock(); defer a.mu.Unlock(); a.model = v }
func (a *sharedSettingsTestAgent) GetModel() string        { a.mu.Lock(); defer a.mu.Unlock(); return a.model }
func (*sharedSettingsTestAgent) AvailableModels(context.Context) []ModelOption {
	return []ModelOption{{Name: "default-model", Alias: "default"}, {Name: "new-model", Alias: "new"}}
}
func (a *sharedSettingsTestAgent) SetReasoningEffort(v string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.effort = v
}
func (a *sharedSettingsTestAgent) GetReasoningEffort() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.effort
}
func (*sharedSettingsTestAgent) AvailableReasoningEfforts() []string { return []string{"low", "high"} }
func (a *sharedSettingsTestAgent) SetMode(v string)                  { a.mu.Lock(); defer a.mu.Unlock(); a.mode = v }
func (a *sharedSettingsTestAgent) GetMode() string                   { a.mu.Lock(); defer a.mu.Unlock(); return a.mode }
func (*sharedSettingsTestAgent) PermissionModes() []PermissionModeInfo {
	return []PermissionModeInfo{{Key: "suggest", Name: "Suggest"}, {Key: "yolo", Name: "Yolo"}}
}
func (a *sharedSettingsTestAgent) ListProviders() []ProviderConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]ProviderConfig(nil), a.providers...)
}
func (a *sharedSettingsTestAgent) SetProviders(v []ProviderConfig) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.providers = append([]ProviderConfig(nil), v...)
}
func (a *sharedSettingsTestAgent) SetActiveProvider(v string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, p := range a.providers {
		if p.Name == v {
			a.provider = v
			return true
		}
	}
	if v == "" {
		a.provider = v
		return true
	}
	return false
}
func (a *sharedSettingsTestAgent) GetActiveProvider() *ProviderConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, p := range a.providers {
		if p.Name == a.provider {
			copy := p
			return &copy
		}
	}
	return nil
}

func newSharedSettingsEnv(t *testing.T) (*sharedTestEnv, *sharedSettingsTestAgent, *sharedTestSession) {
	env := newSharedTestEnv(t)
	a := &sharedSettingsTestAgent{model: "default-model", effort: "low", mode: "suggest", providers: []ProviderConfig{{Name: "default-provider"}, {Name: "new-provider"}}, provider: "default-provider"}
	env.e.agent = a
	env.send("/attach first")
	env.await("Attached to session first")
	as := a.connection("first")
	as.mu.Lock()
	as.settings = AgentRuntimeSettings{Model: "server-model", ReasoningEffort: "medium", Provider: "server-provider", ApprovalPolicy: "on-request", Sandbox: "read-only"}
	as.mu.Unlock()
	env.e.sessions.GetOrCreateActive("test:user").AddHistory("user", "KEEP HISTORY")
	return env, a, as
}

func assertSharedSettingsPreserved(t *testing.T, env *sharedTestEnv, as *sharedTestSession) {
	t.Helper()
	session := env.e.sessions.GetOrCreateActive("test:user")
	if session.GetAgentSessionID() != "first" {
		t.Fatal("settings command cleared attached thread ID")
	}
	if history := session.GetHistory(0); len(history) != 1 || history[0].Content != "KEEP HISTORY" {
		t.Fatalf("settings command cleared history: %v", history)
	}
	state := as.RuntimeState()
	if !state.Connected || state.TurnID != "active-first" {
		t.Fatalf("settings command changed attached runtime: %+v", state)
	}
	key := env.e.interactiveKeyForSessionKey("test:user")
	env.e.interactiveMu.Lock()
	live := env.e.interactiveStates[key]
	env.e.interactiveMu.Unlock()
	if live == nil {
		t.Fatal("settings command removed event reader")
	}
}

func TestSharedSettingsTextCommandsPreserveObserverHistoryAndThread(t *testing.T) {
	for _, command := range []string{"/model switch new", "/reasoning high", "/mode yolo", "/provider switch new-provider", "/provider clear"} {
		t.Run(command, func(t *testing.T) {
			env, agent, as := newSharedSettingsEnv(t)
			env.send(command)
			env.await("New-thread defaults updated")
			env.await("Model: server-model · Reasoning: medium · Provider: server-provider")
			assertSharedSettingsPreserved(t, env, as)
			switch command {
			case "/model switch new":
				if agent.GetModel() != "new-model" {
					t.Fatal("model default did not change")
				}
			case "/reasoning high":
				if agent.GetReasoningEffort() != "high" {
					t.Fatal("reasoning default did not change")
				}
			case "/mode yolo":
				if agent.GetMode() != "yolo" {
					t.Fatal("mode default did not change")
				}
			case "/provider switch new-provider":
				if agent.GetActiveProvider().Name != "new-provider" {
					t.Fatal("provider default did not change")
				}
			case "/provider clear":
				if agent.GetActiveProvider() != nil {
					t.Fatal("provider default not cleared")
				}
			}
			as.emit(Event{Type: EventText, Content: "LIVE AFTER " + command, Metadata: map[string]any{"phase": "commentary"}})
			env.await("LIVE AFTER " + command)
		})
	}
}

func TestSharedSettingsCardActionsPreserveObserverHistoryAndThread(t *testing.T) {
	for _, action := range []string{"act:/model switch 2", "act:/reasoning 2", "act:/mode yolo", "act:/provider new-provider", "act:/provider clear"} {
		t.Run(action, func(t *testing.T) {
			env, _, as := newSharedSettingsEnv(t)
			card := env.e.handleCardNav(action, "test:user")
			if card == nil || !strings.Contains(card.RenderText(), "New-thread defaults updated") || !strings.Contains(card.RenderText(), "server-model") {
				t.Fatalf("ambiguous card feedback: %+v", card)
			}
			assertSharedSettingsPreserved(t, env, as)
		})
	}
}

func TestSharedSettingsInvalidInputAndPersistenceFailureLeaveDefaultsUnchanged(t *testing.T) {
	env, a, as := newSharedSettingsEnv(t)
	for _, command := range []string{"/reasoning invalid", "/mode invalid", "/provider switch missing"} {
		env.send(command)
	}
	env.e.providerSaveFunc = func(string) error { return fmt.Errorf("simulated save failure") }
	env.send("/provider switch new-provider")
	env.await("simulated save failure")
	if a.GetModel() != "default-model" || a.GetReasoningEffort() != "low" || a.GetMode() != "suggest" || a.GetActiveProvider().Name != "default-provider" {
		t.Fatal("invalid input changed defaults")
	}
	assertSharedSettingsPreserved(t, env, as)
}

func TestSharedSettingsReadFailureDoesNotPresentDefaultsAsRuntime(t *testing.T) {
	env, _, as := newSharedSettingsEnv(t)
	as.mu.Lock()
	as.settingsErr = fmt.Errorf("simulated read failure")
	as.mu.Unlock()
	env.send("/model")
	env.await("Could not read the attached thread settings")
	if strings.Contains(env.visible(), "Attached thread (reported by server)") {
		t.Fatal("read failure pretended defaults were runtime settings")
	}
	env.await("New-thread defaults (CC Connect)")
	assertSharedSettingsPreserved(t, env, as)
}

func TestSharedSettingsDoNotRestoreOldSessionProviderOverNewDefaults(t *testing.T) {
	env, a, _ := newSharedSettingsEnv(t)
	session := env.e.sessions.GetOrCreateActive("test:user")
	session.SetActiveProvider("default-provider")
	a.SetActiveProvider("new-provider")
	restoreActiveProviderFromSession(a, session)
	if a.GetActiveProvider().Name != "new-provider" {
		t.Fatal("stored provider for attached thread replaced new-thread default")
	}
}

func TestSharedSettingsPlainTextShowsRuntimeAndNumberedCreationDefaults(t *testing.T) {
	env, _, as := newSharedSettingsEnv(t)
	p := &stubPlatformEngine{n: "plain"}
	msg := &Message{SessionKey: "test:user", ReplyCtx: "reply"}
	env.e.cmdModel(p, msg, nil)
	visible := strings.Join(p.getSent(), "\n")
	for _, want := range []string{"Attached thread (reported by server)", "server-model", "New-thread defaults (CC Connect)", "1. default-model", "2. new-model"} {
		if !strings.Contains(visible, want) {
			t.Fatalf("plain query missing %q: %s", want, visible)
		}
	}
	assertSharedSettingsPreserved(t, env, as)
}

func TestSharedSettingsCardValidationErrorDoesNotChangeDefaults(t *testing.T) {
	env, a, as := newSharedSettingsEnv(t)
	for _, action := range []string{"act:/reasoning invalid", "act:/mode invalid", "act:/provider missing"} {
		card := env.e.handleCardNav(action, "test:user")
		if card == nil || !strings.Contains(card.RenderText(), "Error") {
			t.Fatalf("invalid card action did not show error: %v", card)
		}
	}
	if a.GetReasoningEffort() != "low" || a.GetMode() != "suggest" || a.GetActiveProvider().Name != "default-provider" {
		t.Fatal("invalid card action changed defaults")
	}
	assertSharedSettingsPreserved(t, env, as)
}
