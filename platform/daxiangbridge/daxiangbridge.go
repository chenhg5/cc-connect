package daxiangbridge

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterPlatform("daxiangbridge", New)
}

// Platform implements core.Platform and core.AsyncRecoverablePlatform.
type Platform struct {
	wsURL        string
	clientID     string
	clientSecret string // 32-char hex AES-128 key
	botID        int64

	handler   core.MessageHandler
	client    *wsClient
	cancel    context.CancelFunc
	lifecycle core.PlatformLifecycleHandler

	permissions *pendingPermissions
	streamSeq   sync.Map // requestID -> *atomic.Int32

	mu sync.Mutex
}

func New(opts map[string]any) (core.Platform, error) {
	wsURL, _ := opts["ws_url"].(string)
	clientID, _ := opts["client_id"].(string)
	clientSecret, _ := opts["client_secret"].(string)
	botID := int64FromOpt(opts["bot_id"])

	if wsURL == "" || clientID == "" || clientSecret == "" || botID == 0 {
		return nil, fmt.Errorf("daxiangbridge: ws_url, client_id, client_secret, and bot_id are required")
	}
	if len(clientSecret) != 32 {
		return nil, fmt.Errorf("daxiangbridge: client_secret must be a 32-char hex string (16 bytes)")
	}
	return &Platform{
		wsURL:        wsURL,
		clientID:     clientID,
		clientSecret: clientSecret,
		botID:        botID,
		permissions:  newPendingPermissions(),
	}, nil
}

func int64FromOpt(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	default:
		return 0
	}
}

func (p *Platform) Name() string { return "daxiangbridge" }

func (p *Platform) SetLifecycleHandler(h core.PlatformLifecycleHandler) {
	p.mu.Lock()
	p.lifecycle = h
	p.mu.Unlock()
}

func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.client = newWsClient(p.wsURL, p.clientID, p.botID, p.clientSecret, p.onFrame, func() {
		p.mu.Lock()
		h := p.lifecycle
		p.mu.Unlock()
		if h != nil {
			h.OnPlatformReady(p)
		}
	})
	go p.client.run(ctx)
	return nil
}

func (p *Platform) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	return nil
}

func (p *Platform) onFrame(frame BridgeFrame) {
	switch frame.Type {
	case FrameTypeBridgeEventMessage:
		msg, err := normalizeInboundMessage(frame)
		if err != nil {
			slog.Warn("daxiangbridge: normalize inbound", "error", err)
			return
		}
		if p.handler != nil {
			p.handler(p, msg)
		}
	case FrameTypeBridgePermissionResponse:
		var payload PermissionResponsePayload
		if err := unmarshalPayload(frame, &payload); err != nil {
			slog.Warn("daxiangbridge: unmarshal permission response", "error", err)
			return
		}
		p.permissions.resolve(payload.PermissionID, payload.Decision)
	default:
		slog.Debug("daxiangbridge: unhandled frame", "type", frame.Type)
	}
}

func (p *Platform) Reply(ctx context.Context, replyCtx any, content string) error {
	return p.Send(ctx, replyCtx, content)
}

func (p *Platform) Send(ctx context.Context, replyCtx any, content string) error {
	rc, ok := replyCtx.(replyContext)
	if !ok {
		return fmt.Errorf("daxiangbridge: invalid reply context type %T", replyCtx)
	}
	p.streamSeq.Delete(rc.requestID)
	p.client.Send(buildFinalReplyFrame(rc.requestID, rc.sessionID, content))
	return nil
}

func (p *Platform) FinalizePreview(ctx context.Context, previewHandle any, finalText string) error {
	rc, ok := previewHandle.(replyContext)
	if !ok {
		return fmt.Errorf("daxiangbridge: invalid preview handle type %T", previewHandle)
	}
	p.streamSeq.Delete(rc.requestID)
	p.client.Send(buildEndFrame(rc.requestID, rc.sessionID, finalText))
	return nil
}

func (p *Platform) KeepPreviewOnFinish() bool {
	return true
}

func (p *Platform) DeletePreviewMessage(ctx context.Context, previewHandle any) error {
	rc, ok := previewHandle.(replyContext)
	if !ok {
		return fmt.Errorf("daxiangbridge: invalid preview handle type %T", previewHandle)
	}
	p.streamSeq.Delete(rc.requestID)
	return nil
}

// SendPreviewStart begins a streaming response.
func (p *Platform) SendPreviewStart(ctx context.Context, replyCtx any, content string) (any, error) {
	rc, ok := replyCtx.(replyContext)
	if !ok {
		return nil, fmt.Errorf("daxiangbridge: invalid reply context type %T", replyCtx)
	}
	var seq atomic.Int32
	p.streamSeq.Store(rc.requestID, &seq)
	p.client.Send(buildStartFrame(rc.requestID, rc.sessionID))
	if content != "" {
		p.client.Send(buildDeltaFrame(rc.requestID, rc.sessionID, int(seq.Add(1)-1), content))
	}
	return rc, nil
}

// UpdateMessage sends a stream delta.
func (p *Platform) UpdateMessage(ctx context.Context, previewHandle any, content string) error {
	rc, ok := previewHandle.(replyContext)
	if !ok {
		return fmt.Errorf("daxiangbridge: invalid preview handle type %T", previewHandle)
	}
	seqVal, _ := p.streamSeq.Load(rc.requestID)
	seq, _ := seqVal.(*atomic.Int32)
	if seq == nil {
		return fmt.Errorf("daxiangbridge: no active stream for requestID %q", rc.requestID)
	}
	p.client.Send(buildDeltaFrame(rc.requestID, rc.sessionID, int(seq.Add(1)-1), content))
	return nil
}

// HandlePermissionRequest forwards a permission request to bridge and waits for user decision.
func (p *Platform) HandlePermissionRequest(event core.Event, replyCtx any) (<-chan core.PermissionResult, error) {
	rc, ok := replyCtx.(replyContext)
	if !ok {
		return nil, fmt.Errorf("daxiangbridge: invalid reply context")
	}
	permID := event.RequestID
	ch := p.permissions.register(permID, rc.requestID, rc.sessionID)
	p.client.Send(buildPermissionRequestFrame(
		rc.requestID, rc.sessionID, permID,
		event.ToolName, event.ToolInput, "",
	))

	result := make(chan core.PermissionResult, 1)
	go func() {
		select {
		case r := <-ch:
			behavior := "deny"
			if r.decision == "approve" {
				behavior = "allow"
			}
			result <- core.PermissionResult{Behavior: behavior}
		case <-time.After(2 * time.Minute):
			result <- core.PermissionResult{Behavior: "deny", Message: "permission confirmation timed out"}
		}
	}()
	return result, nil
}
