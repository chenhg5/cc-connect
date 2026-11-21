package codex

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

func normalizeManagedMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "", "default":
		return "default"
	case "ask-for-approval", "approve-for-me", "full-access", "read-only":
		return mode
	default:
		return normalizeMode(mode)
	}
}

func (s *managedSession) CurrentPermissionModes() []core.PermissionModeInfo {
	return s.agent.PermissionModes()[1:] // default means omission on creation, not a runtime reset
}

func (s *managedSession) SetCurrentPermissionMode(ctx context.Context, mode string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	valid := false
	for _, option := range s.CurrentPermissionModes() {
		if option.Key == mode {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("codex managed unsupported current permission mode %q", mode)
	}
	params := map[string]any{}
	applyManagedModeParams(params, mode)
	// Runtime updates accept a profile or sandboxPolicy, not thread/start's
	// sandbox mode string. Legacy presets select the equivalent built-in profile.
	if sandbox, ok := params["sandbox"].(string); ok {
		profiles := map[string]string{"read-only": ":read-only", "workspace-write": ":workspace", "danger-full-access": ":danger-full-access"}
		params["permissions"] = profiles[sandbox]
		delete(params, "sandbox")
	}
	return s.updateCurrentPermissionParams(ctx, params)
}

func (s *managedSession) CurrentApprovalModes() []core.PermissionModeInfo {
	return []core.PermissionModeInfo{
		{Key: "request", NameKey: core.MsgPermAsk},
		{Key: "auto-review", NameKey: core.MsgPermAutoReview},
	}
}

func (s *managedSession) SetCurrentApprovalMode(ctx context.Context, mode string) error {
	reviewer := ""
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "request":
		reviewer = "user"
	case "auto-review":
		reviewer = "auto_review"
	default:
		return fmt.Errorf("codex managed unsupported approval mode %q", mode)
	}
	// Deliberately omit permissions and sandboxPolicy to preserve custom profiles.
	return s.updateCurrentPermissionParams(ctx, map[string]any{"approvalPolicy": "on-request", "approvalsReviewer": reviewer})
}

func (s *managedSession) updateCurrentPermissionParams(ctx context.Context, params map[string]any) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	busy := s.turn != "" || len(s.pending) != 0
	params["threadId"] = s.thread
	s.mu.Unlock()
	if busy {
		return fmt.Errorf("codex managed permissions require an idle thread: %w", core.ErrAgentTurnBusy)
	}
	rpc, err := s.connection()
	if err != nil {
		return err
	}
	if err := rpc.request(ctx, "thread/settings/update", params, nil); err != nil {
		return fmt.Errorf("codex managed update current permissions: %w", err)
	}
	return nil
}

// Managed settings configure new threads; attached threads retain their settings.
func (a *managedAgent) SetMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = normalizeManagedMode(mode)
	a.approvalMode = "" // A complete preset must not retain an older partial override.
	slog.Info("codex: new thread permission default changed", "mode", a.mode)
}

func (a *managedAgent) ApprovalModes() []core.PermissionModeInfo {
	return []core.PermissionModeInfo{
		{Key: "default", NameKey: core.MsgApprovalInherit},
		{Key: "request", NameKey: core.MsgPermAsk},
		{Key: "auto-review", NameKey: core.MsgPermAutoReview},
	}
}

func (a *managedAgent) SetApprovalMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "default":
		a.approvalMode = ""
	case "request":
		a.approvalMode = "request"
	case "auto-review":
		a.approvalMode = "auto-review"
	default:
		slog.Warn("codex: unsupported approval default", "mode", mode)
	}
}

func (a *managedAgent) GetApprovalMode() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.approvalMode == "" {
		return "default"
	}
	return a.approvalMode
}

func (a *managedAgent) PermissionModes() []core.PermissionModeInfo {
	modes := []core.PermissionModeInfo{
		{Key: "default", NameKey: core.MsgPermDefault},
		{Key: "ask-for-approval", NameKey: core.MsgPermAsk},
		{Key: "approve-for-me", NameKey: core.MsgPermAutoReview},
		{Key: "full-access", NameKey: core.MsgPermFullAccess},
		{Key: "read-only", NameKey: core.MsgPermReadOnly},
	}
	// Keep legacy consumers and bilingual mode metadata compatible.
	en, zh := core.NewI18n(core.LangEnglish), core.NewI18n(core.LangChinese)
	for i := range modes {
		modes[i].Name = en.T(modes[i].NameKey)
		modes[i].NameZh = zh.T(modes[i].NameKey)
	}
	return append(modes, a.Agent.PermissionModes()...)
}

func applyManagedModeParams(params map[string]any, mode string) {
	mode = normalizeManagedMode(mode)
	profile, approval, reviewer := "", "on-request", "user"
	switch mode {
	case "default":
		return // Let the daemon resolve all permission defaults.
	case "ask-for-approval":
		profile = ":workspace"
	case "approve-for-me":
		profile, reviewer = ":workspace", "auto_review"
	case "full-access":
		profile, approval = ":danger-full-access", "never"
	case "read-only":
		profile = ":read-only"
	default:
		approval, sandbox := appServerModeSettings(mode)
		params["sandbox"] = sandbox
		params["approvalPolicy"] = approval
		params["approvalsReviewer"] = reviewer
		return
	}
	params["permissions"] = profile
	params["approvalPolicy"] = approval
	params["approvalsReviewer"] = reviewer
}
