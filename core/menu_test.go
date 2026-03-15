package core

import (
	"context"
	"strings"
	"testing"
)

// stubMenuAgent implements ModelSwitcher and ModeSwitcher for testing.
type stubMenuAgent struct {
	model string
	mode  string
}

func (s *stubMenuAgent) Name() string { return "stub" }
func (s *stubMenuAgent) StartSession(_ context.Context, _ string) (AgentSession, error) {
	return nil, nil
}
func (s *stubMenuAgent) ListSessions(_ context.Context) ([]AgentSessionInfo, error) {
	return nil, nil
}
func (s *stubMenuAgent) Stop() error { return nil }

// ModelSwitcher
func (s *stubMenuAgent) GetModel() string { return s.model }
func (s *stubMenuAgent) SetModel(m string) { s.model = m }
func (s *stubMenuAgent) AvailableModels(_ context.Context) []ModelOption {
	return []ModelOption{{Name: "model-a"}, {Name: "model-b"}, {Name: "model-c"}}
}

// ModeSwitcher
func (s *stubMenuAgent) GetMode() string { return s.mode }
func (s *stubMenuAgent) SetMode(m string) { s.mode = m }
func (s *stubMenuAgent) PermissionModes() []PermissionModeInfo {
	return []PermissionModeInfo{
		{Key: "default", Name: "Default"},
		{Key: "yolo", Name: "YOLO"},
	}
}

func newMenuTestEngine(t *testing.T) *Engine {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	agent := &stubMenuAgent{model: "model-a", mode: "default"}
	e := &Engine{
		name:     "test",
		agent:    agent,
		sessions: NewSessionManager(""),
		i18n:     NewI18n(LangEnglish),
		ctx:      ctx,
	}
	e.interactiveStates = make(map[string]*interactiveState)
	return e
}

func TestHandleMenuNavigation_Main(t *testing.T) {
	e := newMenuTestEngine(t)
	page := e.handleMenuNavigation("menu:main", "telegram:123:456")
	if page == nil {
		t.Fatal("expected non-nil MenuPage for menu:main")
	}
	if page.Title == "" {
		t.Error("expected non-empty Title")
	}
	if len(page.Buttons) == 0 {
		t.Error("expected buttons in main menu")
	}
}

func TestHandleMenuNavigation_Category(t *testing.T) {
	e := newMenuTestEngine(t)
	for _, cat := range []string{"session", "ai", "task", "system", "advanced"} {
		page := e.handleMenuNavigation("menu:cat:"+cat, "telegram:123:456")
		if page == nil {
			t.Fatalf("expected non-nil page for category %q", cat)
		}
		// Last button row should be back button pointing to menu:main
		rows := page.Buttons
		if len(rows) == 0 {
			t.Fatalf("category %q: expected buttons", cat)
		}
		lastRow := rows[len(rows)-1]
		if lastRow[0].Data != "menu:main" {
			t.Errorf("category %q: last button should be back, got %q", cat, lastRow[0].Data)
		}
	}
}

func TestHandleMenuNavigation_ListModel(t *testing.T) {
	e := newMenuTestEngine(t)
	page := e.handleMenuNavigation("menu:list:model:0", "telegram:123:456")
	if page == nil {
		t.Fatal("expected non-nil page for model list")
	}
	// 3 models fit on 1 page; expect 3 item rows + 1 navigation row
	if len(page.Buttons) != 4 {
		t.Errorf("expected 4 button rows (3 items + nav), got %d", len(page.Buttons))
	}
	// First item should have ✅ since model-a is current
	if len(page.Buttons[0]) == 0 {
		t.Fatal("first row empty")
	}
	if !strings.Contains(page.Buttons[0][0].Text, "✅") {
		t.Error("expected ✅ on current model")
	}
}

func TestHandleMenuNavigation_Noop(t *testing.T) {
	e := newMenuTestEngine(t)
	page := e.handleMenuNavigation("menu:noop", "telegram:123:456")
	if page != nil {
		t.Error("expected nil page for menu:noop")
	}
}

func TestHandleMenuNavigation_SelModel(t *testing.T) {
	e := newMenuTestEngine(t)
	agent := e.agent.(*stubMenuAgent)
	// Select model-b (index 2, 1-based)
	page := e.handleMenuNavigation("menu:sel:model:2", "telegram:123:456")
	if page == nil {
		t.Fatal("expected main menu page after selection")
	}
	if agent.model != "model-b" {
		t.Errorf("expected model-b after sel, got %q", agent.model)
	}
}
