package core

import (
	"context"
	"time"
)

// Translate an identity issued by the manager's platform into the receiving
// member's identity scope before authenticating decisions on that member.
type DecisionUserResolver interface {
	ResolveDecisionUser(context.Context, Platform, string) (string, error)
}

// A reminder deliberately carries no command, log, question body or decision
// controls. The original execution card remains the sole decision surface.
type DecisionNotification struct {
	Key        string `json:"key"`
	UserID     string `json:"user_id"`
	ChatID     string `json:"chat_id"`
	Title      string `json:"title"`
	Summary    string `json:"summary"`
	SourceLink string `json:"source_link"`
}
type PrivateDecisionNotifier interface {
	SendPrivateDecisionNotification(context.Context, DecisionNotification) error
}
type DecisionNotificationCard interface {
	ConfigureDecisionNotification(string, string, func(context.Context, DecisionNotification) error) bool
}

func (c *nativeWorkflowCard) ConfigureDecisionNotification(user, title string, send func(context.Context, DecisionNotification) error) bool {
	if target, ok := c.inner.(DecisionNotificationCard); ok {
		return target.ConfigureDecisionNotification(user, title, send)
	}
	return false
}
func (e *Engine) decisionNotificationPlatform(p Platform, project *TeamProject) Platform {
	sender := p
	if e.teamProjects != nil && project.Manager != e.name {
		e.teamProjects.runtimeMu.Lock()
		manager := e.teamProjects.engines[project.Manager]
		e.teamProjects.runtimeMu.Unlock()
		if manager != nil {
			sender = manager.platformForName(project.Platform)
		} else {
			sender = nil
		}
	}
	return sender
}

func (e *Engine) resolveDecisionRequester(p Platform, project *TeamProject, user string) (string, error) {
	if resolver, ok := p.(DecisionUserResolver); ok {
		ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
		defer cancel()
		return resolver.ResolveDecisionUser(ctx, e.decisionNotificationPlatform(p, project), user)
	}
	return user, nil
}

func (e *Engine) configurePrivateDecision(p Platform, card InteractiveStreamingCard, project *TeamProject, task *TeamTask, user, notificationUser string) bool {
	target, ok := card.(DecisionNotificationCard)
	if !ok {
		return false
	}
	sender := e.decisionNotificationPlatform(p, project)
	title := project.Name
	if task != nil {
		title += " · " + task.Title
	}
	// An unavailable manager must not silently send a member/group reminder.
	send := func(context.Context, DecisionNotification) error { return ErrNotSupported }
	if notifier, ok := sender.(PrivateDecisionNotifier); ok {
		send = func(ctx context.Context, n DecisionNotification) error {
			// Notification belongs to the manager's app; decision controls belong
			// to the member's app. Their open IDs need not be equal.
			n.UserID = notificationUser
			return notifier.SendPrivateDecisionNotification(ctx, n)
		}
	}
	return target.ConfigureDecisionNotification(user, title, send)
}

type DecisionNoticeCard interface {
	SetDecisionNotice(context.Context, string) error
}

func (c *nativeWorkflowCard) SetDecisionNotice(ctx context.Context, text string) error {
	if target, ok := c.inner.(DecisionNoticeCard); ok {
		return target.SetDecisionNotice(ctx, text)
	}
	return c.inner.SetNotice(ctx, text)
}
