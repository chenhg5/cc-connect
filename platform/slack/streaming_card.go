package slack

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"

	"github.com/slack-go/slack"
)

// cardUpdateMinInterval coalesces chat.update calls for the streaming card so we
// stay within Slack's rate limits. The engine pushes an update per agent event;
// we flush at most once per interval and always flush on Finalize.
const cardUpdateMinInterval = 1200 * time.Millisecond

// slackStreamingCard aggregates one agent turn (thinking + tool steps + answer)
// into a single Slack message that updates in place — the cc-connect equivalent
// of DingTalk's AI Card. Implements core.StreamingCard.
type slackStreamingCard struct {
	client  *slack.Client
	channel string
	ts      string

	mu         sync.Mutex
	failed     bool
	lastUpdate time.Time
	lastSent   string
}

// CreateStreamingCard posts the initial card message (threaded like a normal
// reply) and returns it. Implements core.StreamingCardPlatform: when present,
// the engine routes the whole turn through this card and skips the plain
// streaming preview (they are mutually exclusive, so no double-post).
func (p *Platform) CreateStreamingCard(ctx context.Context, rctx any) (core.StreamingCard, error) {
	rc, ok := rctx.(replyContext)
	if !ok {
		return nil, fmt.Errorf("slack: invalid reply context type %T", rctx)
	}
	opts := []slack.MsgOption{slack.MsgOptionText("…", false)}
	if rc.timestamp != "" {
		opts = append(opts, slack.MsgOptionPostMessageParameters(slack.PostMessageParameters{ThreadTimestamp: rc.timestamp}))
	}
	_, ts, err := p.client.PostMessageContext(ctx, rc.channel, opts...)
	if err != nil {
		return nil, fmt.Errorf("slack: create streaming card: %w", err)
	}
	return &slackStreamingCard{client: p.client, channel: rc.channel, ts: ts}, nil
}

// Update edits the card with the latest aggregated content, throttled. Transient
// update errors are swallowed (a later Update / Finalize retries) so a blip
// doesn't abort the turn; the engine still gets the final content via Finalize.
func (c *slackStreamingCard) Update(ctx context.Context, content string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failed || content == "" {
		return nil
	}
	if time.Since(c.lastUpdate) < cardUpdateMinInterval {
		return nil
	}
	rendered := core.MarkdownToSlackMrkdwn(content)
	if rendered == "" || rendered == c.lastSent {
		return nil
	}
	if _, _, _, err := c.client.UpdateMessageContext(ctx, c.channel, c.ts, slack.MsgOptionText(rendered, false)); err != nil {
		return nil
	}
	c.lastUpdate = time.Now()
	c.lastSent = rendered
	return nil
}

// Finalize writes the final content unconditionally (no throttle). On error it
// marks the card failed and returns the error so the engine falls back to a
// normal message.
func (c *slackStreamingCard) Finalize(ctx context.Context, content string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failed {
		return nil
	}
	rendered := core.MarkdownToSlackMrkdwn(content)
	if rendered == "" || rendered == c.lastSent {
		return nil
	}
	if _, _, _, err := c.client.UpdateMessageContext(ctx, c.channel, c.ts, slack.MsgOptionText(rendered, false)); err != nil {
		c.failed = true
		return fmt.Errorf("slack: finalize streaming card: %w", err)
	}
	c.lastSent = rendered
	return nil
}

// Failed reports whether the card has entered a terminal failed state.
func (c *slackStreamingCard) Failed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failed
}

var _ core.StreamingCardPlatform = (*Platform)(nil)
