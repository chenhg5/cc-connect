package silk

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

func init() {
	core.RegisterPlatform("silk", New)
}

type replyContext struct {
	msgID    string
	userID   string
	userName string
}

type Platform struct {
	serverURL string
	token     string
	project   string
	agentType string
	cwd       string

	mu       sync.RWMutex
	conn     *websocket.Conn
	handler  core.MessageHandler
	cancel   context.CancelFunc
	stopping bool
	groupID  string
}

func New(opts map[string]any) (core.Platform, error) {
	server, _ := opts["server"].(string)
	if server == "" {
		return nil, fmt.Errorf("silk: server is required")
	}
	token, _ := opts["token"].(string)
	if token == "" {
		return nil, fmt.Errorf("silk: token is required")
	}
	project, _ := opts["project"].(string)
	agentType, _ := opts["agent_type"].(string)
	cwd, _ := opts["work_dir"].(string)
	if cwd == "" {
		cwd, _ = opts["cwd"].(string)
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	return &Platform{
		serverURL: server,
		token:     token,
		project:   project,
		agentType: agentType,
		cwd:       cwd,
	}, nil
}

func (p *Platform) Name() string { return "silk" }

func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	go p.connectLoop(ctx)
	return nil
}

func (p *Platform) Stop() error {
	p.mu.Lock()
	p.stopping = true
	if p.cancel != nil {
		p.cancel()
	}
	conn := p.conn
	p.mu.Unlock()
	if conn != nil {
		conn.WriteMessage(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "stopping"),
		)
		conn.Close()
	}
	return nil
}

func (p *Platform) Reply(ctx context.Context, replyCtx any, content string) error {
	return p.sendJSON(map[string]any{
		"type":    "reply",
		"content": content,
		"format":  "markdown",
	})
}

func (p *Platform) Send(ctx context.Context, replyCtx any, content string) error {
	return p.Reply(ctx, replyCtx, content)
}

// MessageUpdater enables streaming: the engine calls UpdateMessage for incremental
// content, then Reply for the final version.
func (p *Platform) UpdateMessage(ctx context.Context, replyCtx any, content string) error {
	return p.sendJSON(map[string]any{
		"type":    "reply_stream",
		"content": content,
		"done":    false,
	})
}

func (p *Platform) StartTyping(ctx context.Context, replyCtx any) (stop func()) {
	_ = p.sendJSON(map[string]any{
		"type":  "status",
		"state": "thinking",
	})
	return func() {
		_ = p.sendJSON(map[string]any{
			"type":  "status",
			"state": "idle",
		})
	}
}

func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	return &replyContext{}, nil
}

// StreamingCardPlatform — aggregates an entire agent turn into a single
// updatable message instead of sending separate messages per event.
func (p *Platform) CreateStreamingCard(ctx context.Context, replyCtx any) (core.StreamingCard, error) {
	return &silkStreamingCard{platform: p}, nil
}

type silkStreamingCard struct {
	platform *Platform
	mu       sync.Mutex
	failed   bool
	lastSent time.Time
}

func (c *silkStreamingCard) Update(ctx context.Context, content string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.lastSent) < 500*time.Millisecond {
		return nil
	}
	c.lastSent = time.Now()
	return c.platform.sendJSON(map[string]any{
		"type":        "reply_stream",
		"content":     content,
		"done":        false,
		"incremental": false,
	})
}

func (c *silkStreamingCard) Finalize(ctx context.Context, content string) error {
	// Intentional error: the engine's fallback calls Reply(fullResponse) which
	// sends only the clean answer text, without thinking/tool card metadata.
	return fmt.Errorf("silk: finalize triggers reply fallback for clean answer")
}

func (c *silkStreamingCard) Failed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failed
}

// --- internal ---

func (p *Platform) sendJSON(msg map[string]any) error {
	p.mu.RLock()
	conn := p.conn
	p.mu.RUnlock()
	if conn == nil {
		return fmt.Errorf("silk: not connected")
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, data)
}

func (p *Platform) connectLoop(ctx context.Context) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		err := p.connect(ctx)
		if err != nil {
			slog.Warn("[silk] connection error", "err", err)
		}
		p.mu.RLock()
		stopping := p.stopping
		p.mu.RUnlock()
		if stopping {
			return
		}
		slog.Info("[silk] reconnecting", "backoff", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (p *Platform) connect(ctx context.Context) error {
	u, err := url.Parse(p.serverURL)
	if err != nil {
		return fmt.Errorf("silk: invalid server URL: %w", err)
	}
	q := u.Query()
	q.Set("token", p.token)
	u.RawQuery = q.Encode()

	slog.Info("[silk] connecting", "url", u.String())
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return fmt.Errorf("silk: dial failed: %w", err)
	}

	hello := map[string]any{
		"type":       "hello",
		"platform":   "silk",
		"version":    1,
		"project":    p.project,
		"agent_type": p.agentType,
		"cwd":        p.cwd,
	}
	if err := conn.WriteJSON(hello); err != nil {
		conn.Close()
		return fmt.Errorf("silk: hello send failed: %w", err)
	}

	var ack struct {
		Type      string `json:"type"`
		OK        bool   `json:"ok"`
		GroupID   string `json:"group_id"`
		GroupName string `json:"group_name"`
		Error     string `json:"error"`
	}
	if err := conn.ReadJSON(&ack); err != nil {
		conn.Close()
		return fmt.Errorf("silk: hello_ack read failed: %w", err)
	}
	if !ack.OK {
		conn.Close()
		return fmt.Errorf("silk: handshake rejected: %s", ack.Error)
	}

	p.mu.Lock()
	p.conn = conn
	p.groupID = ack.GroupID
	p.mu.Unlock()

	slog.Info("[silk] connected", "groupId", ack.GroupID, "groupName", ack.GroupName)

	go p.pingLoop(ctx, conn)

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			p.mu.Lock()
			p.conn = nil
			p.mu.Unlock()
			return fmt.Errorf("silk: read error: %w", err)
		}

		var envelope struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &envelope) != nil {
			continue
		}

		switch envelope.Type {
		case "message":
			var msg struct {
				Content  string `json:"content"`
				UserID   string `json:"user_id"`
				UserName string `json:"user_name"`
				MsgID    string `json:"msg_id"`
			}
			if json.Unmarshal(data, &msg) != nil {
				continue
			}
			coreMsg := &core.Message{
				Content:    msg.Content,
				UserID:     msg.UserID,
				UserName:   msg.UserName,
				SessionKey: p.groupID,
				Platform:   "silk",
				MessageID:  msg.MsgID,
				ReplyCtx: &replyContext{
					msgID:    msg.MsgID,
					userID:   msg.UserID,
					userName: msg.UserName,
				},
			}
			if p.handler != nil {
				p.handler(p, coreMsg)
			}
		case "ping":
			_ = p.sendJSON(map[string]any{"type": "pong"})
		}
	}
}

func (p *Platform) pingLoop(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.mu.RLock()
			current := p.conn
			p.mu.RUnlock()
			if current != conn {
				return
			}
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
