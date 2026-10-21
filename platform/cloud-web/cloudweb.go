package cloudweb

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterPlatform("cloud_web", New)
}

type Platform struct {
	name    string
	project string
	token   string

	transportKind string
	allowFrom     string
	shareInChannel bool
	groupReplyAll  bool

	mu               sync.RWMutex
	handler          core.MessageHandler
	navHandler       core.CardNavigationHandler
	lifecycleHandler core.PlatformLifecycleHandler
	cancel           context.CancelFunc
	stopping         bool

	tp    transport
	dedup core.MessageDedup

	replyCtxMu sync.RWMutex
	replyCtxs  map[string]string // sessionKey -> reply_ctx
}

// Compile-time interface checks.
var (
	_ core.Platform                  = (*Platform)(nil)
	_ core.ReplyContextReconstructor = (*Platform)(nil)
	_ core.ImageSender               = (*Platform)(nil)
	_ core.FileSender                = (*Platform)(nil)
	_ core.AudioSender               = (*Platform)(nil)
	_ core.InlineButtonSender        = (*Platform)(nil)
	_ core.CardSender                = (*Platform)(nil)
	_ core.CardNavigable             = (*Platform)(nil)
	_ core.CardRefresher             = (*Platform)(nil)
	_ core.TypingIndicator           = (*Platform)(nil)
	_ core.MessageUpdater            = (*Platform)(nil)
	_ core.PreviewStarter            = (*Platform)(nil)
	_ core.PreviewCleaner            = (*Platform)(nil)
	_ core.AsyncRecoverablePlatform  = (*Platform)(nil)
)

func New(opts map[string]any) (core.Platform, error) {
	name, _ := opts["name"].(string)
	if name == "" {
		name = "cloud_web"
	}
	project, _ := opts["cc_project"].(string)
	token, _ := opts["token"].(string)
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("cloud_web: token is required")
	}

	transportKind, _ := opts["transport"].(string)
	if transportKind == "" {
		transportKind = "websocket"
	}
	switch transportKind {
	case "websocket", "long_poll", "gateway":
	default:
		return nil, fmt.Errorf("cloud_web: transport must be websocket, long_poll, or gateway")
	}

	baseURL, _ := opts["base_url"].(string)
	wsURL, _ := opts["ws_url"].(string)
	listen, _ := opts["listen"].(string)
	webhookPath, _ := opts["webhook_path"].(string)
	registerURL, _ := opts["register_url"].(string)
	publicURL, _ := opts["public_url"].(string)
	eventsPath, _ := opts["events_path"].(string)
	sendPath, _ := opts["send_path"].(string)
	longPollMS := pickInt(opts["long_poll_timeout_ms"])

	allowFrom, _ := opts["allow_from"].(string)
	core.CheckAllowFrom(name, allowFrom)
	shareInChannel, _ := opts["share_session_in_channel"].(bool)
	groupReplyAll, _ := opts["group_reply_all"].(bool)

	var tp transport
	switch transportKind {
	case "websocket":
		resolved := resolveWSURL(baseURL, wsURL)
		if strings.TrimSpace(resolved) == "" {
			return nil, fmt.Errorf("cloud_web: ws_url or base_url is required for websocket transport")
		}
		tp = newWSTransport(resolved, token, name, project)
	case "long_poll":
		if strings.TrimSpace(baseURL) == "" {
			return nil, fmt.Errorf("cloud_web: base_url is required for long_poll transport")
		}
		tp = newPollTransport(baseURL, token, name, project, eventsPath, sendPath, longPollMS)
	case "gateway":
		if strings.TrimSpace(baseURL) == "" && strings.TrimSpace(registerURL) != "" {
			baseURL = deriveBaseURL(registerURL)
		}
		if strings.TrimSpace(baseURL) == "" && strings.TrimSpace(registerURL) == "" {
			return nil, fmt.Errorf("cloud_web: base_url or register_url is required for gateway transport")
		}
		if strings.TrimSpace(registerURL) != "" && strings.TrimSpace(publicURL) == "" {
			return nil, fmt.Errorf("cloud_web: public_url is required when register_url is set for gateway transport")
		}
		tp = newGatewayTransport(baseURL, token, name, project, listen, webhookPath, registerURL, publicURL, sendPath)
	}

	return &Platform{
		name:           name,
		project:        project,
		token:          token,
		transportKind:  transportKind,
		allowFrom:      allowFrom,
		shareInChannel: shareInChannel,
		groupReplyAll:  groupReplyAll,
		tp:             tp,
		replyCtxs:      make(map[string]string),
	}, nil
}

func pickInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case string:
		if n == "" {
			return 0
		}
		var i int
		if _, err := fmt.Sscanf(n, "%d", &i); err == nil {
			return i
		}
	}
	return 0
}

func (p *Platform) Name() string { return p.name }

func (p *Platform) SetLifecycleHandler(h core.PlatformLifecycleHandler) {
	p.lifecycleHandler = h
}

func (p *Platform) SetCardNavigationHandler(h core.CardNavigationHandler) {
	p.navHandler = h
}

func (p *Platform) Start(handler core.MessageHandler) error {
	p.mu.Lock()
	if p.stopping {
		p.mu.Unlock()
		return fmt.Errorf("cloud_web: platform stopped")
	}
	p.handler = handler
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.mu.Unlock()

	if ws, ok := p.tp.(*wsTransport); ok {
		ws.onConnected = p.notifyReady
		ws.onDisconnected = p.notifyUnavailable
	}

	if err := p.tp.Start(ctx, p.handleWire); err != nil {
		cancel()
		return err
	}

	// WebSocket connects asynchronously; readiness is signaled after register_ack.
	if _, isWS := p.tp.(*wsTransport); !isWS {
		p.notifyReady()
	}
	slog.Info("cloud_web: platform started", "name", p.name, "transport", p.transportKind)
	return nil
}

func (p *Platform) notifyReady() {
	if p.lifecycleHandler != nil {
		p.lifecycleHandler.OnPlatformReady(p)
	}
}

func (p *Platform) notifyUnavailable(err error) {
	if err == nil {
		return
	}
	p.mu.RLock()
	stopping := p.stopping
	p.mu.RUnlock()
	if stopping {
		return
	}
	if p.lifecycleHandler != nil {
		p.lifecycleHandler.OnPlatformUnavailable(p, err)
	}
}

func (p *Platform) Stop() error {
	p.mu.Lock()
	p.stopping = true
	cancel := p.cancel
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return p.tp.Stop()
}

func (p *Platform) caps() map[string]bool {
	return p.tp.Capabilities()
}

func (p *Platform) hasCap(name string) bool {
	return p.caps()[name]
}

func (p *Platform) storeReplyCtx(sessionKey, replyCtx string) {
	if sessionKey == "" || replyCtx == "" {
		return
	}
	p.replyCtxMu.Lock()
	p.replyCtxs[sessionKey] = replyCtx
	p.replyCtxMu.Unlock()
}

func (p *Platform) lookupReplyCtx(sessionKey string) (string, bool) {
	p.replyCtxMu.RLock()
	defer p.replyCtxMu.RUnlock()
	v, ok := p.replyCtxs[sessionKey]
	return v, ok
}

func (p *Platform) handleWire(raw []byte) {
	var base wireMsg
	if err := json.Unmarshal(raw, &base); err != nil {
		slog.Debug("cloud_web: invalid inbound json", "error", err)
		return
	}
	switch base.Type {
	case "message":
		p.handleMessage(raw)
	case "card_action":
		p.handleCardAction(raw)
	case "message_recall":
		p.handleRecall(raw)
	case "ping":
		_ = p.tp.Send(context.Background(), map[string]any{"type": "pong", "ts": time.Now().UnixMilli()})
	default:
		slog.Debug("cloud_web: unknown inbound type", "type", base.Type)
	}
}

func (p *Platform) handleMessage(raw []byte) {
	var m wireInboundMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		slog.Debug("cloud_web: parse message", "error", err)
		return
	}
	if m.UserID == "" {
		return
	}
	if !core.AllowList(p.allowFrom, m.UserID) {
		slog.Debug("cloud_web: unauthorized user", "user", m.UserID)
		return
	}
	if isGroupChat(m.ChatType) && !p.groupReplyAll {
		// Require explicit mention in group chats unless group_reply_all is enabled.
		if m.Mentioned == nil || !*m.Mentioned {
			return
		}
	}

	hasContent := strings.TrimSpace(m.Content) != ""
	hasAttachments := len(m.Images) > 0 || len(m.Files) > 0 || m.Audio != nil
	if !hasContent && !hasAttachments {
		return
	}

	msgID := m.MsgID
	if msgID == "" {
		msgID = fmt.Sprintf("cw-%d", time.Now().UnixNano())
	}
	if p.dedup.IsDuplicate(msgID) {
		return
	}

	sessionKey := buildSessionKey(p.name, m.ChatID, m.UserID, p.shareInChannel, m.SessionKey)
	replyCtx := m.ReplyCtx
	if replyCtx == "" {
		replyCtx = sessionKey
	}
	p.storeReplyCtx(sessionKey, replyCtx)

	rc := replyContext{SessionKey: sessionKey, ReplyCtx: replyCtx}
	msg := &core.Message{
		SessionKey: sessionKey,
		Platform:   p.name,
		MessageID:  msgID,
		ChannelID:  m.ChatID,
		UserID:     m.UserID,
		UserName:   firstNonEmpty(m.UserName, m.UserID),
		Content:    m.Content,
		Images:     decodeImages(m.Images),
		Files:      decodeFiles(m.Files),
		Audio:      decodeAudio(m.Audio),
		ReplyCtx:   rc,
	}

	p.mu.RLock()
	h := p.handler
	p.mu.RUnlock()
	if h != nil {
		h(p, msg)
	}
}

func (p *Platform) handleCardAction(raw []byte) {
	var ca wireCardAction
	if err := json.Unmarshal(raw, &ca); err != nil {
		return
	}
	if ca.SessionKey == "" {
		return
	}
	replyCtx := ca.ReplyCtx
	if replyCtx == "" {
		if v, ok := p.lookupReplyCtx(ca.SessionKey); ok {
			replyCtx = v
		}
	}
	rc := replyContext{SessionKey: ca.SessionKey, ReplyCtx: replyCtx}

	if strings.HasPrefix(ca.Action, "perm:") {
		var responseText string
		switch ca.Action {
		case "perm:allow":
			responseText = "allow"
		case "perm:deny":
			responseText = "deny"
		case "perm:allow_all":
			responseText = "allow all"
		default:
			return
		}
		p.dispatchAsPermissionResponse(ca.SessionKey, rc, responseText)
		return
	}
	if strings.HasPrefix(ca.Action, "askq:") {
		p.dispatchAsMessage(ca.SessionKey, rc, ca.Action)
		return
	}
	if strings.HasPrefix(ca.Action, "cmd:") {
		p.dispatchAsMessage(ca.SessionKey, rc, strings.TrimPrefix(ca.Action, "cmd:"))
		return
	}

	p.mu.RLock()
	nav := p.navHandler
	p.mu.RUnlock()
	if nav == nil {
		return
	}
	card := nav(ca.Action, ca.SessionKey)
	if card == nil {
		return
	}
	if p.hasCap(capCard) {
		_ = p.sendWire(context.Background(), map[string]any{
			"type":        "card",
			"session_key": ca.SessionKey,
			"reply_ctx":   replyCtx,
			"card":        serializeCard(card),
		})
	} else {
		_ = p.Reply(context.Background(), rc, card.RenderText())
	}
}

func (p *Platform) handleRecall(raw []byte) {
	var r wireMessageRecall
	if err := json.Unmarshal(raw, &r); err != nil || r.MsgID == "" {
		return
	}
	replyCtx, ok := p.lookupReplyCtx(r.SessionKey)
	if !ok {
		replyCtx = ""
	}
	rc := replyContext{SessionKey: r.SessionKey, ReplyCtx: replyCtx}
	msg := &core.Message{
		SessionKey: r.SessionKey,
		Platform:   p.name,
		MessageID:  r.MsgID,
		Recalled:   true,
		ReplyCtx:   rc,
	}
	p.mu.RLock()
	h := p.handler
	p.mu.RUnlock()
	if h != nil {
		h(p, msg)
	}
}

func (p *Platform) dispatchAsMessage(sessionKey string, rc replyContext, content string) {
	p.mu.RLock()
	h := p.handler
	p.mu.RUnlock()
	if h == nil {
		return
	}
	msg := &core.Message{
		SessionKey: sessionKey,
		Platform:   p.name,
		UserID:     "system",
		UserName:   "System",
		Content:    content,
		ReplyCtx:   rc,
	}
	go h(p, msg)
}

// dispatchAsPermissionResponse is the permission-callback sibling of
// dispatchAsMessage. It sets IsPermissionResponse so the engine can drop
// stale clicks (e.g. tapping an old "Allow" card after the session reset)
// instead of letting the literal "allow"/"deny" string reach the agent
// prompt stream. Mirrors the feishu/qqbot/telegram/bridge convention.
func (p *Platform) dispatchAsPermissionResponse(sessionKey string, rc replyContext, content string) {
	p.mu.RLock()
	h := p.handler
	p.mu.RUnlock()
	if h == nil {
		return
	}
	msg := &core.Message{
		SessionKey:           sessionKey,
		Platform:             p.name,
		UserID:               "system",
		UserName:             "System",
		Content:              content,
		ReplyCtx:             rc,
		IsPermissionResponse: true,
	}
	go h(p, msg)
}

func (p *Platform) Reply(ctx context.Context, replyCtx any, content string) error {
	rc, err := parseReplyCtx(replyCtx)
	if err != nil {
		return err
	}
	return p.sendWire(ctx, map[string]any{
		"type":        "reply",
		"session_key": rc.SessionKey,
		"reply_ctx":   rc.ReplyCtx,
		"content":     content,
		"format":      "text",
	})
}

func (p *Platform) Send(ctx context.Context, replyCtx any, content string) error {
	return p.Reply(ctx, replyCtx, content)
}

func (p *Platform) SendWithButtons(ctx context.Context, replyCtx any, content string, buttons [][]core.ButtonOption) error {
	rc, err := parseReplyCtx(replyCtx)
	if err != nil {
		return err
	}
	if !p.hasCap(capButtons) {
		return p.Reply(ctx, rc, content)
	}
	return p.sendWire(ctx, map[string]any{
		"type":        "buttons",
		"session_key": rc.SessionKey,
		"reply_ctx":   rc.ReplyCtx,
		"content":     content,
		"buttons":     buttons,
	})
}

func (p *Platform) SendCard(ctx context.Context, replyCtx any, card *core.Card) error {
	rc, err := parseReplyCtx(replyCtx)
	if err != nil {
		return err
	}
	if !p.hasCap(capCard) {
		return p.Reply(ctx, rc, card.RenderText())
	}
	return p.sendWire(ctx, map[string]any{
		"type":        "card",
		"session_key": rc.SessionKey,
		"reply_ctx":   rc.ReplyCtx,
		"card":        serializeCard(card),
	})
}

func (p *Platform) ReplyCard(ctx context.Context, replyCtx any, card *core.Card) error {
	return p.SendCard(ctx, replyCtx, card)
}

func (p *Platform) RefreshCard(ctx context.Context, sessionKey string, card *core.Card) error {
	replyCtx, ok := p.lookupReplyCtx(sessionKey)
	if !ok {
		return fmt.Errorf("cloud_web: no reply context for session %q", sessionKey)
	}
	if !p.hasCap(capCard) {
		return p.Reply(ctx, replyContext{SessionKey: sessionKey, ReplyCtx: replyCtx}, card.RenderText())
	}
	return p.sendWire(ctx, map[string]any{
		"type":        "card",
		"session_key": sessionKey,
		"reply_ctx":   replyCtx,
		"card":        serializeCard(card),
	})
}

func (p *Platform) UpdateMessage(ctx context.Context, replyCtx any, content string) error {
	rc, err := parseReplyCtx(replyCtx)
	if err != nil {
		return err
	}
	if !p.hasCap(capUpdateMessage) {
		return core.ErrNotSupported
	}
	return p.sendWire(ctx, map[string]any{
		"type":           "update_message",
		"session_key":    rc.SessionKey,
		"preview_handle": rc.ReplyCtx,
		"content":        content,
	})
}

func (p *Platform) SendPreviewStart(ctx context.Context, replyCtx any, content string) (any, error) {
	rc, err := parseReplyCtx(replyCtx)
	if err != nil {
		return nil, err
	}
	if !p.hasCap(capPreview) || !p.hasCap(capUpdateMessage) {
		return nil, core.ErrNotSupported
	}
	refID := fmt.Sprintf("prev-%d", time.Now().UnixNano())
	if err := p.sendWire(ctx, map[string]any{
		"type":        "preview_start",
		"ref_id":      refID,
		"session_key": rc.SessionKey,
		"reply_ctx":   rc.ReplyCtx,
		"content":     content,
	}); err != nil {
		return nil, err
	}
	waiter, ok := p.tp.(previewWaiter)
	if !ok {
		return nil, fmt.Errorf("cloud_web: transport does not support preview_ack")
	}
	handle, err := waiter.waitPreviewAck(refID, 10*time.Second)
	if err != nil {
		return nil, err
	}
	return replyContext{SessionKey: rc.SessionKey, ReplyCtx: handle}, nil
}

func (p *Platform) DeletePreviewMessage(ctx context.Context, previewHandle any) error {
	rc, err := parseReplyCtx(previewHandle)
	if err != nil {
		return err
	}
	if !p.hasCap(capDeleteMessage) {
		return core.ErrNotSupported
	}
	return p.sendWire(ctx, map[string]any{
		"type":           "delete_message",
		"session_key":    rc.SessionKey,
		"preview_handle": rc.ReplyCtx,
	})
}

func (p *Platform) StartTyping(ctx context.Context, replyCtx any) (stop func()) {
	rc, err := parseReplyCtx(replyCtx)
	if err != nil || !p.hasCap(capTyping) {
		return func() {}
	}
	_ = p.sendWire(ctx, map[string]any{
		"type":        "typing_start",
		"session_key": rc.SessionKey,
		"reply_ctx":   rc.ReplyCtx,
	})
	return func() {
		_ = p.sendWire(context.Background(), map[string]any{
			"type":        "typing_stop",
			"session_key": rc.SessionKey,
			"reply_ctx":   rc.ReplyCtx,
		})
	}
}

func (p *Platform) SendImage(ctx context.Context, replyCtx any, img core.ImageAttachment) error {
	rc, err := parseReplyCtx(replyCtx)
	if err != nil {
		return err
	}
	if !p.hasCap(capImage) {
		return core.ErrNotSupported
	}
	return p.sendWire(ctx, map[string]any{
		"type":        "image",
		"session_key": rc.SessionKey,
		"reply_ctx":   rc.ReplyCtx,
		"data":        base64.StdEncoding.EncodeToString(img.Data),
		"mime_type":   img.MimeType,
		"file_name":   img.FileName,
	})
}

func (p *Platform) SendFile(ctx context.Context, replyCtx any, file core.FileAttachment) error {
	rc, err := parseReplyCtx(replyCtx)
	if err != nil {
		return err
	}
	if !p.hasCap(capFile) {
		return core.ErrNotSupported
	}
	return p.sendWire(ctx, map[string]any{
		"type":        "file",
		"session_key": rc.SessionKey,
		"reply_ctx":   rc.ReplyCtx,
		"data":        base64.StdEncoding.EncodeToString(file.Data),
		"mime_type":   file.MimeType,
		"file_name":   file.FileName,
	})
}

func (p *Platform) SendAudio(ctx context.Context, replyCtx any, audio []byte, format string) error {
	rc, err := parseReplyCtx(replyCtx)
	if err != nil {
		return err
	}
	if !p.hasCap(capAudio) {
		return core.ErrNotSupported
	}
	return p.sendWire(ctx, map[string]any{
		"type":        "audio",
		"session_key": rc.SessionKey,
		"reply_ctx":   rc.ReplyCtx,
		"data":        base64.StdEncoding.EncodeToString(audio),
		"format":      format,
	})
}

func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	if !p.hasCap(capReconstructReply) {
		return nil, fmt.Errorf("cloud_web: reconstruct_reply not supported by gateway")
	}
	replyCtx, ok := p.lookupReplyCtx(sessionKey)
	if !ok {
		return nil, fmt.Errorf("cloud_web: no stored reply context for session %q", sessionKey)
	}
	return replyContext{SessionKey: sessionKey, ReplyCtx: replyCtx}, nil
}

func (p *Platform) sendWire(ctx context.Context, msg map[string]any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return p.tp.Send(ctx, msg)
}

func parseReplyCtx(v any) (replyContext, error) {
	switch rc := v.(type) {
	case replyContext:
		return rc, nil
	case *replyContext:
		if rc == nil {
			return replyContext{}, fmt.Errorf("cloud_web: nil reply context")
		}
		return *rc, nil
	default:
		return replyContext{}, fmt.Errorf("cloud_web: invalid reply context type %T", v)
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
