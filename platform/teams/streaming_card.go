package teams

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// CreateStreamingCard implements core.StreamingCardPlatform: the reply always
// streams as an Adaptive Card, edited in place as the answer grows. It renders
// uniformly in channels, group chats, and 1:1 (unlike the native streamType
// protocol, which is one-on-one only). If the reply context is unusable the
// engine treats the error as "no stream" and falls back to a plain reply.
func (p *Platform) CreateStreamingCard(ctx context.Context, rctx any) (core.StreamingCard, error) {
	rc, ok := rctx.(replyContext)
	if !ok || rc.serviceURL == "" || rc.conversationID == "" {
		return nil, fmt.Errorf("teams: invalid reply context for streaming card")
	}
	return p.createCardStream(ctx, rc)
}

// streamInterval resolves the streaming-card edit cadence from
// card_update_interval_ms, with the built-in default as fallback.
func (p *Platform) streamInterval() time.Duration {
	ms := p.cfg.cardUpdateIntervalMS
	if ms <= 0 {
		ms = defaultCardUpdateIntervalMS // zero-value config (e.g. tests built without parseConfig)
	}
	return time.Duration(ms) * time.Millisecond
}

// createCardStream posts the loading Adaptive Card immediately and returns a
// handle that edits it in place.
func (p *Platform) createCardStream(ctx context.Context, rc replyContext) (core.StreamingCard, error) {
	id, err := p.conn.send(ctx, rc, aiCardActivity(rc, loadingCard(p.cfg.cardLoadingText)))
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, fmt.Errorf("teams: streaming card got no activity id")
	}
	return &teamsStreamingCard{
		conn:       p.conn,
		rc:         rc,
		activityID: id,
		interval:   p.streamInterval(),
	}, nil
}

// teamsStreamingCard edits one Adaptive Card in place as the answer streams.
type teamsStreamingCard struct {
	conn       sender
	rc         replyContext
	activityID string
	interval   time.Duration

	mu       sync.Mutex
	lastSent time.Time
	lastText string
	failed   bool
}

// Update edits the card with the answer so far, throttled (latest-wins). Mid-stream
// edit failures are non-fatal — Finalize is what gates the engine's fallback.
func (c *teamsStreamingCard) Update(ctx context.Context, content string) error {
	c.mu.Lock()
	if c.failed || content == c.lastText {
		c.mu.Unlock()
		return nil
	}
	if !c.lastSent.IsZero() && time.Since(c.lastSent) < c.interval {
		c.lastText = content // remember the latest; a later Update past the interval sends it.
		// Finalize does not read lastText — the engine passes it the full final content.
		c.mu.Unlock()
		return nil
	}
	c.lastSent = time.Now()
	c.lastText = content
	c.mu.Unlock()

	if err := c.conn.update(ctx, c.rc, c.activityID, aiCardActivity(c.rc, answerCard(content))); err != nil {
		slog.Debug("teams: streaming card update failed", "error", err)
	}
	return nil
}

// Finalize replaces the card with the final answer (plus the AI footer). A failure
// here marks the card failed so the engine sends its normal-message fallback.
func (c *teamsStreamingCard) Finalize(ctx context.Context, content string) error {
	c.mu.Lock()
	if c.failed {
		c.mu.Unlock()
		return nil // already terminal — don't re-PUT (matches Slack/DingTalk)
	}
	c.mu.Unlock()
	if err := c.conn.update(ctx, c.rc, c.activityID, aiCardActivity(c.rc, answerCard(content))); err != nil {
		c.mu.Lock()
		c.failed = true
		c.mu.Unlock()
		return err
	}
	return nil
}

// Failed reports whether the card hit a terminal error.
func (c *teamsStreamingCard) Failed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failed
}
