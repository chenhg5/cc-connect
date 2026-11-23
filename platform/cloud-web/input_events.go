package cloudweb

import (
	"context"
	"log/slog"
	"time"
)

// ReportInjected acknowledges that this input joined an existing native turn,
// so the gateway must not wait for a separate completion trace for it.
func (p *Platform) ReportInjected(ctx context.Context, reply any, targetMessageID string) {
	if !p.hasCap("input_injected_v1") || targetMessageID == "" {
		return
	}
	rc, err := parseReplyCtx(reply)
	if err != nil {
		return
	}
	wire := map[string]any{"type": "input_injected", "session_key": rc.SessionKey,
		"reply_ctx": rc.ReplyCtx, "target_message_id": targetMessageID}
	for attempt := 0; attempt < 3; attempt++ {
		if err := p.sendWire(ctx, wire); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
	slog.Warn("cloud_web: injected input acknowledgement was not delivered")
}
