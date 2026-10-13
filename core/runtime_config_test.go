package core

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type runtimeConfigTestAgent struct {
	cujAgent
	configMu      sync.Mutex
	model         string
	effort        string
	discoverErr   error
	sessionEnv    []string
	discoveredEnv []string
}

func (a *runtimeConfigTestAgent) PrepareRuntimeConfig(context.Context) error {
	a.configMu.Lock()
	defer a.configMu.Unlock()
	a.discoveredEnv = append([]string(nil), a.sessionEnv...)
	return a.discoverErr
}
func (a *runtimeConfigTestAgent) SetSessionEnv(env []string) {
	a.configMu.Lock()
	defer a.configMu.Unlock()
	a.sessionEnv = append([]string(nil), env...)
}
func (a *runtimeConfigTestAgent) StartSession(ctx context.Context, id string) (AgentSession, error) {
	a.configMu.Lock()
	matches := strings.Join(a.discoveredEnv, "\n") == strings.Join(a.sessionEnv, "\n")
	a.configMu.Unlock()
	if !matches {
		return nil, errors.New("settings and chat received different environments")
	}
	return a.cujAgent.StartSession(ctx, id)
}

func (a *runtimeConfigTestAgent) SetRuntimeConfig(_ context.Context, category, value string) error {
	a.configMu.Lock()
	defer a.configMu.Unlock()
	if value == "denied" {
		return errors.New("selection denied")
	}
	switch category {
	case "model":
		if value != "smart" && value != "fast" {
			return errors.New("unknown model")
		}
		a.model = value
		a.effort = "low"
		if value == "fast" {
			a.effort = ""
		}
	case "thought_level":
		if a.model == "fast" || (value != "low" && value != "high") {
			return errors.New("unknown effort")
		}
		a.effort = value
	default:
		return errors.New("unsupported setting")
	}
	return nil
}
func (a *runtimeConfigTestAgent) SetModel(string) { panic("unacknowledged SetModel must not be used") }
func (a *runtimeConfigTestAgent) GetModel() string {
	a.configMu.Lock()
	defer a.configMu.Unlock()
	return a.model
}
func (a *runtimeConfigTestAgent) AvailableModels(context.Context) []ModelOption {
	return []ModelOption{{Name: "smart"}, {Name: "fast"}, {Name: "denied"}}
}
func (a *runtimeConfigTestAgent) SetReasoningEffort(string) { panic("unacknowledged reasoning setter") }
func (a *runtimeConfigTestAgent) GetReasoningEffort() string {
	a.configMu.Lock()
	defer a.configMu.Unlock()
	return a.effort
}
func (a *runtimeConfigTestAgent) AvailableReasoningEfforts() []string {
	a.configMu.Lock()
	defer a.configMu.Unlock()
	if a.model == "fast" {
		return nil
	}
	return []string{"low", "high"}
}
func (a *runtimeConfigTestAgent) SetMode(string)                        { panic("unacknowledged mode setter") }
func (a *runtimeConfigTestAgent) GetMode() string                       { return "" }
func (a *runtimeConfigTestAgent) PermissionModes() []PermissionModeInfo { return nil }

func newRuntimeConfigEngine(t *testing.T) (*Engine, *runtimeConfigTestAgent) {
	t.Helper()
	a := &runtimeConfigTestAgent{model: "smart", effort: "low"}
	e := NewEngine("config", a, nil, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	t.Cleanup(func() { e.Stop() })
	return e, a
}
func TestRuntimeConfigCards_StableValuesAndAcknowledgedErrors(t *testing.T) {
	e, a := newRuntimeConfigEngine(t)
	model := e.handleCardNav("nav:/model", "test:alice")
	for _, el := range model.Elements {
		if sel, ok := el.(CardSelect); ok {
			if sel.Options[0].Value != "act:/model switch smart" {
				t.Fatalf("unstable selector: %+v", sel)
			}
		}
	}
	r := e.handleCardNav("act:/reasoning high", "test:alice")
	if !strings.Contains(r.RenderText(), "high") {
		t.Fatal(r.RenderText())
	}
	r = e.handleCardNav("act:/model switch denied", "test:alice")
	if !strings.Contains(r.RenderText(), "selection denied") || a.GetModel() != "smart" {
		t.Fatal(r.RenderText())
	}
	r = e.handleCardNav("act:/model switch fast", "test:alice")
	if !strings.Contains(r.RenderText(), "fast") {
		t.Fatal(r.RenderText())
	}
	r = e.handleCardNav("act:/reasoning high", "test:alice")
	if !strings.Contains(r.RenderText(), e.i18n.T(MsgReasoningNotSupported)) {
		t.Fatal(r.RenderText())
	}
	for _, el := range r.Elements {
		if _, ok := el.(CardSelect); ok {
			t.Fatal("unsupported empty dropdown")
		}
	}
	r = e.handleCardNav("act:/mode auto", "test:alice")
	if !strings.Contains(r.RenderText(), e.i18n.T(MsgModeNotSupported)) {
		t.Fatal(r.RenderText())
	}
}
func TestRuntimeConfigDiscoveryErrorAndPersistenceRollback(t *testing.T) {
	e, a := newRuntimeConfigEngine(t)
	a.discoverErr = errors.New("backend unavailable")
	if text := e.renderReasoningCardForAgent(a).RenderText(); !strings.Contains(text, "backend unavailable") {
		t.Fatal(text)
	}
	a.discoverErr = nil
	_ = a.SetRuntimeConfig(context.Background(), "thought_level", "high")
	e.modelSaveFunc = func(string) error { return errors.New("read-only config") }
	if _, err := e.switchModelOnAgent(a, "fast", true); err == nil {
		t.Fatal("missing save error")
	}
	if a.GetModel() != "smart" || a.GetReasoningEffort() != "high" {
		t.Fatal("failed save changed current selection")
	}
}
