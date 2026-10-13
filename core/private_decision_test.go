package core

import (
	"context"
	"fmt"
	"sync"
)

// Only messaging/process boundaries are faked; tests drive real Engine routing.
type privateCUJPlatform struct{ *threadCUJPlatform }

// Simulate application-scoped identities at the external platform boundary.
func (p *privateCUJPlatform) ResolveDecisionUser(_ context.Context, source Platform, user string) (string, error) {
	if p.role == "dev" && source != p && user == "owner" {
		return "member-owner", nil
	}
	return user, nil
}

func (p *privateCUJPlatform) SendPrivateDecisionNotification(ctx context.Context, n DecisionNotification) error {
	return p.Send(ctx, "private:"+n.UserID, "DM "+n.UserID+": "+n.Summary+" · "+n.SourceLink)
}
func (p *privateCUJPlatform) CreateStreamingCard(ctx context.Context, rc any) (StreamingCard, error) {
	sc, err := p.threadCUJPlatform.CreateStreamingCard(ctx, rc)
	if err != nil {
		return nil, err
	}
	return &privateCUJCard{cujNativeCard: sc.(*cujNativeCard), p: p, source: fmt.Sprint(rc)}, nil
}
func (p *privateCUJPlatform) CreateTaskStreamingCard(ctx context.Context, rc any, key string) (StreamingCard, error) {
	return p.CreateStreamingCard(ctx, rc)
}

type privateCUJCard struct {
	*cujNativeCard
	lock                sync.Mutex
	p                   *privateCUJPlatform
	source, user, title string
	notify              func(context.Context, DecisionNotification) error
}

func (c *privateCUJCard) ConfigureDecisionNotification(user, title string, notify func(context.Context, DecisionNotification) error) bool {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.user, c.title, c.notify = user, title, notify
	return true
}
func (c *privateCUJCard) SetPrompt(ctx context.Context, prompt *Card) error {
	if err := c.cujNativeCard.SetPrompt(ctx, prompt); err != nil {
		return err
	}
	if prompt == nil {
		return nil
	}
	_ = c.p.Send(ctx, c.source, "ON_CARD "+prompt.RenderText())
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.notify != nil {
		return c.notify(ctx, DecisionNotification{Key: decisionTestKey(prompt), UserID: c.user, Title: c.title, Summary: "Action needed", SourceLink: "source:" + c.source})
	}
	return nil
}
func decisionTestKey(c *Card) string {
	if c.DecisionToken != "" {
		return c.DecisionToken
	}
	return "approval"
}
