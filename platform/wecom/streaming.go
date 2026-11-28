package wecom

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
	"github.com/google/uuid"
)

// The intelligent-bot stream API replaces the full body on every frame. Its
// 20480-byte limit is measured in UTF-8 bytes, not characters.
const wsStreamMaxBytes = 20480

// wsStreamingCard uses the existing turn-level lifecycle, rather than general
// MessageUpdater: ordinary commands and proactive sends have no stream to end.
type wsStreamingCard struct {
	platform *WSPlatform
	reqID    string
	streamID string
	interval time.Duration

	mu         sync.Mutex
	lastSent   string
	lastUpdate time.Time
	started    bool
	finished   bool
	failed     bool
}

var _ core.StreamingCardPlatform = (*WSPlatform)(nil)
var _ core.StreamingCardCloser = (*wsStreamingCard)(nil)

// CreateStreamingCard only enables streaming for an actual inbound callback.
// Scheduled/proactive messages keep the existing markdown send path.
func (p *WSPlatform) CreateStreamingCard(_ context.Context, replyCtx any) (core.StreamingCard, error) {
	rc, ok := replyCtx.(wsReplyContext)
	if !ok {
		return nil, fmt.Errorf("wecom-ws: invalid stream reply context %T", replyCtx)
	}
	if p.disableStreaming || rc.reqID == "" {
		return nil, nil
	}
	return &wsStreamingCard{
		platform: p, reqID: rc.reqID, streamID: uuid.NewString(),
		interval: 1500 * time.Millisecond,
	}, nil
}

// Update sends cumulative snapshots at most once per interval. The engine
// supplies the full accumulated text; intermediate snapshots may be coalesced.
func (c *wsStreamingCard) Update(ctx context.Context, content string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished || c.failed || content == "" {
		return nil
	}
	content = streamBytePrefix(content, wsStreamMaxBytes)
	if content == c.lastSent || (c.started && time.Since(c.lastUpdate) < c.interval) {
		return nil
	}
	if err := c.sendLocked(ctx, content, false); err != nil {
		c.failed = true
		slog.Warn("wecom-ws: stream update failed, falling back to normal messages", "stream_id", c.streamID, "error", err)
		return err
	}
	return nil
}

// Finalize always sends finish=true, even if the last snapshot is identical.
// Over-limit responses close the preview and return an error so the engine
// delivers the complete answer through its existing chunked markdown fallback.
func (c *wsStreamingCard) Finalize(ctx context.Context, content string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished {
		return nil
	}
	if len(content) > wsStreamMaxBytes {
		if c.started {
			if err := c.sendLocked(ctx, c.lastSent, true); err != nil {
				return err
			}
		}
		c.finished = true
		return fmt.Errorf("wecom-ws: final stream content exceeds %d bytes; use markdown fallback", wsStreamMaxBytes)
	}
	if content == "" {
		// A silent turn without any visible output must not create a message.
		if !c.started {
			c.finished = true
			return nil
		}
		content = c.lastSent
	}
	return c.sendLocked(ctx, content, true)
}

// Close terminates an unfinished stream on cancellation, agent errors, or
// transport exit. It preserves the last acknowledged text, without pretending
// the interrupted response completed successfully.
func (c *wsStreamingCard) Close(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished || !c.started {
		return nil
	}
	return c.sendLocked(ctx, c.lastSent, true)
}

func (c *wsStreamingCard) Failed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failed
}

// sendLocked uses the callback req_id for every frame and a stable stream ID.
// Wait for a real ACK: timeout is not proof of delivery. Per-card serialization
// prevents simultaneous waiters for the same callback req_id.
func (c *wsStreamingCard) sendLocked(ctx context.Context, content string, finish bool) error {
	frame := map[string]any{
		"cmd":     "aibot_respond_msg",
		"headers": map[string]string{"req_id": c.reqID},
		"body": map[string]any{
			"msgtype": "stream",
			"stream":  map[string]any{"id": c.streamID, "content": content, "finish": finish},
		},
	}
	if err := c.platform.writeAndWaitAckStrict(ctx, frame, c.reqID, wsAckTimeout); err != nil {
		return fmt.Errorf("wecom-ws: stream frame: %w", err)
	}
	c.started = true
	c.finished = finish
	c.lastSent = content
	c.lastUpdate = time.Now()
	slog.Debug("wecom-ws: stream frame acknowledged", "stream_id", c.streamID, "finish", finish, "bytes", len(content))
	return nil
}

func streamBytePrefix(content string, limit int) string {
	if len(content) <= limit {
		return content
	}
	for limit > 0 && !utf8.RuneStart(content[limit]) {
		limit--
	}
	return content[:limit]
}
