package core

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type sharedPermissionTestSession struct {
	*sharedTestSession
	updateErr          error
	readErrAfterUpdate error
}

type sharedApprovalDefaultsTestAgent struct {
	*sharedPermissionPresetTestAgent
	approval string
}

func (*sharedApprovalDefaultsTestAgent) ApprovalModes() []PermissionModeInfo {
	return []PermissionModeInfo{{Key: "default", NameKey: MsgApprovalInherit}, {Key: "request", NameKey: MsgPermAsk}, {Key: "auto-review", NameKey: MsgPermAutoReview}}
}
func (a *sharedApprovalDefaultsTestAgent) GetApprovalMode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.approval == "" {
		return "default"
	}
	return a.approval
}
func (a *sharedApprovalDefaultsTestAgent) SetApprovalMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.approval = mode
}
func (a *sharedApprovalDefaultsTestAgent) SetMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = mode
	a.approval = "default"
}

func (*sharedPermissionTestSession) CurrentPermissionModes() []PermissionModeInfo {
	return []PermissionModeInfo{{Key: "ask-for-approval", NameKey: MsgPermAsk}, {Key: "approve-for-me", NameKey: MsgPermAutoReview}}
}

func (*sharedPermissionTestSession) CurrentApprovalModes() []PermissionModeInfo {
	return []PermissionModeInfo{{Key: "request", NameKey: MsgPermAsk}, {Key: "auto-review", NameKey: MsgPermAutoReview}}
}

func (s *sharedPermissionTestSession) SetCurrentApprovalMode(_ context.Context, mode string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.updateErr != nil {
		return s.updateErr
	}
	if s.turn != "" {
		return ErrAgentTurnBusy
	}
	s.settings.ApprovalPolicy = "on-request"
	s.settings.ApprovalsReviewer = "user"
	if mode == "auto-review" {
		s.settings.ApprovalsReviewer = "auto_review"
	}
	s.settingsErr = s.readErrAfterUpdate
	return nil
}

func (s *sharedPermissionTestSession) SetCurrentPermissionMode(_ context.Context, mode string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.updateErr != nil {
		return s.updateErr
	}
	if s.turn != "" {
		return ErrAgentTurnBusy
	}
	s.settings.PermissionProfile = ":workspace"
	s.settings.ApprovalPolicy = "on-request"
	s.settings.Sandbox = "workspace-write"
	s.settings.ApprovalsReviewer = "user"
	if mode == "approve-for-me" {
		s.settings.ApprovalsReviewer = "auto_review"
	}
	s.settingsErr = s.readErrAfterUpdate
	return nil
}

func newSharedPermissionEnv(t *testing.T) (*sharedTestEnv, *sharedPermissionTestSession) {
	env, a, as := newSharedSettingsEnv(t)
	env.e.agent = &sharedApprovalDefaultsTestAgent{sharedPermissionPresetTestAgent: &sharedPermissionPresetTestAgent{a}}
	as.mu.Lock()
	as.turn = ""
	as.settings.PermissionProfile = ":read-only"
	as.settings.ApprovalsReviewer = "user"
	as.mu.Unlock()
	s := &sharedPermissionTestSession{sharedTestSession: as}
	env.e.interactiveMu.Lock()
	state := env.e.interactiveStates[env.e.interactiveKeyForSessionKey("test:user")]
	env.e.interactiveMu.Unlock()
	state.mu.Lock()
	state.agentSession = s
	state.mu.Unlock()
	return env, s
}

func TestCurrentPermissionMode_ReportsErrorsWithoutSuccess(t *testing.T) {
	for _, tc := range []struct {
		name          string
		updateErr     error
		turn          string
		readErr       error
		command, want string
	}{
		{name: "rejected", updateErr: fmt.Errorf("server rejected permissions"), command: "/mode current approve-for-me", want: "server rejected permissions"},
		{name: "busy", turn: "busy", command: "/mode current approve-for-me", want: "Wait for the current turn"},
		{name: "default", command: "/mode current default", want: "`default` applies only to new threads"},
		{name: "verification", readErr: fmt.Errorf("settings unavailable"), command: "/mode current approve-for-me", want: "Permissions update accepted, but reading"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, s := newSharedPermissionEnv(t)
			s.updateErr, s.readErrAfterUpdate = tc.updateErr, tc.readErr
			s.mu.Lock()
			s.turn = tc.turn
			s.mu.Unlock()
			env.send(tc.command)
			env.await(tc.want)
			if strings.Contains(env.visible(), "Current thread permissions updated.") {
				t.Fatal("reported verified success on error")
			}
		})
	}
}

func TestModeCard_CurrentPermissionPickerPreservesDefaultScope(t *testing.T) {
	env, _ := newSharedPermissionEnv(t)
	env.send("/mode")
	root := env.e.handleCardNav("nav:/mode", "test:user")
	foundEntry := false
	for _, element := range root.Elements {
		if actions, ok := element.(CardActions); ok {
			for _, button := range actions.Buttons {
				if button.Value == "nav:/mode current" {
					foundEntry = true
				}
			}
		}
	}
	if !foundEntry {
		t.Fatal("mode popup lacks current-permissions entry")
	}
	current := env.e.handleCardNav("nav:/mode current", "test:user")
	if current == nil || !strings.Contains(current.RenderText(), "Profile: :read-only") {
		t.Fatalf("current permission card=%+v", current)
	}
	foundOption := false
	for _, element := range current.Elements {
		if selectRow, ok := element.(CardSelect); ok {
			for _, option := range selectRow.Options {
				if option.Value == "act:/mode current approve-for-me" {
					foundOption = true
				}
				if option.Value == "act:/mode current default" {
					t.Fatal("current picker offered creation-only default")
				}
			}
		}
	}
	if !foundOption {
		t.Fatal("current permission picker lacks switch action")
	}
	updated := env.e.handleCardNav("act:/mode current approve-for-me", "test:user")
	if updated == nil || !strings.Contains(updated.RenderText(), "Reviewer: auto_review") || !strings.Contains(updated.RenderText(), "New-thread defaults are unchanged") {
		t.Fatalf("switch card=%+v", updated)
	}
	back := env.e.handleCardNav("nav:/mode", "test:user")
	if !strings.Contains(back.RenderText(), "Mode: suggest") {
		t.Fatal("current selection changed new-session default")
	}
	env.send("/history 10")
	env.await("KEEP HISTORY")
}

func TestModePopup_InlineButtonsIncludeCurrentPermissionsNavigation(t *testing.T) {
	env, _ := newSharedPermissionEnv(t)
	p := &stubInlineButtonPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	env.e.ReceiveMessage(p, &Message{SessionKey: "test:user", Platform: "test", UserID: "user", MessageID: "mode-inline", Content: "/mode", ReplyCtx: "reply"})
	found := false
	for _, row := range p.buttonRows {
		for _, button := range row {
			if button.Data == "cmd:/mode current" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("inline mode popup lacks current permissions button")
	}
}

func TestApprovalFlowPickers_PreserveCustomProfileAndKeepScopesSeparate(t *testing.T) {
	env, as := newSharedPermissionEnv(t)
	as.mu.Lock()
	as.settings.PermissionProfile = "custom-network"
	as.settings.Sandbox = "workspace-write"
	as.mu.Unlock()
	root := env.e.handleCardNav("nav:/mode", "test:user")
	current := env.e.handleCardNav("nav:/mode current", "test:user")
	for _, tc := range []struct {
		card   *Card
		action string
	}{{root, "act:/mode approval auto-review"}, {current, "act:/mode current approval auto-review"}} {
		found := false
		for _, element := range tc.card.Elements {
			if row, ok := element.(CardSelect); ok {
				for _, option := range row.Options {
					if option.Value == tc.action {
						found = true
					}
				}
			}
		}
		if !found {
			t.Fatalf("missing scoped approval picker action %s", tc.action)
		}
	}
	updated := env.e.handleCardNav("act:/mode current approval auto-review", "test:user")
	if !strings.Contains(updated.RenderText(), "Profile: custom-network · Approval: on-request · Reviewer: auto_review · Sandbox: workspace-write") {
		t.Fatalf("custom profile overwritten: %s", updated.RenderText())
	}
	defaults := env.e.handleCardNav("nav:/mode", "test:user")
	if !strings.Contains(defaults.RenderText(), "New-session approval override: default") {
		t.Fatal("current change modified creation override")
	}
	future := env.e.handleCardNav("act:/mode approval request", "test:user")
	if !strings.Contains(future.RenderText(), "Mode: suggest") || !strings.Contains(future.RenderText(), "New-session approval override: request") {
		t.Fatal("approval default changed profile preset")
	}
	unchanged := env.e.handleCardNav("nav:/mode current", "test:user")
	if !strings.Contains(unchanged.RenderText(), "Profile: custom-network · Approval: on-request · Reviewer: auto_review") {
		t.Fatal("creation override changed current permissions")
	}
	reset := env.e.handleCardNav("act:/mode full-access", "test:user")
	if !strings.Contains(reset.RenderText(), "Mode: full-access") || !strings.Contains(reset.RenderText(), "New-session approval override: default") {
		t.Fatal("complete preset retained old approval override")
	}
}
