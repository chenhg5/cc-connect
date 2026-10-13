package core

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// DecisionReminderPlatform places actionable requests in the parent conversation
// while preserving the execution session and its original thread.
type DecisionReminderPlatform interface {
	CreateDecisionReminder(context.Context, string, string, string) (InteractiveStreamingCard, error)
}
type decisionCards struct{ thread, reminder InteractiveStreamingCard }

func (c *decisionCards) Failed() bool                               { return c.reminder.Failed() }
func (c *decisionCards) Update(ctx context.Context, s string) error { return c.reminder.Update(ctx, s) }
func (c *decisionCards) Finalize(ctx context.Context, s string) error {
	return c.reminder.Finalize(ctx, s)
}
func (c *decisionCards) Finish(ctx context.Context, s string, status CardStatus) error {
	return c.reminder.Finish(ctx, s, status)
}
func (c *decisionCards) SetPrompt(ctx context.Context, p *Card) error {
	// A failed parent reminder must not make the request invisible in the thread.
	err := c.reminder.SetPrompt(ctx, p)
	if c.thread != nil {
		err = errors.Join(err, c.thread.SetPrompt(ctx, p))
	}
	return err
}
func (c *decisionCards) SetNotice(ctx context.Context, s string) error {
	err := c.reminder.SetNotice(ctx, s)
	if c.thread != nil {
		err = errors.Join(err, c.thread.SetNotice(ctx, s))
	}
	return err
}
func (e *Engine) expirePendingDecision(state *interactiveState, pending *pendingPermission) {
	pending.mu.Lock()
	defer pending.mu.Unlock()
	state.mu.Lock()
	if state.pending != pending {
		state.mu.Unlock()
		return
	}
	state.pending = nil
	state.mu.Unlock()
	pending.Expired = true
	if state.agentSession != nil {
		if err := state.agentSession.RespondPermission(pending.RequestID, PermissionResult{Behavior: "deny", Message: "Request expired or execution stopped; no user authorization."}); err != nil {
			slog.Debug("expire pending decision", "error", err)
		}
	}
	if pending.Card != nil {
		queryCardNotice(e.ctx, pending.Card, e.i18n.T(MsgDecisionExpired))
		if err := pending.Card.SetPrompt(e.ctx, nil); err != nil {
			slog.Warn("close expired decision", "error", err)
		}
	}
	pending.resolve()
}
func decisionDeadline(event Event) time.Time {
	if !event.ExpiresAt.IsZero() {
		return event.ExpiresAt
	}
	return time.Now().Add(10 * time.Minute)
}

// Stop/reset may already have detached the agent. Close the visible request
// independently; a stopped session must never retain active-looking controls.
func (e *Engine) closePendingDecision(p *pendingPermission) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Expired = true
	if p.Card != nil {
		queryCardNotice(e.ctx, p.Card, e.i18n.T(MsgDecisionExpired))
		if err := p.Card.SetPrompt(e.ctx, nil); err != nil {
			slog.Warn("close decision card", "error", err)
		}
	}
	p.resolve()
}

// Shared only by the engines of this runtime. It prevents an unmentioned reply
// to a member's reminder from also starting a new manager turn.
type DecisionReplyRoute struct {
	ChatID, SessionKey, RequesterID, Token string
	Owned                                  bool
}
type DecisionReplyRegistryPlatform interface {
	SetDecisionReplyRegistry(func(string, DecisionReplyRoute), func(string, string) (DecisionReplyRoute, bool))
}
type decisionRouteEntry struct {
	route   DecisionReplyRoute
	bot     string
	created time.Time
}

func (e *Engine) configureDecisionReplies(p Platform) {
	rp, ok := p.(DecisionReplyRegistryPlatform)
	if !ok || !e.teamReports || e.teamProjects == nil {
		return
	}
	s := e.teamProjects
	rp.SetDecisionReplyRegistry(func(id string, r DecisionReplyRoute) {
		s.runtimeMu.Lock()
		defer s.runtimeMu.Unlock()
		if s.decisionRoutes == nil {
			s.decisionRoutes = map[string]decisionRouteEntry{}
		}
		for key, old := range s.decisionRoutes {
			if time.Since(old.created) > 24*time.Hour {
				delete(s.decisionRoutes, key)
			}
		}
		s.decisionRoutes[p.Name()+":"+r.ChatID+":"+id] = decisionRouteEntry{route: r, bot: e.name, created: time.Now()}
	}, func(chat, id string) (DecisionReplyRoute, bool) {
		s.runtimeMu.Lock()
		defer s.runtimeMu.Unlock()
		entry, ok := s.decisionRoutes[p.Name()+":"+chat+":"+id]
		r := entry.route
		r.Owned = entry.bot == e.name
		return r, ok
	})
}
