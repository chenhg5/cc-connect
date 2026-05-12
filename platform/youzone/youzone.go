package youzone

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

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
	_, err = p.client.sendMessage(ctx, robotID, msg)
	return err
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
	msg, ok := parseInboundMessage(raw)
	if !ok {
		if p.cfg.logInboundRaw {
			slog.Debug("youzone: ignored inbound frame", "raw", string(raw))
		}
		return
	}
	if p.dedup.IsDuplicate(msg.MessageID) {
		slog.Debug("youzone: duplicate message ignored", "msg_id", msg.MessageID)
		return
	}
	if !core.AllowList(p.cfg.allowFrom, msg.SenderID) {
		slog.Debug("youzone: message from unauthorized user", "user", msg.SenderID)
		return
	}
	p.mu.RLock()
	handler := p.handler
	robotID := p.cfg.robotID
	p.mu.RUnlock()
	if handler == nil {
		return
	}
	handler(p, &core.Message{
		SessionKey: sessionKey(msg),
		Platform:   p.Name(),
		MessageID:  msg.MessageID,
		UserID:     msg.SenderID,
		UserName:   msg.SenderName,
		Content:    msg.Text,
		ChannelKey: msg.ConversationID,
		ReplyCtx: replyContext{
			robotID:        robotID,
			conversationID: msg.ConversationID,
			senderID:       msg.SenderID,
			messageID:      msg.MessageID,
			messageVersion: msg.MessageVersion,
			replyText:      msg.Text,
		},
	})
}
