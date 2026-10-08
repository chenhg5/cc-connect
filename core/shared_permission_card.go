package core

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// permissionCardState is optional presentation state, separate from the generic
// permission lifecycle. Its pointer is set before publishing a shared request.
type permissionCardState struct {
	mu       sync.Mutex // protects state and serializes edits to this message
	updater  CardMessageUpdater
	handle   any
	decision string
	resolved bool
	updated  bool
}

func (s *permissionCardState) wasUpdated() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updated
}

func (e *Engine) sharedDecisionLabel(decision string) (string, bool) {
	labels := map[string]MsgKey{"allow": MsgPermBtnAllow, "deny": MsgPermBtnDeny, "cancel": MsgSharedCancelDecision, "allow_session": MsgSharedAllowSession, "allow_similar": MsgSharedAllowSimilar, "network_allow": MsgSharedNetworkAllow, "network_deny": MsgSharedNetworkDeny}
	key, ok := labels[decision]
	if !ok {
		if i := strings.LastIndex(decision, "_"); i >= 0 {
			key, ok = labels[decision[:i]]
		}
	}
	if !ok {
		return "", false
	}
	return e.i18n.T(key), true
}

func (e *Engine) sharedPermissionBody(pending *pendingPermission) string {
	text := e.i18n.Tf(MsgSharedPermissionPrompt, pending.ToolName, truncateIf(pending.InputPreview, e.display.ToolMaxLen))
	if reason, _ := pending.ToolInput["reason"].(string); reason != "" && reason != pending.InputPreview {
		text += "\n\n" + e.i18n.Tf(MsgSharedApprovalReason, reason)
	}
	if cwd, _ := pending.ToolInput["cwd"].(string); cwd != "" {
		text += "\n\n" + e.i18n.Tf(MsgSharedApprovalCwd, cwd)
	}
	for _, decision := range pending.Decisions {
		if label, ok := e.sharedDecisionLabel(decision); ok {
			if details := pending.DecisionDetails[decision]; details != "" {
				text += "\n\n" + label + ": `" + details + "`"
			}
		}
	}
	return text
}

func (e *Engine) sharedResolvedPermissionCard(pending *pendingPermission, decision string) *Card {
	if decision == "" {
		return NewCard().Title(e.i18n.T(MsgSharedRequestResolved), "grey").Markdown(e.sharedPermissionBody(pending)).Build()
	}
	label, _ := e.sharedDecisionLabel(decision)
	icon, color := "✅ ", "green"
	if strings.HasPrefix(decision, "deny") || strings.HasPrefix(decision, "network_deny") {
		icon, color = "❌ ", "red"
	} else if decision == "cancel" {
		icon, color = "⏹ ", "grey"
	}
	return NewCard().Title(icon+label, color).Markdown(e.sharedPermissionBody(pending)).Build()
}

func (e *Engine) sendSharedPermissionCard(p Platform, reply any, pending *pendingPermission, card *Card) {
	updater, ok := p.(CardMessageUpdater)
	if !ok || pending.card == nil {
		e.sendWithCard(p, reply, card)
		return
	}
	if err := e.waitOutgoing(p); err != nil {
		slog.Warn("send shared permission card cancelled", "error", err)
		return
	}
	handle, err := updater.SendCardWithHandle(e.ctx, reply, e.renderCardForPlatform(p, card))
	if err != nil {
		slog.Error("send tracked permission card", "platform", p.Name(), "error", err)
		return
	}
	s := pending.card
	s.mu.Lock()
	s.updater, s.handle = updater, handle
	resolved := s.resolved
	s.mu.Unlock()
	if resolved {
		e.updateSharedPermissionCard(pending, "")
	}
}

func (e *Engine) resolveSharedPermissionCard(pending *pendingPermission) {
	s := pending.card
	if s == nil {
		return
	}
	// Both the API call and its lock may be slow while another reply edits
	// this card. Neither should hold up daemon events or foreground output.
	go func() {
		s.mu.Lock()
		refresh := !s.resolved
		s.resolved = true
		s.mu.Unlock()
		if refresh {
			e.updateSharedPermissionCard(pending, "")
		}
	}()
}

// Completion notifications carry no decision. Preserve any decision accepted
// locally; otherwise show a neutral completed state. The per-request handle
// prevents delayed or duplicate events from editing another approval card.
func (e *Engine) updateSharedPermissionCard(pending *pendingPermission, decision string) {
	s := pending.card
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if decision != "" && decision != s.decision {
		s.decision = decision
		s.updated = false
	}
	s.resolved = true
	if s.updater == nil || s.handle == nil || s.updated {
		return
	}
	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()
	card := e.sharedResolvedPermissionCard(pending, s.decision)
	if err := s.updater.UpdateCard(ctx, s.handle, card); err != nil {
		slog.Warn("update resolved permission card", "request_id", pending.RequestID, "error", err)
		return
	}
	s.updated = true
}
