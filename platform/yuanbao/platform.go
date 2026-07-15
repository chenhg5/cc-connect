package yuanbao

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

const (
	heartbeatInterval     = 30 * time.Second
	heartbeatTimeout      = 10 * time.Second
	heartbeatThreshold    = 2
	authTimeout           = 10 * time.Second
	replyHeartbeatIntv    = 2 * time.Second
	replyHeartbeatTimeout = 30 * time.Second
	dedupCleanupInterval  = 5 * time.Minute
)

var authFailedCodes = map[int]bool{
	4001: true, 4002: true, 4003: true,
}

type replyContext struct {
	chatType string
	chatID   string
	targetID string
}

type Platform struct {
	appKey    string
	appSecret string
	allowFrom string
	apiDomain string
	wsURL     string
	routeEnv  string

	mu      sync.Mutex
	ws      *websocket.Conn
	handler core.MessageHandler
	tokens  *tokenManager
	botID   string
	source  string

	heartbeatTimer     *time.Timer
	dedupCleanupTimer  *time.Timer
	dedupSet           map[string]time.Time
	replyHBTimers      map[string]*time.Timer
	replyHBStop        map[string]chan struct{}
	reconnectAttempts  int
	shouldReconnect    bool
	done               chan struct{}
	pendingAcks        map[string]chan<- []byte
	pendingMu          sync.Mutex
	consecutiveHbFails int
}

func New(opts map[string]any) (core.Platform, error) {
	botToken, _ := opts["bot_token"].(string)
	appKey, appSecret := splitBotToken(botToken)
	allowFrom, _ := opts["allow_from"].(string)
	if appKey == "" || appSecret == "" {
		return nil, fmt.Errorf("yuanbao: bot_token is required (format: app_key:app_secret)")
	}
	apiDomain, _ := opts["api_domain"].(string)
	wsURL, _ := opts["ws_url"].(string)
	routeEnv, _ := opts["route_env"].(string)
	if apiDomain == "" {
		apiDomain = defaultAPIDomain
	}
	if wsURL == "" {
		wsURL = defaultWSURL
	}
	core.CheckAllowFrom("yuanbao", allowFrom)
	return &Platform{
		appKey: appKey, appSecret: appSecret, allowFrom: allowFrom,
		apiDomain: apiDomain, wsURL: wsURL, routeEnv: routeEnv,
		tokens:        newTokenManager(),
		dedupSet:      make(map[string]time.Time),
		replyHBTimers: make(map[string]*time.Timer),
		replyHBStop:   make(map[string]chan struct{}),
		pendingAcks:   make(map[string]chan<- []byte),
		done:          make(chan struct{}),
	}, nil
}

func (p *Platform) Name() string { return "yuanbao" }

// splitBotToken splits a "app_key:app_secret" string. An empty input yields
// empty outputs so callers can detect the missing-config case with a simple
// `appKey == "" || appSecret == ""` check.
func splitBotToken(raw string) (appKey, appSecret string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	idx := strings.Index(raw, ":")
	if idx <= 0 || idx >= len(raw)-1 {
		return "", ""
	}
	return strings.TrimSpace(raw[:idx]), strings.TrimSpace(raw[idx+1:])
}

func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler
	p.shouldReconnect = true
	go p.run()
	return nil
}

func (p *Platform) run() {
	for {
		p.mu.Lock()
		p.reconnectAttempts = 0
		p.mu.Unlock()
		if !p.connect() {
			p.mu.Lock()
			shouldReconnect := p.shouldReconnect
			p.mu.Unlock()
			if !shouldReconnect {
				return
			}
			time.Sleep(5 * time.Second)
			continue
		}
		<-p.done
		p.mu.Lock()
		shouldReconnect := p.shouldReconnect
		attempts := p.reconnectAttempts
		p.mu.Unlock()
		if !shouldReconnect {
			return
		}
		delay := time.Duration(min(1000*(1<<uint(attempts)), 60000)) * time.Millisecond
		p.mu.Lock()
		p.reconnectAttempts++
		p.mu.Unlock()
		slog.Info("yuanbao: reconnecting", "attempt", attempts+1, "delay", delay)
		time.Sleep(delay)
	}
}

func (p *Platform) connect() bool {
	tokenData, err := p.tokens.getToken(p.appKey, p.appSecret, p.apiDomain, p.routeEnv)
	if err != nil {
		slog.Error("yuanbao: get sign token failed", "error", err)
		return false
	}
	if tokenData.botID != "" {
		p.mu.Lock()
		p.botID = tokenData.botID
		p.source = tokenData.source
		p.mu.Unlock()
	}
	u, err := url.Parse(p.wsURL)
	if err != nil {
		slog.Error("yuanbao: invalid ws url", "url", p.wsURL, "error", err)
		return false
	}
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		slog.Error("yuanbao: websocket dial failed", "error", err)
		return false
	}
	p.mu.Lock()
	p.ws = conn
	p.done = make(chan struct{})
	p.mu.Unlock()
	if !p.authenticate(tokenData.token) {
		p.mu.Lock()
		_ = p.ws.Close()
		p.ws = nil
		p.mu.Unlock()
		return false
	}
	slog.Info("yuanbao: connected and authenticated", "bot_id", p.botID)
	p.startHeartbeat()
	p.startDedupCleanup()
	go p.readLoop()
	return true
}

func (p *Platform) authenticate(token string) bool {
	msgID := fmt.Sprintf("auth_%d", time.Now().UnixNano())
	authBytes := encodeAuthBind(p.botID, p.source, token, msgID, "", "darwin", "", p.routeEnv)
	p.mu.Lock()
	ws := p.ws
	p.mu.Unlock()
	if ws == nil {
		return false
	}
	if err := ws.WriteMessage(websocket.BinaryMessage, authBytes); err != nil {
		slog.Error("yuanbao: write auth bind failed", "error", err)
		return false
	}
	if err := ws.SetReadDeadline(time.Now().Add(authTimeout)); err != nil {
		slog.Error("yuanbao: set read deadline failed", "error", err)
		return false
	}
	defer func() { _ = ws.SetReadDeadline(time.Time{}) }()
	for {
		_, raw, err := ws.ReadMessage()
		if err != nil {
			slog.Error("yuanbao: read auth response failed", "error", err)
			return false
		}
		msg, err := decodeConnMsg(raw)
		if err != nil {
			continue
		}
		if msg.head.cmdType == cmdTypeResponse && msg.head.cmd == cmdAuthBind {
			rsp := decodeAuthBindRsp(msg.data)
			if rsp.code == 0 {
				return true
			}
			slog.Error("yuanbao: auth failed", "code", rsp.code, "message", rsp.message)
			if authFailedCodes[rsp.code] {
				p.tokens.forceRefresh()
			}
			return false
		}
	}
}

func (p *Platform) readLoop() {
	ws := p.getWS()
	if ws == nil {
		return
	}
	defer func() {
		p.stopHeartbeat()
		p.stopDedupCleanup()
		p.stopAllReplyHeartbeats()
		if p.ws != nil {
			_ = p.ws.Close()
		}
		close(p.done)
	}()
	for {
		if err := ws.SetReadDeadline(time.Now().Add(90 * time.Second)); err != nil {
			return
		}
		_, raw, err := ws.ReadMessage()
		if err != nil {
			return
		}
		p.handleFrame(raw)
	}
}

func (p *Platform) handleFrame(raw []byte) {
	msg, err := decodeConnMsg(raw)
	if err != nil {
		return
	}
	head := msg.head
	switch {
	case head.cmdType == cmdTypeResponse && head.cmd == cmdPing:
		p.resolvePendingAck(head.msgID, nil)
	case head.cmdType == cmdTypeResponse && (head.cmd == "send_c2c_message" || head.cmd == "send_group_message" ||
		head.cmd == "send_private_heartbeat" || head.cmd == "send_group_heartbeat"):
		return
	case head.cmdType == cmdTypeResponse:
		if head.cmd != cmdAuthBind {
			p.resolvePendingAck(head.msgID, msg.data)
		}
	case head.cmdType == cmdTypePush:
		if head.needAck {
			if ws := p.getWS(); ws != nil {
				_ = ws.WriteMessage(websocket.BinaryMessage, encodePushAck(head))
			}
		}
		if head.cmd == "inbound_message" || head.cmd == "InboundMessagePush" {
			p.handleInboundPush(msg.data)
		} else if len(msg.data) > 0 {
			p.handleInboundPush(msg.data)
		}
	case head.cmd == cmdKickout:
		slog.Warn("yuanbao: kicked out by server")
		p.mu.Lock()
		p.shouldReconnect = false
		p.mu.Unlock()
		p.disconnect()
	}
}

func (p *Platform) handleInboundPush(data []byte) {
	push := p.decodePush(data)
	if push == nil {
		slog.Warn("yuanbao: failed to decode inbound push")
		return
	}
	if push.msgID != "" {
		p.mu.Lock()
		if _, ok := p.dedupSet[push.msgID]; ok {
			p.mu.Unlock()
			return
		}
		p.dedupSet[push.msgID] = time.Now()
		p.mu.Unlock()
	}
	botID := p.getBotID()
	if push.fromAccount == botID || !core.AllowList(p.allowFrom, push.fromAccount) {
		return
	}
	text := inboundText(push)
	if text == "" {
		return
	}
	isGroup := push.groupCode != ""
	var chatID, targetID string
	if isGroup {
		chatID = "group:" + push.groupCode
		targetID = push.groupCode
	} else {
		chatID = "dm:" + push.fromAccount
		targetID = push.fromAccount
	}
	userName := push.senderNickname
	if userName == "" {
		userName = push.fromAccount
	}
	p.handler(p, &core.Message{
		SessionKey: fmt.Sprintf("yuanbao:%s", chatID),
		Platform:   "yuanbao",
		MessageID:  push.msgID,
		UserID:     push.fromAccount,
		UserName:   userName,
		ChatName:   push.groupName,
		Content:    text,
		ReplyCtx: replyContext{
			chatType: map[bool]string{true: "group", false: "dm"}[isGroup],
			chatID:   chatID,
			targetID: targetID,
		},
	})
}

func (p *Platform) decodePush(data []byte) *inboundPush {
	var jp jsonInboundPush
	if json.Unmarshal(data, &jp) == nil && jp.FromAccount != "" {
		return &inboundPush{
			callbackCommand: jp.CallbackCommand,
			fromAccount:     jp.FromAccount,
			toAccount:       jp.ToAccount,
			senderNickname:  jp.SenderNickname,
			groupCode:       jp.GroupCode,
			groupName:       jp.GroupName,
			msgID:           jp.MsgID,
			msgKey:          jp.MsgKey,
			msgTime:         jp.MsgTime,
			msgBody:         parseJSONMsgBody(jp.MsgBody),
		}
	}
	return decodeInboundPush(data)
}

func parseJSONMsgBody(raw json.RawMessage) []msgBodyElement {
	if len(raw) == 0 {
		return nil
	}
	var items []map[string]interface{}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	var result []msgBodyElement
	for _, item := range items {
		elem := msgBodyElement{}
		if mt, _ := item["msg_type"].(string); mt != "" {
			elem.msgType = mt
		} else if mt, _ := item["MsgType"].(string); mt != "" {
			elem.msgType = mt
		}
		if content, ok := item["msg_content"].(map[string]interface{}); ok {
			elem.msgContent = content
		} else if mc, ok := item["MsgContent"]; ok {
			switch v := mc.(type) {
			case map[string]interface{}:
				elem.msgContent = v
			case string:
				_ = json.Unmarshal([]byte(v), &elem.msgContent)
			}
		}
		result = append(result, elem)
	}
	return result
}

func (p *Platform) Reply(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("yuanbao: invalid reply context type %T", rctx)
	}
	if content == "" {
		return nil
	}
	content = core.StripMarkdown(content)
	p.startReplyHeartbeat(rc.chatID)
	defer p.stopReplyHeartbeat(rc.chatID, true)
	msgBody := [][]byte{encodeTextBody(content)}
	var frame []byte
	if rc.chatType == "group" {
		frame = encodeSendGroupMessage(rc.targetID, msgBody, p.getBotID(), "", "", "")
	} else {
		frame = encodeSendC2CMessage(rc.targetID, msgBody, p.getBotID(), "", 0, "", "")
	}
	ws := p.getWS()
	if ws == nil {
		return fmt.Errorf("yuanbao: not connected")
	}
	err := ws.WriteMessage(websocket.BinaryMessage, frame)
	if err != nil {
		slog.Error("yuanbao: reply send failed", "error", err)
	}
	return err
}

func (p *Platform) Send(ctx context.Context, rctx any, content string) error {
	return p.Reply(ctx, rctx, content)
}

func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	parts := strings.SplitN(sessionKey, ":", 3)
	if len(parts) < 2 || parts[0] != "yuanbao" {
		return nil, fmt.Errorf("yuanbao: invalid session key %q", sessionKey)
	}
	chatID := parts[1]
	isGroup := strings.HasPrefix(chatID, "group:")
	targetID := strings.TrimPrefix(chatID, "dm:")
	targetID = strings.TrimPrefix(targetID, "group:")
	return replyContext{
		chatType: map[bool]string{true: "group", false: "dm"}[isGroup],
		chatID:   chatID,
		targetID: targetID,
	}, nil
}

func (p *Platform) Stop() error {
	p.mu.Lock()
	p.shouldReconnect = false
	ws := p.ws
	p.mu.Unlock()
	if ws != nil {
		_ = ws.Close()
	}
	return nil
}

func (p *Platform) startHeartbeat() {
	p.stopHeartbeat()
	p.mu.Lock()
	p.heartbeatTimer = time.AfterFunc(heartbeatInterval, p.doHeartbeat)
	p.mu.Unlock()
}

func (p *Platform) stopHeartbeat() {
	p.mu.Lock()
	if p.heartbeatTimer != nil {
		p.heartbeatTimer.Stop()
		p.heartbeatTimer = nil
	}
	p.mu.Unlock()
}

func (p *Platform) doHeartbeat() {
	ws := p.getWS()
	if ws == nil {
		return
	}
	msgID := fmt.Sprintf("ping_%d", time.Now().UnixNano())
	pingBytes := encodePing(msgID)
	ack := make(chan []byte, 1)
	p.pendingMu.Lock()
	p.pendingAcks[msgID] = ack
	p.pendingMu.Unlock()
	if err := ws.WriteMessage(websocket.BinaryMessage, pingBytes); err != nil {
		p.scheduleReconnect()
		return
	}
	select {
	case <-ack:
		p.mu.Lock()
		p.consecutiveHbFails = 0
		p.mu.Unlock()
	case <-time.After(heartbeatTimeout):
		p.mu.Lock()
		p.consecutiveHbFails++
		if p.consecutiveHbFails >= heartbeatThreshold {
			p.mu.Unlock()
			slog.Warn("yuanbao: heartbeat timeout, reconnecting")
			p.scheduleReconnect()
			return
		}
		p.mu.Unlock()
	}
	p.mu.Lock()
	p.heartbeatTimer = time.AfterFunc(heartbeatInterval, p.doHeartbeat)
	p.mu.Unlock()
}

func (p *Platform) startReplyHeartbeat(chatID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.replyHBTimers[chatID]; ok {
		return
	}
	stop := make(chan struct{})
	p.replyHBStop[chatID] = stop
	// Send initial heartbeat directly without re-acquiring the lock.
	if p.ws != nil {
		var frame []byte
		if strings.HasPrefix(chatID, "group:") {
			frame = encodeSendGroupHeartbeat(p.botID, strings.TrimPrefix(chatID, "group:"), 1)
		} else {
			frame = encodeSendPrivateHeartbeat(p.botID, strings.TrimPrefix(chatID, "dm:"), 1)
		}
		_ = p.ws.WriteMessage(websocket.BinaryMessage, frame)
	}
	timer := time.AfterFunc(replyHeartbeatIntv, func() {
		select {
		case <-stop:
			return
		default:
			p.doReplyHeartbeat(chatID)
		}
	})
	p.replyHBTimers[chatID] = timer
	go func() {
		time.Sleep(replyHeartbeatTimeout)
		p.stopReplyHeartbeat(chatID, true)
	}()
}

func (p *Platform) doReplyHeartbeat(chatID string) {
	ws := p.getWS()
	if ws == nil {
		return
	}
	botID := p.getBotID()
	var frame []byte
	if strings.HasPrefix(chatID, "group:") {
		frame = encodeSendGroupHeartbeat(botID, strings.TrimPrefix(chatID, "group:"), 1)
	} else {
		frame = encodeSendPrivateHeartbeat(botID, strings.TrimPrefix(chatID, "dm:"), 1)
	}
	_ = ws.WriteMessage(websocket.BinaryMessage, frame)
}

func (p *Platform) stopReplyHeartbeat(chatID string, sendFinish bool) {
	p.mu.Lock()
	if t, ok := p.replyHBTimers[chatID]; ok {
		t.Stop()
		delete(p.replyHBTimers, chatID)
	}
	if stop, ok := p.replyHBStop[chatID]; ok {
		close(stop)
		delete(p.replyHBStop, chatID)
	}
	botID, ws := p.botID, p.ws
	p.mu.Unlock()
	if sendFinish && ws != nil {
		var frame []byte
		if strings.HasPrefix(chatID, "group:") {
			frame = encodeSendGroupHeartbeat(botID, strings.TrimPrefix(chatID, "group:"), 2)
		} else {
			frame = encodeSendPrivateHeartbeat(botID, strings.TrimPrefix(chatID, "dm:"), 2)
		}
		_ = ws.WriteMessage(websocket.BinaryMessage, frame)
	}
}

func (p *Platform) stopAllReplyHeartbeats() {
	p.mu.Lock()
	for chatID, t := range p.replyHBTimers {
		t.Stop()
		if stop, ok := p.replyHBStop[chatID]; ok {
			close(stop)
		}
	}
	p.replyHBTimers = make(map[string]*time.Timer)
	p.replyHBStop = make(map[string]chan struct{})
	p.mu.Unlock()
}

func (p *Platform) startDedupCleanup() {
	p.mu.Lock()
	p.dedupCleanupTimer = time.AfterFunc(dedupCleanupInterval, func() {
		p.mu.Lock()
		p.dedupSet = make(map[string]time.Time)
		p.mu.Unlock()
		p.startDedupCleanup()
	})
	p.mu.Unlock()
}

func (p *Platform) stopDedupCleanup() {
	p.mu.Lock()
	if p.dedupCleanupTimer != nil {
		p.dedupCleanupTimer.Stop()
		p.dedupCleanupTimer = nil
	}
	p.mu.Unlock()
}

func (p *Platform) scheduleReconnect() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ws != nil {
		_ = p.ws.Close()
	}
	close(p.done)
	p.done = make(chan struct{})
}

func (p *Platform) disconnect() {
	p.mu.Lock()
	ws := p.ws
	p.mu.Unlock()
	if ws != nil {
		_ = ws.Close()
	}
}

func (p *Platform) getWS() *websocket.Conn {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ws
}

func (p *Platform) getBotID() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.botID
}

func (p *Platform) resolvePendingAck(msgID string, data []byte) {
	p.pendingMu.Lock()
	ch, ok := p.pendingAcks[msgID]
	if ok {
		delete(p.pendingAcks, msgID)
	}
	p.pendingMu.Unlock()
	if ok && ch != nil {
		select {
		case ch <- data:
		default:
		}
	}
}

func init() {
	core.RegisterPlatform("yuanbao", New)
}
