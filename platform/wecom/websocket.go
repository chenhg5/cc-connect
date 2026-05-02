package wecom

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

const (
	wsEndpoint      = "wss://openws.work.weixin.qq.com"
	wsPingInterval  = 30 * time.Second
	wsMaxBackoff    = 30 * time.Second
	wsMaxMissedPong = 2
)

// WSPlatform implements core.Platform using the WeChat Work WebSocket long-connection
// mode (智能机器人长连接). No public URL, no message encryption, no IP allowlist required.
type WSPlatform struct {
	botID       string
	secret      string
	allowFrom   string
	conn        *websocket.Conn
	handler     core.MessageHandler
	ctx         context.Context
	cancel      context.CancelFunc
	mu          sync.Mutex // protects conn writes
	dedup       core.MessageDedup
	reqSeq      atomic.Int64 // monotonic counter for generating unique req_id
	missedPong  atomic.Int32 // consecutive heartbeat acks not received
	pendingAcks sync.Map     // req_id -> chan error, for sequential send with ack waiting
	// streamStates tracks the currently-open aibot_respond_msg stream for each
	// in-flight user message. Key: original aibot_msg_callback req_id (string).
	// Value: *wsStreamState. Every Reply/Send/UpdateMessage call accumulates
	// content into the same state and emits frames with the same stream.id, so
	// the WeChat Work client renders progressive typing inside one bubble.
	// The stream is closed (finish=true) when the engine's TypingIndicator
	// stop function fires at turn end (engine.go defers stopTyping() in
	// processInteractiveEvents).
	streamStates sync.Map
}

// wsStreamState owns the running aibot_respond_msg/stream for one user
// message. Content is full-replacement (per protocol), so we keep a builder
// and re-emit the entire accumulated text on every frame. The placeholder
// from StartTyping is intentionally NOT seeded into the builder — first real
// Send/Reply call replaces the "🤔 思考中..." text with real content because
// stream.content overwrites the bubble's display.
type wsStreamState struct {
	streamID string
	reqID    string
	chatID   string
	userID   string
	// sendMu serializes outbound frames for this stream. WeChat Work rejects
	// concurrent aibot_respond_msg calls for the same req_id with errcode=6000
	// "data version conflict". Acquire sendMu around the (mutate accumulator
	// → emit frame → wait ack) critical section so frame order matches the
	// order writes were applied to the accumulator.
	sendMu      sync.Mutex
	mu          sync.Mutex
	accumulated strings.Builder
	finalized   bool
}

const wsAckTimeout = 5 * time.Second

// typingRenderDelay is held under the per-stream sendMu after StartTyping's
// placeholder frame is acked, gating any follow-up frame so the WeChat Work
// client has time to render the "🤔 思考中..." bubble before it gets replaced.
const typingRenderDelay = 800 * time.Millisecond

// wsReplyContext holds the context needed to reply to a specific message.
type wsReplyContext struct {
	reqID    string // req_id from headers of aibot_msg_callback
	chatID   string // chatid for aibot_send_msg
	chatType string // chattype: "single" or "group"
	userID   string // from.userid
}

// wsPreviewHandle is returned from SendPreviewStart and identifies an in-progress
// streaming response. The same streamID is reused across UpdateMessage and the
// final FinalizeStream call so the WeChat Work client can render a typing-style
// progressive update inside one message bubble.
type wsPreviewHandle struct {
	streamID string // generated id used in stream.id for every frame
	reqID    string // original aibot_msg_callback req_id (echoed in headers)
	chatID   string
	userID   string
}

// --- WebSocket protocol frame types (matching official SDK) ---

// wsFrame is the unified frame structure used for all WebSocket communication.
// Format: { cmd, headers: { req_id }, body: {...} }
// Response frames may omit cmd and include errcode/errmsg instead.
type wsFrame struct {
	Cmd     string          `json:"cmd,omitempty"`
	Headers wsFrameHeaders  `json:"headers"`
	Body    json.RawMessage `json:"body,omitempty"`
	ErrCode *int            `json:"errcode,omitempty"`
	ErrMsg  string          `json:"errmsg,omitempty"`
}

type wsFrameHeaders struct {
	ReqID string `json:"req_id"`
}

// wsMsgCallbackBody is the body of an aibot_msg_callback frame.
type wsMsgCallbackBody struct {
	MsgID    string `json:"msgid"`
	AibotID  string `json:"aibotid"`
	ChatID   string `json:"chatid"`
	ChatType string `json:"chattype"` // "single" or "group"
	From     struct {
		UserID string `json:"userid"`
	} `json:"from"`
	MsgType string `json:"msgtype"`
	Text    struct {
		Content string `json:"content"`
	} `json:"text"`
	// Voice: official field is content; some payloads used text — accept both.
	Voice struct {
		Text    string `json:"text,omitempty"`
		Content string `json:"content,omitempty"`
	} `json:"voice"`
	Image *struct {
		URL    string `json:"url"`
		Aeskey string `json:"aeskey"`
	} `json:"image,omitempty"`
	File *struct {
		URL    string `json:"url"`
		Aeskey string `json:"aeskey"`
	} `json:"file,omitempty"`
	Mixed      *wsMixedBlock `json:"mixed,omitempty"`
	Quote      *wsQuoteBlock `json:"quote,omitempty"`
	CreateTime int64         `json:"create_time"`
}

func wsVoiceText(v struct {
	Text    string `json:"text,omitempty"`
	Content string `json:"content,omitempty"`
}) string {
	if s := strings.TrimSpace(v.Content); s != "" {
		return s
	}
	return strings.TrimSpace(v.Text)
}

func newWebSocket(opts map[string]any) (core.Platform, error) {
	botID, _ := opts["bot_id"].(string)
	secret, _ := opts["bot_secret"].(string)
	if botID == "" || secret == "" {
		return nil, fmt.Errorf("wecom-ws: bot_id and bot_secret are required for websocket mode")
	}
	allowFrom, _ := opts["allow_from"].(string)

	return &WSPlatform{
		botID:     botID,
		secret:    secret,
		allowFrom: allowFrom,
	}, nil
}

// generateReqID creates a globally-unique id with the given prefix.
// Format: "<prefix>_<unix_ms>_<seq>_<rand_hex>" — matches the WeCom official
// SDK pattern (timestamp + entropy). Process-unique seq alone is NOT enough:
// after a restart the sequence resets and stream.id collides with previously
// committed stream IDs on the server, causing the WeChat Work client to
// silently drop the frames as duplicate/stale data.
func (p *WSPlatform) generateReqID(prefix string) string {
	seq := p.reqSeq.Add(1)
	var buf [4]byte
	_, _ = rand.Read(buf[:])
	return fmt.Sprintf("%s_%d_%d_%s", prefix, time.Now().UnixMilli(), seq, hex.EncodeToString(buf[:]))
}

func (p *WSPlatform) Name() string { return "wecom" }

func (p *WSPlatform) Start(handler core.MessageHandler) error {
	p.handler = handler
	p.ctx, p.cancel = context.WithCancel(context.Background())

	go p.connectLoop()
	return nil
}

// connectLoop establishes the WebSocket connection and reconnects on failure with
// exponential backoff (1s → 2s → 4s → ... → 30s max).
func (p *WSPlatform) connectLoop() {
	backoff := time.Second
	for {
		select {
		case <-p.ctx.Done():
			return
		default:
		}

		start := time.Now()
		err := p.runConnection()
		if p.ctx.Err() != nil {
			return // shutting down
		}

		// If the connection was alive for a meaningful period, reset backoff
		if time.Since(start) > 2*wsPingInterval {
			backoff = time.Second
		}

		slog.Warn("wecom-ws: connection lost, reconnecting", "error", err, "backoff", backoff)
		select {
		case <-time.After(backoff):
		case <-p.ctx.Done():
			return
		}

		backoff *= 2
		if backoff > wsMaxBackoff {
			backoff = wsMaxBackoff
		}
	}
}

// runConnection dials, subscribes, and processes messages until disconnection.
func (p *WSPlatform) runConnection() error {
	slog.Info("wecom-ws: connecting", "endpoint", wsEndpoint)

	conn, _, err := websocket.DefaultDialer.DialContext(p.ctx, wsEndpoint, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}

	p.mu.Lock()
	p.conn = conn
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		p.conn = nil
		p.mu.Unlock()
		conn.Close()

		// Drain pending ACK channels so waiting goroutines are unblocked
		// and stale entries do not accumulate across reconnections.
		// Collect keys first, then delete — Range+Delete in callback is
		// not guaranteed safe by the sync.Map contract.
		var staleKeys []any
		p.pendingAcks.Range(func(key, value any) bool {
			if ch, ok := value.(chan *wsFrame); ok {
				select {
				case ch <- nil: // nil signals connection-closed to writeAndWaitAckFrame
				default:
				}
			}
			staleKeys = append(staleKeys, key)
			return true
		})
		for _, k := range staleKeys {
			p.pendingAcks.Delete(k)
		}
	}()

	// Send subscribe (auth) frame
	// Format: { cmd: "aibot_subscribe", headers: { req_id }, body: { bot_id, secret } }
	subReqID := p.generateReqID("aibot_subscribe")
	subFrame := map[string]any{
		"cmd":     "aibot_subscribe",
		"headers": map[string]string{"req_id": subReqID},
		"body": map[string]string{
			"bot_id": p.botID,
			"secret": p.secret,
		},
	}
	if err := p.writeJSON(subFrame); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	// Read subscribe response: { headers: { req_id }, errcode: 0, errmsg: "ok" }
	var subResp wsFrame
	if err := conn.ReadJSON(&subResp); err != nil {
		return fmt.Errorf("subscribe response: %w", err)
	}
	if subResp.ErrCode == nil || *subResp.ErrCode != 0 {
		errCode := 0
		if subResp.ErrCode != nil {
			errCode = *subResp.ErrCode
		}
		return fmt.Errorf("subscribe failed: errcode=%d errmsg=%s", errCode, subResp.ErrMsg)
	}
	slog.Info("wecom-ws: subscribed successfully", "bot_id", p.botID)
	p.missedPong.Store(0)

	// Start heartbeat goroutine
	heartCtx, heartCancel := context.WithCancel(p.ctx)
	defer heartCancel()
	go p.heartbeat(heartCtx, conn)

	// Read loop
	for {
		select {
		case <-p.ctx.Done():
			return p.ctx.Err()
		default:
		}

		_, raw, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		var frame wsFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			slog.Warn("wecom-ws: invalid json", "error", err)
			continue
		}

		p.handleFrame(frame)
	}
}

// handleFrame dispatches incoming frames by cmd or req_id prefix.
func (p *WSPlatform) handleFrame(frame wsFrame) {
	switch frame.Cmd {
	case "aibot_msg_callback":
		p.handleMsgCallback(frame)
	case "aibot_event_callback":
		slog.Debug("wecom-ws: event callback received (ignored)", "req_id", frame.Headers.ReqID)
	case "":
		// Response frame (no cmd): identify by req_id prefix
		reqID := frame.Headers.ReqID
		switch {
		case strings.HasPrefix(reqID, "ping"):
			p.missedPong.Store(0)
			slog.Debug("wecom-ws: heartbeat ack received")
		case strings.HasPrefix(reqID, "aibot_subscribe"):
			// Late subscribe ack (should have been consumed in runConnection)
			slog.Debug("wecom-ws: late subscribe ack")
		default:
			if frame.ErrCode != nil && *frame.ErrCode != 0 {
				slog.Warn("wecom-ws: reply/send ack error", "req_id", reqID, "errcode", *frame.ErrCode, "errmsg", frame.ErrMsg)
			} else {
				slog.Debug("wecom-ws: reply/send ack ok", "req_id", reqID)
			}
			if ch, ok := p.pendingAcks.LoadAndDelete(reqID); ok {
				// Capture frame in a local so the channel pointer doesn't
				// alias the loop's frame (frame is a function-scope value
				// here, but a fresh allocation is clearer for the reader).
				f := frame
				ch.(chan *wsFrame) <- &f
			}
		}
	default:
		slog.Debug("wecom-ws: unhandled cmd", "cmd", frame.Cmd)
	}
}

func (p *WSPlatform) heartbeat(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(wsPingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			missed := int(p.missedPong.Load())
			if missed >= wsMaxMissedPong {
				slog.Warn("wecom-ws: no heartbeat ack for consecutive pings, connection considered dead",
					"missed", missed)
				conn.Close()
				return
			}

			p.missedPong.Add(1)
			pingFrame := map[string]any{
				"cmd":     "ping",
				"headers": map[string]string{"req_id": p.generateReqID("ping")},
			}
			if err := p.writeJSON(pingFrame); err != nil {
				slog.Warn("wecom-ws: ping failed", "error", err)
				return
			}
			slog.Debug("wecom-ws: ping sent", "missed_pong", p.missedPong.Load())
		}
	}
}

func (p *WSPlatform) handleMsgCallback(frame wsFrame) {
	var body wsMsgCallbackBody
	if err := json.Unmarshal(frame.Body, &body); err != nil {
		slog.Warn("wecom-ws: parse msg_callback body failed", "error", err)
		return
	}

	reqID := frame.Headers.ReqID

	if p.dedup.IsDuplicate(body.MsgID) {
		slog.Debug("wecom-ws: skipping duplicate message", "msg_id", body.MsgID)
		return
	}

	if body.CreateTime > 0 {
		if core.IsOldMessage(time.Unix(body.CreateTime, 0)) {
			slog.Debug("wecom-ws: ignoring old message", "create_time", body.CreateTime)
			return
		}
	}

	if !core.AllowList(p.allowFrom, body.From.UserID) {
		slog.Debug("wecom-ws: message from unauthorized user", "user", body.From.UserID)
		return
	}

	chatID := body.ChatID
	if chatID == "" {
		chatID = body.From.UserID
	}

	sessionKey := fmt.Sprintf("wecom:%s:%s", chatID, body.From.UserID)
	rctx := wsReplyContext{
		reqID:    reqID,
		chatID:   chatID,
		chatType: body.ChatType,
		userID:   body.From.UserID,
	}

	// WS mode does not provide display names; the protocol only carries userID.
	// Name resolution would require a separate HTTP API call with corpSecret,
	// which is unavailable in WebSocket-only mode.
	chatName := ""
	if body.ChatType == "group" {
		chatName = body.ChatID
	}

	texts, imgRefs, fileRefs := wsCollectInboundParts(&body)

	switch body.MsgType {
	case "voice":
		vt := stripWeComAtMentions(wsVoiceText(body.Voice), p.botID, body.AibotID)
		if vt == "" && len(imgRefs) == 0 && len(fileRefs) == 0 {
			slog.Debug("wecom-ws: voice message with empty transcription, ignoring")
			return
		}
		if len(imgRefs) > 0 || len(fileRefs) > 0 {
			out := []string{}
			if vt != "" {
				out = append(out, vt)
			}
			out = append(out, texts...)
			slog.Info("wecom-ws: voice + media", "user", body.From.UserID, "images", len(imgRefs), "files", len(fileRefs))
			go p.deliverWSMediaInbound(&body, sessionKey, chatName, rctx, out, imgRefs, fileRefs)
			return
		}
		slog.Debug("wecom-ws: voice received (transcribed)", "user", body.From.UserID, "len", len(vt))
		go p.handler(p, &core.Message{
			SessionKey: sessionKey, Platform: "wecom",
			MessageID: body.MsgID,
			UserID:    body.From.UserID, UserName: body.From.UserID,
			ChatName: chatName,
			Content:  vt, ReplyCtx: rctx, FromVoice: true,
		})
		return
	}

	if len(imgRefs) == 0 && len(fileRefs) == 0 {
		if len(texts) == 0 {
			slog.Warn("wecom-ws: no text or media in message", "msg_type", body.MsgType, "msg_id", body.MsgID)
			return
		}
		content := stripWeComAtMentions(strings.Join(texts, "\n"), p.botID, body.AibotID)
		slog.Debug("wecom-ws: text received", "user", body.From.UserID, "len", len(content))
		go p.handler(p, &core.Message{
			SessionKey: sessionKey, Platform: "wecom",
			MessageID: body.MsgID,
			UserID:    body.From.UserID, UserName: body.From.UserID,
			ChatName: chatName,
			Content:  content, ReplyCtx: rctx,
		})
		return
	}

	slog.Info("wecom-ws: media message", "msg_type", body.MsgType, "user", body.From.UserID,
		"images", len(imgRefs), "files", len(fileRefs), "text_parts", len(texts))
	go p.deliverWSMediaInbound(&body, sessionKey, chatName, rctx, texts, imgRefs, fileRefs)
}

// sendStreamFrame writes a single aibot_respond_msg/stream frame using the
// streamID and reqID from the handle and BLOCKS until the server ack is
// received. WeChat Work serializes per-req_id processing on the server side
// and rejects parallel frames with errcode=6000 ("data version conflict"),
// so callers must already hold sendMu (per-stream) before invoking this.
// content is a full replacement (not incremental append).
func (p *WSPlatform) sendStreamFrame(h wsPreviewHandle, content string, finish bool) error {
	frame := map[string]any{
		"cmd":     "aibot_respond_msg",
		"headers": map[string]string{"req_id": h.reqID},
		"body": map[string]any{
			"msgtype": "stream",
			"stream": map[string]any{
				"id":      h.streamID,
				"finish":  finish,
				"content": content,
			},
		},
	}
	if err := p.writeAndWaitAck(p.ctx, frame, h.reqID); err != nil {
		slog.Error("wecom-ws: stream frame failed",
			"stream_id", h.streamID, "finish", finish, "user", h.userID, "error", err)
		return err
	}
	slog.Debug("wecom-ws: stream frame sent",
		"stream_id", h.streamID, "finish", finish, "user", h.userID, "content_len", len(content))
	return nil
}

// streamStateFor returns the wsStreamState for a given user message reqID,
// creating one with a fresh streamID on first access. All subsequent
// Reply/Send/UpdateMessage calls for the same user turn share this state so
// every frame goes out under the same stream.id (the WeChat Work client
// renders progressive typing in the same message bubble).
func (p *WSPlatform) streamStateFor(rc wsReplyContext) *wsStreamState {
	if v, ok := p.streamStates.Load(rc.reqID); ok {
		return v.(*wsStreamState)
	}
	fresh := &wsStreamState{
		streamID: p.generateReqID("stream"),
		reqID:    rc.reqID,
		chatID:   rc.chatID,
		userID:   rc.userID,
	}
	if existing, loaded := p.streamStates.LoadOrStore(rc.reqID, fresh); loaded {
		return existing.(*wsStreamState)
	}
	return fresh
}

// streamHandleFromState builds a wsPreviewHandle from state for sendStreamFrame.
func streamHandleFromState(st *wsStreamState) wsPreviewHandle {
	return wsPreviewHandle{streamID: st.streamID, reqID: st.reqID, chatID: st.chatID, userID: st.userID}
}

// emitIndependentBubble sends `content` as one self-contained bubble (a
// single aibot_respond_msg/stream frame with finish=true).
//
// If a typing-placeholder stream is currently registered for rc.reqID it
// is taken over: the same stream.id is reused so the placeholder text is
// replaced by `content` in the existing bubble, and finish=true closes the
// stream out. After that the entry is gone, so the next call allocates a
// fresh stream.id and lands in a brand-new bubble — giving each Reply/Send
// invocation its own message in the WeChat Work conversation.
func (p *WSPlatform) emitIndependentBubble(rc wsReplyContext, content string) error {
	var (
		streamID string
		st       *wsStreamState
	)
	if v, ok := p.streamStates.LoadAndDelete(rc.reqID); ok {
		st = v.(*wsStreamState)
		st.sendMu.Lock()
		defer st.sendMu.Unlock()
		st.mu.Lock()
		if !st.finalized {
			st.finalized = true
			streamID = st.streamID
		}
		st.mu.Unlock()
	}
	if streamID == "" {
		streamID = p.generateReqID("stream")
	}
	h := wsPreviewHandle{
		streamID: streamID,
		reqID:    rc.reqID,
		chatID:   rc.chatID,
		userID:   rc.userID,
	}
	return p.sendStreamFrame(h, content, true)
}

// emitReplaceAccumulated overwrites the accumulator (used by stream-preview
// callbacks where the engine already maintains its own running buffer).
func (p *WSPlatform) emitReplaceAccumulated(rc wsReplyContext, content string, finish bool) error {
	st := p.streamStateFor(rc)
	st.sendMu.Lock()
	defer st.sendMu.Unlock()

	st.mu.Lock()
	if st.finalized {
		st.mu.Unlock()
		return nil
	}
	st.accumulated.Reset()
	st.accumulated.WriteString(content)
	if finish {
		st.finalized = true
	}
	h := streamHandleFromState(st)
	st.mu.Unlock()

	if finish {
		p.streamStates.Delete(rc.reqID)
	}
	return p.sendStreamFrame(h, content, finish)
}

// Reply emits content as a self-contained bubble. The first Reply/Send for
// a given user message takes over the typing-placeholder stream so the
// "🤔 思考中..." bubble morphs into the first real message; subsequent
// Reply/Send calls land in fresh bubbles, so thinking blocks and the final
// answer end up as separate messages in the WeChat Work conversation.
func (p *WSPlatform) Reply(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(wsReplyContext)
	if !ok {
		return fmt.Errorf("wecom-ws: invalid reply context type %T", rctx)
	}
	if content == "" || rc.reqID == "" {
		return nil
	}
	return p.emitIndependentBubble(rc, content)
}

// SendPreviewStart implements core.PreviewStarter. It writes the first chunk
// into the running stream (or creates one) and returns a handle for later
// UpdateMessage / FinalizeStream calls. Note: in the typical wecom turn the
// engine never reaches this path because thinking_messages=true freezes
// stream preview before it starts; Reply/Send carry the actual content.
func (p *WSPlatform) SendPreviewStart(ctx context.Context, rctx any, content string) (any, error) {
	rc, ok := rctx.(wsReplyContext)
	if !ok {
		return nil, fmt.Errorf("wecom-ws: invalid reply context type %T", rctx)
	}
	if rc.reqID == "" {
		return nil, fmt.Errorf("wecom-ws: stream preview requires reqID (cron/relay paths use Send instead)")
	}
	if err := p.emitReplaceAccumulated(rc, content, false); err != nil {
		return nil, err
	}
	return rc, nil
}

// StartTyping implements core.TypingIndicator. WeChat Work智能机器人 has no
// native typing frame; we emulate it by opening a stream with "🤔 思考中..."
// as the placeholder content (finish=false). The first follow-up Reply/Send
// takes over this stream.id and emits finish=true with the real content,
// which morphs the placeholder bubble into the first real message. Any
// further Reply/Send for the same turn allocates a fresh stream.id, so each
// landing message becomes its own bubble.
//
// The returned stop func runs from engine's defer at turn end. If the
// placeholder was never taken over (rare: the engine produced no output for
// this turn), it closes the stream out so the bubble doesn't linger as a
// perpetual "🤔 思考中..." stub.
func (p *WSPlatform) StartTyping(ctx context.Context, rctx any) (stop func()) {
	rc, ok := rctx.(wsReplyContext)
	if !ok || rc.reqID == "" {
		return func() {}
	}
	st := p.streamStateFor(rc)
	h := streamHandleFromState(st)
	// Hold sendMu across the placeholder frame AND a short render delay so
	// that any subsequent Reply/Send racing in (e.g. fast Claude Code thinking
	// block) is gated behind the typing bubble actually appearing on the
	// client. Without this delay the WeChat Work desktop client sometimes
	// coalesces back-to-back stream frames and only renders the latest state,
	// making the "🤔 思考中..." flash too briefly to be seen.
	st.sendMu.Lock()
	err := p.sendStreamFrame(h, "🤔 思考中...", false)
	if err == nil {
		time.Sleep(typingRenderDelay)
	}
	st.sendMu.Unlock()
	if err != nil {
		// Best-effort: drop the state we just created so it doesn't leak.
		p.streamStates.CompareAndDelete(rc.reqID, st)
		return func() {}
	}
	return func() {
		v, ok := p.streamStates.LoadAndDelete(rc.reqID)
		if !ok {
			return // already finalized via FinalizeStream / Reply path
		}
		state := v.(*wsStreamState)
		// Hold sendMu across the close so any in-flight emit completes
		// before we send finish=true (avoids data version conflict).
		state.sendMu.Lock()
		defer state.sendMu.Unlock()
		state.mu.Lock()
		if state.finalized {
			state.mu.Unlock()
			return
		}
		state.finalized = true
		text := state.accumulated.String()
		streamID := state.streamID
		state.mu.Unlock()
		if text == "" {
			// No real content arrived (turn ended before any Reply/Send) —
			// keep the placeholder so the bubble has SOMETHING when the
			// stream closes.
			text = "🤔 思考中..."
		}
		closing := wsPreviewHandle{streamID: streamID, reqID: rc.reqID, chatID: rc.chatID, userID: rc.userID}
		_ = p.sendStreamFrame(closing, text, true)
	}
}

// UpdateMessage implements core.MessageUpdater. previewHandle is the
// wsReplyContext returned by SendPreviewStart. content is the full accumulated
// response so far (per stream-preview semantics).
func (p *WSPlatform) UpdateMessage(ctx context.Context, previewHandle any, content string) error {
	rc, ok := previewHandle.(wsReplyContext)
	if !ok {
		return fmt.Errorf("wecom-ws: invalid preview handle type %T", previewHandle)
	}
	return p.emitReplaceAccumulated(rc, content, false)
}

// FinalizeStream implements core.StreamFinalizer.
func (p *WSPlatform) FinalizeStream(ctx context.Context, previewHandle any, finalContent string) error {
	rc, ok := previewHandle.(wsReplyContext)
	if !ok {
		return fmt.Errorf("wecom-ws: invalid preview handle type %T", previewHandle)
	}
	return p.emitReplaceAccumulated(rc, finalContent, true)
}

// KeepPreviewOnFinish implements core.PreviewFinishPreference. The stream
// preview message IS the final delivered message, so we want streamPreview.finish()
// to call FinalizeStream rather than delete the preview.
func (p *WSPlatform) KeepPreviewOnFinish() bool {
	return true
}

// Send routes outgoing content based on whether we have a reqID:
//   - reqID present (responding to a user message): emit one self-contained
//     stream bubble per call. First call takes over the typing placeholder;
//     follow-up calls (e.g. thinking-block then final answer) each land in
//     their own bubble.
//   - reqID absent (cron / proactive push, or reply context reconstructed
//     for a one-off message): use aibot_send_msg/markdown — no stream.
func (p *WSPlatform) Send(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(wsReplyContext)
	if !ok {
		return fmt.Errorf("wecom-ws: invalid reply context type %T", rctx)
	}
	if content == "" {
		return nil
	}
	if rc.reqID != "" {
		return p.emitIndependentBubble(rc, content)
	}
	if rc.chatID == "" {
		return fmt.Errorf("wecom-ws: chatID is empty, cannot send proactive message")
	}

	chunks := splitByBytes(content, 2000)
	for i, chunk := range chunks {
		reqID := p.generateReqID("aibot_send_msg")
		frame := map[string]any{
			"cmd":     "aibot_send_msg",
			"headers": map[string]string{"req_id": reqID},
			"body": map[string]any{
				"chatid":  rc.chatID,
				"msgtype": "markdown",
				"markdown": map[string]string{
					"content": chunk,
				},
			},
		}
		if err := p.writeAndWaitAck(ctx, frame, reqID); err != nil {
			slog.Error("wecom-ws: send failed", "user", rc.userID, "chunk", i, "error", err)
			return err
		}
	}
	slog.Debug("wecom-ws: message sent", "user", rc.userID, "chunks", len(chunks), "total_len", len(content))
	return nil
}

// ReconstructReplyCtx rebuilds a reply context from a session key.
// Session key format: "wecom:{chatID}:{userID}".
// The reconstructed context has no req_id, so Reply() (which needs req_id for
// aibot_respond_msg) won't work — the engine should use Send() (aibot_send_msg)
// for cron/relay scenarios.
func (p *WSPlatform) ReconstructReplyCtx(sessionKey string) (any, error) {
	// wecom:{chatID}:{userID}
	parts := strings.SplitN(sessionKey, ":", 3)
	if len(parts) < 3 || parts[0] != "wecom" {
		return nil, fmt.Errorf("wecom-ws: invalid session key %q", sessionKey)
	}
	return wsReplyContext{chatID: parts[1], userID: parts[2]}, nil
}

func (p *WSPlatform) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	p.mu.Lock()
	conn := p.conn
	p.mu.Unlock()
	if conn != nil {
		return conn.Close()
	}
	return nil
}

// writeJSON sends a JSON message over the WebSocket connection with mutex protection.
func (p *WSPlatform) writeJSON(v any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn == nil {
		return fmt.Errorf("wecom-ws: not connected")
	}
	return p.conn.WriteJSON(v)
}

// writeAndWaitAck sends a frame and waits for the server ack before returning.
// Discards the ack body. For callers that need to read the ack body (e.g.
// aibot_upload_media_init returning upload_id) use writeAndWaitAckFrame.
// Falls back to non-blocking on timeout to avoid deadlocks.
func (p *WSPlatform) writeAndWaitAck(ctx context.Context, frame map[string]any, reqID string) error {
	_, err := p.writeAndWaitAckFrame(ctx, frame, reqID)
	return err
}

// writeAndWaitAckFrame is the body-aware variant: it returns the full server
// response frame so callers can json.Unmarshal frame.Body. The error is
// non-nil iff frame.errcode is non-zero (or transport/timeout failed); on
// timeout it returns (nil, nil) for backward compat with writeAndWaitAck.
func (p *WSPlatform) writeAndWaitAckFrame(ctx context.Context, frame map[string]any, reqID string) (*wsFrame, error) {
	ch := make(chan *wsFrame, 1)
	p.pendingAcks.Store(reqID, ch)

	if err := p.writeJSON(frame); err != nil {
		p.pendingAcks.Delete(reqID)
		return nil, err
	}

	select {
	case f := <-ch:
		if f == nil {
			return nil, fmt.Errorf("wecom-ws: connection closed")
		}
		if f.ErrCode != nil && *f.ErrCode != 0 {
			return f, fmt.Errorf("wecom-ws: ack error: errcode=%d errmsg=%s", *f.ErrCode, f.ErrMsg)
		}
		return f, nil
	case <-ctx.Done():
		p.pendingAcks.Delete(reqID)
		return nil, ctx.Err()
	case <-time.After(wsAckTimeout):
		p.pendingAcks.Delete(reqID)
		slog.Debug("wecom-ws: ack timeout, proceeding", "req_id", reqID)
		return nil, nil
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Media upload (aibot_upload_media_init / chunk / finish) and outbound
// image / file delivery for WeChat Work智能机器人.
//
// Protocol summary (verified against the official Node SDK & developer docs):
//
//   1) aibot_upload_media_init  body{type, filename, total_size, total_chunks, md5}
//      → ack body{upload_id}
//   2) aibot_upload_media_chunk body{upload_id, chunk_index (1-based), base64_data}
//      → ack body{}
//   3) aibot_upload_media_finish body{upload_id}
//      → ack body{media_id}
//   4) aibot_send_msg body{chatid, chat_type?, msgtype: image|file, image|file:{media_id}}
//      → ack body{}
//
// Limits per the official docs:
//   - chunk size: ≤ 512 KB before base64 encoding
//   - max chunks: 100  (so total payload ≤ ~50 MB raw, well above the 10 MB image cap)
//   - image / video: 10 MB; voice: 2 MB; generic file: 20 MB
// ─────────────────────────────────────────────────────────────────────────────

const (
	// wsUploadChunkSize is the raw byte size per upload chunk. Server requires
	// ≤ 512 KB BEFORE base64 encoding, so this is the chunk slice size we feed
	// into base64.StdEncoding.EncodeToString.
	wsUploadChunkSize = 256 * 1024 // 256 KB — well under the 512 KB cap

	// wsMediaUploadTimeout caps the entire 3-step upload sequence.
	wsMediaUploadTimeout = 60 * time.Second
)

// uploadMedia runs the 3-step aibot_upload_media handshake and returns the
// media_id allocated by WeChat Work. mediaType is one of "image" / "voice" /
// "video" / "file" per the protocol.
func (p *WSPlatform) uploadMedia(ctx context.Context, mediaType, filename string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("wecom-ws: uploadMedia: empty payload")
	}
	if filename == "" {
		filename = fmt.Sprintf("file_%d.bin", time.Now().UnixMilli())
	}
	chunks := chunkBytes(data, wsUploadChunkSize)
	if len(chunks) > 100 {
		return "", fmt.Errorf("wecom-ws: uploadMedia: %d chunks exceeds 100-chunk limit (file too large for ws upload)", len(chunks))
	}

	uploadCtx, cancel := context.WithTimeout(ctx, wsMediaUploadTimeout)
	defer cancel()

	sum := md5.Sum(data)
	md5Hex := hex.EncodeToString(sum[:])

	// ── 1. init ─────────────────────────────────────────────────────────────
	initReqID := p.generateReqID("aibot_upload_media_init")
	initFrame := map[string]any{
		"cmd":     "aibot_upload_media_init",
		"headers": map[string]string{"req_id": initReqID},
		"body": map[string]any{
			"type":         mediaType,
			"filename":     filename,
			"total_size":   len(data),
			"total_chunks": len(chunks),
			"md5":          md5Hex,
		},
	}
	initAck, err := p.writeAndWaitAckFrame(uploadCtx, initFrame, initReqID)
	if err != nil {
		return "", fmt.Errorf("upload init: %w", err)
	}
	if initAck == nil {
		return "", fmt.Errorf("upload init: ack timeout")
	}
	var initBody struct {
		UploadID string `json:"upload_id"`
	}
	if err := json.Unmarshal(initAck.Body, &initBody); err != nil {
		return "", fmt.Errorf("upload init: parse body: %w", err)
	}
	if initBody.UploadID == "" {
		return "", fmt.Errorf("upload init: empty upload_id (body=%s)", string(initAck.Body))
	}

	// ── 2. chunks ───────────────────────────────────────────────────────────
	for i, raw := range chunks {
		chunkReqID := p.generateReqID("aibot_upload_media_chunk")
		chunkFrame := map[string]any{
			"cmd":     "aibot_upload_media_chunk",
			"headers": map[string]string{"req_id": chunkReqID},
			"body": map[string]any{
				"upload_id":   initBody.UploadID,
				"chunk_index": i, // 0-based per protocol
				"base64_data": base64.StdEncoding.EncodeToString(raw),
			},
		}
		if _, err := p.writeAndWaitAckFrame(uploadCtx, chunkFrame, chunkReqID); err != nil {
			return "", fmt.Errorf("upload chunk %d/%d: %w", i+1, len(chunks), err)
		}
	}

	// ── 3. finish ───────────────────────────────────────────────────────────
	finishReqID := p.generateReqID("aibot_upload_media_finish")
	finishFrame := map[string]any{
		"cmd":     "aibot_upload_media_finish",
		"headers": map[string]string{"req_id": finishReqID},
		"body":    map[string]any{"upload_id": initBody.UploadID},
	}
	finishAck, err := p.writeAndWaitAckFrame(uploadCtx, finishFrame, finishReqID)
	if err != nil {
		return "", fmt.Errorf("upload finish: %w", err)
	}
	if finishAck == nil {
		return "", fmt.Errorf("upload finish: ack timeout")
	}
	var finishBody struct {
		MediaID string `json:"media_id"`
	}
	if err := json.Unmarshal(finishAck.Body, &finishBody); err != nil {
		return "", fmt.Errorf("upload finish: parse body: %w", err)
	}
	if finishBody.MediaID == "" {
		return "", fmt.Errorf("upload finish: empty media_id (body=%s)", string(finishAck.Body))
	}
	slog.Info("wecom-ws: media uploaded", "type", mediaType, "filename", filename, "size", len(data), "chunks", len(chunks), "media_id", finishBody.MediaID)
	return finishBody.MediaID, nil
}

// sendMediaMessage emits an aibot_send_msg with msgtype=image|file pointing at
// an already-uploaded media_id.
func (p *WSPlatform) sendMediaMessage(ctx context.Context, rc wsReplyContext, msgtype, mediaID string) error {
	if rc.chatID == "" {
		return fmt.Errorf("wecom-ws: sendMediaMessage: chatID is empty")
	}
	reqID := p.generateReqID("aibot_send_msg")
	frame := map[string]any{
		"cmd":     "aibot_send_msg",
		"headers": map[string]string{"req_id": reqID},
		"body": map[string]any{
			"chatid":  rc.chatID,
			"msgtype": msgtype,
			msgtype:   map[string]string{"media_id": mediaID},
		},
	}
	if err := p.writeAndWaitAck(ctx, frame, reqID); err != nil {
		return err
	}
	slog.Debug("wecom-ws: media message sent", "user", rc.userID, "msgtype", msgtype, "media_id", mediaID)
	return nil
}

// SendImage implements core.ImageSender. Uploads img.Data via the 3-step
// handshake and emits an aibot_send_msg/image referencing the resulting
// media_id. Works in both single and group chats; a reqID-bearing reply
// context is NOT required (we use chatID for proactive delivery).
func (p *WSPlatform) SendImage(ctx context.Context, rctx any, img core.ImageAttachment) error {
	rc, ok := rctx.(wsReplyContext)
	if !ok {
		return fmt.Errorf("wecom-ws: SendImage: invalid reply context type %T", rctx)
	}
	filename := img.FileName
	if filename == "" {
		ext := imageExtFromMime(img.MimeType)
		filename = fmt.Sprintf("image_%d%s", time.Now().UnixMilli(), ext)
	}
	mediaID, err := p.uploadMedia(ctx, "image", filename, img.Data)
	if err != nil {
		return fmt.Errorf("wecom-ws: SendImage upload: %w", err)
	}
	return p.sendMediaMessage(ctx, rc, "image", mediaID)
}

// SendFile implements core.FileSender. Uploads file.Data and emits an
// aibot_send_msg/file referencing the resulting media_id.
func (p *WSPlatform) SendFile(ctx context.Context, rctx any, file core.FileAttachment) error {
	rc, ok := rctx.(wsReplyContext)
	if !ok {
		return fmt.Errorf("wecom-ws: SendFile: invalid reply context type %T", rctx)
	}
	filename := file.FileName
	if filename == "" {
		filename = fmt.Sprintf("file_%d", time.Now().UnixMilli())
	}
	mediaID, err := p.uploadMedia(ctx, "file", filename, file.Data)
	if err != nil {
		return fmt.Errorf("wecom-ws: SendFile upload: %w", err)
	}
	return p.sendMediaMessage(ctx, rc, "file", mediaID)
}

// chunkBytes splits a byte slice into fixed-size chunks (last chunk may be
// shorter). Unlike splitByBytes (which is UTF-8 aware) this is byte-exact and
// safe for binary payloads.
func chunkBytes(data []byte, size int) [][]byte {
	if size <= 0 {
		return [][]byte{data}
	}
	n := (len(data) + size - 1) / size
	chunks := make([][]byte, 0, n)
	for i := 0; i < len(data); i += size {
		end := i + size
		if end > len(data) {
			end = len(data)
		}
		chunks = append(chunks, data[i:end])
	}
	return chunks
}

// imageExtFromMime returns a sensible filename extension for an image MIME
// type. Defaults to ".jpg" when the MIME is unknown — WeChat Work treats
// unknown extensions as files rather than displayable images.
func imageExtFromMime(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/bmp":
		return ".bmp"
	case "image/jpeg", "image/jpg", "":
		return ".jpg"
	default:
		return ".jpg"
	}
}
