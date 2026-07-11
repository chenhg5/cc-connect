package slack

import (
	"context"
	"fmt"

	"github.com/chenhg5/cc-connect/core"

	"github.com/slack-go/slack"
)

// slackPreviewHandle points at the in-flight streaming-preview message so
// UpdateMessage can edit it in place via chat.update.
type slackPreviewHandle struct {
	channel   string
	timestamp string
}

// SendPreviewStart posts the initial streaming-preview message (threaded like a
// normal reply) and returns a handle for subsequent edits. Implements
// core.PreviewStarter; together with UpdateMessage it lights up the engine's
// real-time streaming preview for Slack (the engine throttles the edits, so we
// stay within chat.update rate limits).
func (p *Platform) SendPreviewStart(ctx context.Context, rctx any, content string) (any, error) {
	rc, ok := rctx.(replyContext)
	if !ok {
		return nil, fmt.Errorf("slack: invalid reply context type %T", rctx)
	}
	opts := []slack.MsgOption{
		slack.MsgOptionText(core.MarkdownToSlackMrkdwn(content), false),
	}
	if rc.timestamp != "" {
		opts = append(opts, slack.MsgOptionPostMessageParameters(slack.PostMessageParameters{ThreadTimestamp: rc.timestamp}))
	}
	_, ts, err := p.client.PostMessageContext(ctx, rc.channel, opts...)
	if err != nil {
		return nil, fmt.Errorf("slack: send preview: %w", err)
	}
	return &slackPreviewHandle{channel: rc.channel, timestamp: ts}, nil
}

// UpdateMessage edits the preview message in place. The engine passes the handle
// returned by SendPreviewStart (not the reply context). Implements
// core.MessageUpdater.
func (p *Platform) UpdateMessage(ctx context.Context, previewHandle any, content string) error {
	h, ok := previewHandle.(*slackPreviewHandle)
	if !ok {
		return fmt.Errorf("slack: invalid preview handle type %T", previewHandle)
	}
	_, _, _, err := p.client.UpdateMessageContext(ctx, h.channel, h.timestamp,
		slack.MsgOptionText(core.MarkdownToSlackMrkdwn(content), false),
	)
	if err != nil {
		return fmt.Errorf("slack: update preview: %w", err)
	}
	return nil
}

var (
	_ core.MessageUpdater = (*Platform)(nil)
	_ core.PreviewStarter = (*Platform)(nil)
)
