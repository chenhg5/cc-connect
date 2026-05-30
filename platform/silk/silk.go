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

	handlerMu sync.Mutex // serialises all p.handler() calls (Claude CLI is not concurrent-safe)
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

// Compile-time interface check: silk implements core.InlineButtonSender
var _ core.InlineButtonSender = (*Platform)(nil)

func (p *Platform) SendWithButtons(ctx context.Context, replyCtx any, content string, buttons [][]core.ButtonOption) error {
	if p.metadataActive.Load() {
		return nil
	}
	var rows []map[string]any
	for _, row := range buttons {
		var btns []map[string]any
		for _, btn := range row {
			btns = append(btns, map[string]any{
				"label": btn.Text,
				"value": btn.Data,
			})
		}
		rows = append(rows, map[string]any{"row": btns})
	}
	return p.sendJSON(map[string]any{
		"type":    "question",
		"content": content,
		"options": rows,
	})
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

// blockSegment is a single event-driven content segment, recorded in the
// order events arrive from the engine (EventThinking → EventToolUse → EventText
// as they actually stream from the agent process).
type blockSegment struct {
	typ      string // "thinking", "tool_use", "text"
	content  string
	complete bool
	toolName string
}

type silkStreamingCard struct {
	platform *Platform
	mu       sync.Mutex
	failed   bool
	lastSent time.Time

	// Ordered segments reflecting real event order (not forced text-at-end).
	segments []blockSegment

	// Last-known state, used to detect changes between calls:
	//   thinking: when it changes → old thinking marked complete, new segment appended
	//   answer:   when it grows → merge into last text segment
	lastThinking string
	lastAnswer   string

	// Preserved for Finalize fallback: when lastAnswer is empty,
	// extractAnswerFromCardContent needs the thinking/tools prefix to strip.
	lastTools []core.StructuredTool
}

// buildBlocks serializes the ordered segment list into the wire format
// ([]map[string]any with index/type/content/isComplete/toolName).
func (c *silkStreamingCard) buildBlocks() []map[string]any {
	var blocks []map[string]any
	for i, seg := range c.segments {
		blocks = append(blocks, map[string]any{
			"index":      i,
			"type":       seg.typ,
			"content":    seg.content,
			"isComplete": seg.complete,
			"toolName":   seg.toolName,
		})
	}
	return blocks
}

// Update implements core.StreamingCard (legacy flat-text path, unused when StructuredContentStreamer is active).
func (c *silkStreamingCard) Update(ctx context.Context, content string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.platform.metadataActive.Load() {
		return nil
	}
	if time.Since(c.lastSent) < 500*time.Millisecond {
		return nil
	}
	return c.sendBlocks(nil, false)
}

// UpdateStructured implements core.StructuredContentStreamer.
// eventType is the engine event that triggered this call: "thinking",
// "tool_use", or "text".  Segments are recorded in call order so the
// resulting block list matches the real event stream rather than forcing
// text-after-tools.
func (c *silkStreamingCard) UpdateStructured(ctx context.Context, thinking string, tools []core.StructuredTool, answer string, eventType string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.platform.metadataActive.Load() {
		return nil
	}

	switch eventType {
	case "thinking":
		// Mark the previous thinking segment complete (if any) when a new
		// different thinking arrives.  The engine only tracks the latest
		// thinking text; a change means the old one is finished.
		if thinking != "" && thinking != c.lastThinking && c.lastThinking != "" {
			for i := len(c.segments) - 1; i >= 0; i-- {
				if c.segments[i].typ == "thinking" && !c.segments[i].complete {
					c.segments[i].complete = true
					break
				}
			}
		}
		if thinking != "" && thinking != c.lastThinking {
			c.segments = append(c.segments, blockSegment{typ: "thinking", content: thinking})
		}
		c.lastThinking = thinking

	case "tool_use":
		// Only append genuinely new tools.  EventToolUse fires per tool,
		// so each call adds one tool.  Count existing tool_use segments
		// to determine how many have already been recorded.
		existingTools := 0
		for _, seg := range c.segments {
			if seg.typ == "tool_use" {
				existingTools++
			}
		}
		for i := existingTools; i < len(tools); i++ {
			c.segments = append(c.segments, blockSegment{
				typ:      "tool_use",
				toolName: tools[i].Name,
				content:  tools[i].Input,
				complete: true,
			})
		}
		c.lastTools = tools

	case "text":
		// Answer text accumulates incrementally via EventText.  Merge into
		// the last segment when it is also text (full text, the frontend
		// treats it as an update of the same element).  When a new segment
		// must be created (preceded by thinking/tool_use), use only the
		// delta so earlier text is not duplicated.
		if answer != c.lastAnswer {
			n := len(c.segments)
			if n > 0 && c.segments[n-1].typ == "text" {
				c.segments[n-1].content = answer // full accumulated text, same segment
			} else {
				delta := answer
				if c.lastAnswer != "" && strings.HasPrefix(answer, c.lastAnswer) {
					delta = answer[len(c.lastAnswer):]
				}
				c.segments = append(c.segments, blockSegment{typ: "text", content: delta})
			}
			c.lastAnswer = answer
		}
	}

	// Debounce
	if time.Since(c.lastSent) < 500*time.Millisecond {
		return nil
	}
	c.lastSent = time.Now()
	return c.sendBlocks(c.buildBlocks(), false)
}

func (c *silkStreamingCard) Finalize(ctx context.Context, content string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Mark any still-incomplete thinking as complete.
	for i := range c.segments {
		if c.segments[i].typ == "thinking" && !c.segments[i].complete {
			c.segments[i].complete = true
		}
	}

	// Ensure answer text is present even if EventText never fired (the full
	// response may only be in the buildCardContent content parameter).
	if c.lastAnswer == "" && content != "" {
		answers := extractAnswerFromCardContent(content, c.lastThinking, c.lastTools)
		if answers != "" {
			c.segments = append(c.segments, blockSegment{typ: "text", content: answers, complete: true})
		}
	}

	blocks := c.buildBlocks()
	if err := c.sendBlocks(blocks, true); err != nil {
		return err
	}
	return fmt.Errorf("silk: finalize triggers reply fallback for clean answer")
}

// extractAnswerFromCardContent extracts the answer text portion from the
// buildCardContent format:
//
//	💭 **Thinking**\n\n[thinking]\n\n---\n\n🔧 **Tool #N**: `name`\n[input]\n\n---\n\n[answer]
//
// It removes the thinking and tool prefix portions, returning the raw answer.
func extractAnswerFromCardContent(cardContent, thinking string, tools []core.StructuredTool) string {
	// Reconstruct the prefix (thinking + tools, same format as buildCardContent)
	var prefix strings.Builder
	if thinking != "" {
		prefix.WriteString("💭 **Thinking**\n\n")
		prefix.WriteString(thinking)
		prefix.WriteString("\n\n---\n\n")
	}
	for _, t := range tools {
		prefix.WriteString(fmt.Sprintf("🔧 **Tool #%d**: `%s`\n", t.Index, t.Name))
		if t.Input != "" {
			prefix.WriteString(t.Input)
			prefix.WriteString("\n")
		}
		prefix.WriteString("\n")
	}

	rest := strings.TrimPrefix(cardContent, prefix.String())
	rest = strings.TrimPrefix(rest, "---\n\n")
	return strings.TrimSpace(rest)
}

func (c *silkStreamingCard) Failed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failed
}

func (c *silkStreamingCard) sendBlocks(blocks []map[string]any, done bool) error {
	if c.platform.metadataActive.Load() {
		return nil
	}
	msg := map[string]any{
		"type":        "reply_stream",
		"content":     "",
		"done":        done,
		"incremental": false,
	}
	if len(blocks) > 0 {
		msg["contentBlocks"] = blocks
	}
	return c.platform.sendJSON(msg)
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

	// Run metadata query inline (before accepting messages) to prevent
	// concurrent engine processing — the engine and Claude CLI do NOT
	// support concurrent requests. Without this, a racing metadata
	// goroutine and the main read loop both call p.handler, causing
	// Claude CLI to reject one with "上一个请求仍在处理中".
	p.handlerMu.Lock()
	p.queryAndSendMetadata()
	p.handlerMu.Unlock()

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
				p.handlerMu.Lock()
				p.handler(p, coreMsg)
				p.handlerMu.Unlock()
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
				p.handlerMu.Lock()
				p.handler(p, coreMsg)
				p.handlerMu.Unlock()
				// 仅对 /mode 和 /model 命令重新查询 metadata（更新前端的徽章状态）
				cmdText := strings.TrimSpace(cmd.Text)
				if cmdText == "/mode" || cmdText == "/model" || strings.HasPrefix(cmdText, "/mode ") || strings.HasPrefix(cmdText, "/model ") {
					go func() {
						time.Sleep(2 * time.Second)
						p.handlerMu.Lock()
						p.queryAndSendMetadataOnce()
						p.handlerMu.Unlock()
					}()
				}
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
	p.queryAndSendMetadataOnce()
}

// Returns true if metadata was successfully collected and sent.
func (p *Platform) queryAndSendMetadataOnce() bool {
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
	modeText := waitChan(p.metadataReply, 8*time.Second)
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
	modelText := waitChan(p.metadataReply, 15*time.Second)
	slog.Debug("[silk] raw /model response", "text", modelText)

	p.metadataActive.Store(false)

	mode, modes := parseModeResponse(modeText)
	model, models := parseModelResponse(modelText)

	if mode == "" && model == "" && len(modes) == 0 && len(models) == 0 {
		slog.Debug("[silk] metadata query returned nothing, skipping send")
		return false
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
	return true
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
// Alias: "> 1. alias - model_name" (ASCII hyphen with spaces, not em-dash)
var (
	// Alias lines use " - " between short label and full model id.
	modelAliasLineRe = regexp.MustCompile(`^(>\s*)?\d+\.\s+(\S+)\s+-\s+(\S+)\s*$`)
	// Regular lines use em-dash " — " only before optional description.
	modelLineRe      = regexp.MustCompile(`^(>\s*)?\d+\.\s+(.+?)(?:\s*—\s*(.+))?\s*$`)
	// Require a real model id (letter-first); skip "(未设置...)" / "(not set...)" default lines.
	modelCurrentRe = regexp.MustCompile(`(?:Current model|当前模型|當前模型|現在のモデル):\s*([a-zA-Z][a-zA-Z0-9._\[\]-]*)`)
)

func parseModelResponse(text string) (current string, models []map[string]string) {
	if text == "" {
		return "", nil
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if m := modelCurrentRe.FindStringSubmatch(line); m != nil {
			current = m[1]
			continue
		}
		var isCurrent bool
		var name, desc string
		if m := modelAliasLineRe.FindStringSubmatch(line); m != nil {
			isCurrent = strings.TrimSpace(m[1]) == ">"
			name = m[3]
			desc = m[2] // alias as short description
		} else if m := modelLineRe.FindStringSubmatch(line); m != nil {
			isCurrent = strings.TrimSpace(m[1]) == ">"
			name = strings.TrimSpace(m[2])
			desc = strings.TrimSpace(m[3])
		} else {
			continue
		}
		if name == "" {
			continue
		}
		models = append(models, map[string]string{"name": name, "desc": desc})
		if isCurrent && current == "" {
			current = name
		}
	}
	return current, models
}
