package silk

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
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
	writeMu  sync.Mutex // serialises all WebSocket writes
	handler  core.MessageHandler
	cancel   context.CancelFunc
	stopping bool
	groupID  string

	metadataActive atomic.Bool
	metadataReply  chan string
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
		serverURL:     server,
		token:         token,
		project:       project,
		agentType:     agentType,
		cwd:           cwd,
		metadataReply: make(chan string, 1),
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
		p.writeMu.Lock()
		conn.WriteMessage(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "stopping"),
		)
		p.writeMu.Unlock()
		conn.Close()
	}
	return nil
}

func (p *Platform) Reply(ctx context.Context, replyCtx any, content string) error {
	if p.metadataActive.Load() {
		select {
		case p.metadataReply <- content:
		default:
		}
		return nil
	}
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
	if p.metadataActive.Load() {
		return nil
	}
	return p.sendJSON(map[string]any{
		"type":    "reply_stream",
		"content": content,
		"done":    false,
	})
}

func (p *Platform) StartTyping(ctx context.Context, replyCtx any) (stop func()) {
	if p.metadataActive.Load() {
		return func() {}
	}
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
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
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
	go p.queryAndSendMetadata()

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
		case "command":
			var cmd struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(data, &cmd) != nil || cmd.Text == "" {
				continue
			}
			if p.handler != nil {
				coreMsg := &core.Message{
					Content:    cmd.Text,
					UserID:     "__silk_cmd__",
					UserName:   "Silk",
					SessionKey: p.groupID,
					Platform:   "silk",
					ReplyCtx:   &replyContext{userID: "__silk_cmd__", userName: "Silk"},
				}
				p.handler(p, coreMsg)
				go func() {
					time.Sleep(2 * time.Second)
					p.queryAndSendMetadata()
				}()
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
			p.writeMu.Lock()
			err := conn.WriteMessage(websocket.PingMessage, nil)
			p.writeMu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

// --- metadata query ---

func (p *Platform) queryAndSendMetadata() {
	// Drain any stale replies
	select {
	case <-p.metadataReply:
	default:
	}

	p.metadataActive.Store(true)

	p.handler(p, &core.Message{
		Content:    "/mode",
		UserID:     "__silk_meta__",
		UserName:   "Silk",
		SessionKey: p.groupID,
		Platform:   "silk",
		ReplyCtx:   &replyContext{userID: "__silk_meta__"},
	})
	modeText := waitChan(p.metadataReply, 3*time.Second)
	slog.Debug("[silk] raw /mode response", "text", modeText)

	// Drain again before next query
	select {
	case <-p.metadataReply:
	default:
	}

	p.handler(p, &core.Message{
		Content:    "/model",
		UserID:     "__silk_meta__",
		UserName:   "Silk",
		SessionKey: p.groupID,
		Platform:   "silk",
		ReplyCtx:   &replyContext{userID: "__silk_meta__"},
	})
	modelText := waitChan(p.metadataReply, 5*time.Second)
	slog.Debug("[silk] raw /model response", "text", modelText)

	p.metadataActive.Store(false)

	mode, modes := parseModeResponse(modeText)
	model, models := parseModelResponse(modelText)

	if mode == "" && model == "" && len(modes) == 0 && len(models) == 0 {
		slog.Debug("[silk] metadata query returned nothing, skipping send")
		return
	}

	_ = p.sendJSON(map[string]any{
		"type":             "metadata",
		"mode":             mode,
		"model":            model,
		"available_modes":  modes,
		"available_models": models,
	})
	slog.Info("[silk] metadata sent", "mode", mode, "model", model,
		"modes", len(modes), "models", len(models))
}

func waitChan(ch chan string, timeout time.Duration) string {
	select {
	case v := <-ch:
		return v
	case <-time.After(timeout):
		return ""
	}
}

// parseModeResponse extracts current mode and available modes from the
// engine's /mode text response.
// Format: "**Name**(current) — Description" or "**NameZh**（当前）— DescZh"
// Usage line: "可用值: `default` / `force` / `plan` / `ask`"
func parseModeResponse(text string) (current string, modes []map[string]string) {
	if text == "" {
		return "", nil
	}
	nameRe := regexp.MustCompile(`\*\*([^*]+)\*\*`)
	keyRe := regexp.MustCompile("`([a-zA-Z][a-zA-Z0-9_-]*)`")

	var keys []string
	for _, line := range strings.Split(text, "\n") {
		found := keyRe.FindAllStringSubmatch(line, -1)
		if len(found) >= 2 {
			for _, m := range found {
				keys = append(keys, m[1])
			}
		}
	}

	idx := 0
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := nameRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := m[1]
		key := name
		if idx < len(keys) {
			key = keys[idx]
		}
		idx++
		isCurrent := strings.Contains(line, "(current)") || strings.Contains(line, "（当前）")
		modes = append(modes, map[string]string{"key": key, "name": name})
		if isCurrent {
			current = key
		}
	}
	return current, modes
}

// parseModelResponse extracts current model and available models from the
// engine's /model text response.
// Header: "当前模型: model_name" or "Current model: model_name"
// Lines: "> 1. model_name — description" (current) or "  1. model_name — desc"
// Alias: "> 1. alias - model_name"
var (
	modelLineRe    = regexp.MustCompile(`^(>\s*)?\d+\.\s+(\S+?)(?:\s*[-—]\s*(.+?))?\s*$`)
	modelCurrentRe = regexp.MustCompile(`(?:Current model|当前模型|當前模型|現在のモデル):\s*([a-zA-Z]\S+)`)
)

func parseModelResponse(text string) (current string, models []map[string]string) {
	if text == "" {
		return "", nil
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if m := modelCurrentRe.FindStringSubmatch(line); m != nil {
			current = m[1]
		}
		m := modelLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		isCurrent := strings.TrimSpace(m[1]) == ">"
		name := m[2]
		desc := strings.TrimSpace(m[3])
		models = append(models, map[string]string{"name": name, "desc": desc})
		if isCurrent && current == "" {
			current = name
		}
	}
	return current, models
}
