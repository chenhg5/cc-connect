package slack

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"

	"github.com/slack-go/slack"
)

// cardUpdateMinInterval coalesces chat.update calls for the streaming card.
// Slack recommends updating a streamed message at most once every ~3s; faster
// risks chat.update rate limits.
const cardUpdateMinInterval = 3 * time.Second

// slackStreamingCard aggregates one agent turn (thinking + tool steps + answer)
// into a single Slack message that updates in place — the cc-connect equivalent
// of DingTalk's AI Card. The message is posted LAZILY on the first non-empty
// content, so the native "is thinking…" status (set in StartTyping) stays
// visible until the bot actually has something to show. Implements
// core.StreamingCard.
type slackStreamingCard struct {
	client   *slack.Client
	channel  string
	threadTS string

	mu         sync.Mutex
	ts         string // empty until the first post
	failed     bool
	lastUpdate time.Time
	lastSent   string
}

// CreateStreamingCard prepares a lazy streaming card; the Slack message is not
// posted until the first content arrives. Implements core.StreamingCardPlatform
// — when present, the engine routes the whole turn through this card and skips
// the plain streaming preview (mutually exclusive, so no double-post).
func (p *Platform) CreateStreamingCard(ctx context.Context, rctx any) (core.StreamingCard, error) {
	rc, ok := rctx.(replyContext)
	if !ok {
		return nil, fmt.Errorf("slack: invalid reply context type %T", rctx)
	}
	return &slackStreamingCard{client: p.client, channel: rc.channel, threadTS: rc.timestamp}, nil
}

// render posts the card on first use, then edits it in place thereafter.
// Caller must hold c.mu.
func (c *slackStreamingCard) render(ctx context.Context, rendered string) error {
	if c.ts == "" {
		opts := []slack.MsgOption{slack.MsgOptionText(rendered, false)}
		if c.threadTS != "" {
			opts = append(opts, slack.MsgOptionPostMessageParameters(slack.PostMessageParameters{ThreadTimestamp: c.threadTS}))
		}
		_, ts, err := c.client.PostMessageContext(ctx, c.channel, opts...)
		if err != nil {
			return err
		}
		c.ts = ts
		return nil
	}
	_, _, _, err := c.client.UpdateMessageContext(ctx, c.channel, c.ts, slack.MsgOptionText(rendered, false))
	return err
}

// Update renders the latest aggregated content. The first post is immediate;
// subsequent edits are coalesced to ~cardUpdateMinInterval. Transient errors are
// swallowed (Finalize retries) so a blip doesn't abort the turn.
func (c *slackStreamingCard) Update(ctx context.Context, content string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failed || content == "" {
		return nil
	}
	if c.ts != "" && time.Since(c.lastUpdate) < cardUpdateMinInterval {
		return nil
	}
	rendered := core.MarkdownToSlackMrkdwn(content)
	if rendered == "" || rendered == c.lastSent {
		return nil
	}
	if err := c.render(ctx, rendered); err != nil {
		return nil
	}
	c.lastUpdate = time.Now()
	c.lastSent = rendered
	return nil
}

// Finalize writes the final content unconditionally (no throttle); it posts the
// card if it was never posted. On error it marks the card failed and returns the
// error so the engine falls back to a normal message.
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
	if err := c.render(ctx, rendered); err != nil {
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
