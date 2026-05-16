package youzone

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterPlatform("youzone", New)
}

type Platform struct {
	cfg    config
	client *client

	mu       sync.RWMutex
	handler  core.MessageHandler
	cancel   context.CancelFunc
	stopping bool
	dedup    core.MessageDedup
}

func New(opts map[string]any) (core.Platform, error) {
	cfg, err := parseConfig(opts)
	if err != nil {
		return nil, err
	}
	core.CheckAllowFrom("youzone", cfg.allowFrom)
	return &Platform{
		cfg:    cfg,
		client: newClient(cfg, &http.Client{Timeout: cfg.httpTimeout}),
	}, nil
}

func (p *Platform) Name() string { return "youzone" }

func (p *Platform) Start(handler core.MessageHandler) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopping {
		return fmt.Errorf("youzone: platform stopped")
	}
	p.handler = handler
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	go p.connectLoop(ctx)
	return nil
}

func (p *Platform) Stop() error {
	p.mu.Lock()
	p.stopping = true
	cancel := p.cancel
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func (p *Platform) Reply(ctx context.Context, replyCtx any, content string) error {
	return p.Send(ctx, replyCtx, content)
}

func (p *Platform) Send(ctx context.Context, replyCtx any, content string) error {
	p.mu.RLock()
	robotID := p.cfg.robotID
	p.mu.RUnlock()
	var rc replyContext
	if v, ok := replyCtx.(replyContext); ok {
		rc = v
		if strings.TrimSpace(rc.robotID) != "" {
			robotID = rc.robotID
		}
	}
	if robotID == "" {
		// resolveRobotID may make HTTP calls. Deliberately keep that latency
		// out of the elapsed= field on send_message_succeeded / failed so the
		// send-latency picture stays meaningful.
		var err error
		robotID, err = p.resolveRobotID(ctx)
		if err != nil {
			return err
		}
	}
	msg, err := buildOutboundMessage(content, rc)
	if err != nil {
		return fmt.Errorf("youzone: build outbound message: %w", err)
	}

	start := time.Now()
	result, sendErr := p.client.sendMessage(ctx, robotID, msg)
	elapsed := time.Since(start)
	p.logSendOutcome(robotID, rc, content, result, elapsed, sendErr)
	return sendErr
}

// logSendOutcome records send_message_succeeded / send_message_failed with the
// inbound-side correlation fields (session, conversation_id, sender_id,
// reply_to_message_id, message_version) plus the HTTP-side result fields. This
// pair of logs is the only YOUZONE-aware view of an outbound send; core's
// generic platform send failed log lacks all of these, so it cannot stand in
// for this.
func (p *Platform) logSendOutcome(robotID string, rc replyContext, content string, result sendResult, elapsed time.Duration, sendErr error) {
	fields := []any{
		"robot_id", robotID,
		"session", sessionKey(inboundMessage{ConversationID: rc.conversationID, SenderID: rc.senderID}),
		"conversation_id", rc.conversationID,
		"sender_id", rc.senderID,
		"reply_to_message_id", rc.messageID,
		"message_version", rc.messageVersionRaw,
		"content_len", utf8.RuneCountInString(content),
		"http_status", result.Status,
		"packet_id", result.PacketID,
		"elapsed", elapsed,
	}
	if result.BusinessCode != nil {
		fields = append(fields, "business_code", *result.BusinessCode)
	}
	if sendErr != nil {
		fields = append(fields, "err", sendErr.Error())
		slog.Error("youzone: send message failed", fields...)
		return
	}
	slog.Info("youzone: send message succeeded", fields...)
}

func (p *Platform) resolveRobotID(ctx context.Context) (string, error) {
	p.mu.RLock()
	robotID := p.cfg.robotID
	p.mu.RUnlock()
	if robotID != "" {
		return robotID, nil
	}
	if p.cfg.machineCode == "" {
		return "", fmt.Errorf("youzone: robot_id is empty")
	}
	robots, err := p.client.listRobots(ctx, p.cfg.machineCode)
	if err != nil {
		return "", err
	}
	if len(robots) > 0 && robots[0].ID != "" {
		p.mu.Lock()
		p.cfg.robotID = robots[0].ID
		p.mu.Unlock()
		return robots[0].ID, nil
	}
	if !p.cfg.autoCreateRobot {
		return "", fmt.Errorf("youzone: no robot found for machine_code %q", p.cfg.machineCode)
	}
	robot, err := p.client.createRobot(ctx, p.cfg.machineCode, p.cfg.robotExplain)
	if err != nil {
		return "", err
	}
	p.mu.Lock()
	p.cfg.robotID = robot.ID
	p.mu.Unlock()
	return robot.ID, nil
}

func (p *Platform) handleInbound(raw []byte) {
	msg, reason := parseInboundMessage(raw)
	rawLen := len(raw)

	p.mu.RLock()
	configuredRobotID := p.cfg.robotID
	handler := p.handler
	stopping := p.stopping
	p.mu.RUnlock()

	// json_invalid and empty_frame both come back with a near-empty
	// inboundMessage (only Raw is set), so we can't print message_id /
	// conversation_id / etc. Skip inbound_frame_received and go straight to
	// the dropped log.
	switch reason {
	case inboundDropEmptyFrame:
		// Server→client xmpp-whitespace heartbeat. Default silent; only surface
		// when the operator explicitly opts into raw-frame debugging.
		if p.cfg.logInboundRaw {
			slog.Debug("youzone: inbound message dropped",
				"robot_id", configuredRobotID,
				"reason", string(reason),
				"raw_len", rawLen,
				"raw", string(raw),
			)
		}
		return
	case inboundDropJSONInvalid:
		fields := []any{
			"robot_id", configuredRobotID,
			"reason", string(reason),
			"raw_len", rawLen,
		}
		if p.cfg.logInboundRaw {
			fields = append(fields, "raw", string(raw))
		}
		slog.Warn("youzone: inbound message dropped", fields...)
		return
	}

	cmd := extractCommand(msg.Text)
	session := sessionKey(msg)
	frameFields := []any{
		"robot_id", configuredRobotID,
		"raw_len", rawLen,
		"type", msg.Type,
		"message_id", msg.MessageID,
		"conversation_id", msg.ConversationID,
		"sender_id", msg.SenderID,
		"content_type", msg.ContentType,
		"message_version", msg.MessageVersionRaw,
		"text_len", utf8.RuneCountInString(msg.Text),
		"session", session,
	}
	if cmd != "" {
		frameFields = append(frameFields, "command", cmd)
	}
	slog.Info("youzone: inbound frame received", frameFields...)

	// dropFields shares the per-message context with the frame log, plus the
	// reason field and the optional raw frame body. Each drop branch chooses
	// its own log level based on whether the drop signals a problem.
	dropFields := func(reason inboundDropReason) []any {
		out := []any{
			"robot_id", configuredRobotID,
			"reason", string(reason),
			"raw_len", rawLen,
			"type", msg.Type,
			"message_id", msg.MessageID,
			"conversation_id", msg.ConversationID,
			"sender_id", msg.SenderID,
			"message_version", msg.MessageVersionRaw,
			"session", session,
		}
		if cmd != "" {
			out = append(out, "command", cmd)
		}
		if p.cfg.logInboundRaw {
			out = append(out, "raw", string(raw))
		}
		return out
	}

	switch reason {
	case inboundDropHeartbeat:
		slog.Debug("youzone: inbound message dropped", dropFields(reason)...)
		return
	case inboundDropEmptyText:
		slog.Info("youzone: inbound message dropped", dropFields(reason)...)
		return
	}

	if p.dedup.IsDuplicate(msg.MessageID) {
		slog.Debug("youzone: inbound message dropped", dropFields(inboundDropDuplicate)...)
		return
	}
	if !core.AllowList(p.cfg.allowFrom, msg.SenderID) {
		slog.Debug("youzone: inbound message dropped", dropFields(inboundDropUnauthorizedUser)...)
		return
	}
	if handler == nil {
		if stopping {
			// Frame arrived after Stop(); not actionable, don't make it look
			// like a runtime alert.
			slog.Debug("youzone: inbound message dropped", dropFields(inboundDropMissingHandler)...)
		} else {
			slog.Warn("youzone: inbound message dropped", dropFields(inboundDropMissingHandler)...)
		}
		return
	}

	slog.Info("youzone: inbound message accepted",
		"robot_id", configuredRobotID,
		"message_id", msg.MessageID,
		"conversation_id", msg.ConversationID,
		"sender_id", msg.SenderID,
		"session", session,
		"message_version", msg.MessageVersionRaw,
		"text_len", utf8.RuneCountInString(msg.Text),
		"command", cmd,
	)
	handler(p, &core.Message{
		SessionKey: session,
		Platform:   p.Name(),
		MessageID:  msg.MessageID,
		UserID:     msg.SenderID,
		UserName:   msg.SenderName,
		Content:    msg.Text,
		ChannelKey: msg.ConversationID,
		ReplyCtx: replyContext{
			robotID:           configuredRobotID,
			conversationID:    msg.ConversationID,
			senderID:          msg.SenderID,
			messageID:         msg.MessageID,
			messageVersion:    msg.MessageVersion,
			messageVersionRaw: msg.MessageVersionRaw,
			replyText:         msg.Text,
		},
	})
}
