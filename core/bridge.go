package core

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
// BridgeServer — global WebSocket server shared across all engines
// ---------------------------------------------------------------------------

// BridgeServer exposes a WebSocket endpoint for external platform adapters.
// A single instance is created globally; each project engine receives a
// lightweight BridgePlatform handle that delegates to this server.
type BridgeServer struct {
	port   int
	token  string
	path   string
	server *http.Server

	mu       sync.RWMutex
	adapters map[string]*bridgeAdapter // platform name → adapter

	enginesMu sync.RWMutex
	engines   map[string]*bridgeEngineRef // project name → engine ref
}

type bridgeEngineRef struct {
	engine   *Engine
	platform *BridgePlatform
}

type bridgeAdapter struct {
	platform     string
	capabilities map[string]bool
	conn         *websocket.Conn
	writeMu      sync.Mutex
	server       *BridgeServer

	previewMu       sync.Mutex
	previewRequests map[string]chan string // ref_id → channel receiving preview_handle
}

// bridgeReplyCtx carries the information needed to route replies back to the adapter.
type bridgeReplyCtx struct {
	Platform   string `json:"platform"`
	SessionKey string `json:"session_key"`
	ReplyCtx   string `json:"reply_ctx"`
}

// --- Wire protocol messages ---

type bridgeMsg struct {
	Type string `json:"type"`
}

type bridgeRegister struct {
	Type         string         `json:"type"`
	Platform     string         `json:"platform"`
	Capabilities []string       `json:"capabilities"`
	Project      string         `json:"project,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

type bridgeMessage struct {
	Type       string            `json:"type"`
	MsgID      string            `json:"msg_id"`
	SessionKey string            `json:"session_key"`
	UserID     string            `json:"user_id"`
	UserName   string            `json:"user_name,omitempty"`
	Content    string            `json:"content"`
	ReplyCtx   string            `json:"reply_ctx"`
	Images     []bridgeImageData `json:"images,omitempty"`
	Files      []bridgeFileData  `json:"files,omitempty"`
	Audio      *bridgeAudioData  `json:"audio,omitempty"`
}

type bridgeCardAction struct {
	Type       string `json:"type"`
	SessionKey string `json:"session_key"`
	Action     string `json:"action"`
	ReplyCtx   string `json:"reply_ctx"`
}

type bridgePreviewAck struct {
	Type          string `json:"type"`
	RefID         string `json:"ref_id"`
	PreviewHandle string `json:"preview_handle"`
}

type bridgeImageData struct {
	MimeType string `json:"mime_type"`
	Data     string `json:"data"` // base64
	FileName string `json:"file_name,omitempty"`
}

type bridgeFileData struct {
	MimeType string `json:"mime_type"`
	Data     string `json:"data"` // base64
	FileName string `json:"file_name"`
}

type bridgeAudioData struct {
	MimeType string `json:"mime_type"`
	Data     string `json:"data"` // base64
	Format   string `json:"format"`
	Duration int    `json:"duration,omitempty"`
}

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func NewBridgeServer(port int, token, path string) *BridgeServer {
	if port <= 0 {
		port = 9810
	}
	if path == "" {
		path = "/bridge/ws"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return &BridgeServer{
		port:     port,
		token:    token,
		path:     path,
		adapters: make(map[string]*bridgeAdapter),
		engines:  make(map[string]*bridgeEngineRef),
	}
}

// NewPlatform creates a BridgePlatform for a specific project engine.
func (bs *BridgeServer) NewPlatform(projectName string) *BridgePlatform {
	return &BridgePlatform{server: bs, project: projectName}
}

// RegisterEngine associates a project engine with its BridgePlatform.
func (bs *BridgeServer) RegisterEngine(projectName string, engine *Engine, bp *BridgePlatform) {
	bs.enginesMu.Lock()
	defer bs.enginesMu.Unlock()
	bs.engines[projectName] = &bridgeEngineRef{engine: engine, platform: bp}
}

// Start launches the HTTP/WebSocket server.
func (bs *BridgeServer) Start() {
	mux := http.NewServeMux()
	mux.HandleFunc(bs.path, bs.handleWS)

	// Session management REST endpoints
	mux.HandleFunc("/bridge/sessions", bs.authHTTP(bs.handleSessions))
	mux.HandleFunc("/bridge/sessions/", bs.authHTTP(bs.handleSessionRoutes))

	addr := fmt.Sprintf(":%d", bs.port)
	bs.server = &http.Server{Addr: addr, Handler: mux}

	go func() {
		slog.Info("bridge: server started", "addr", addr, "path", bs.path)
		if err := bs.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("bridge: server error", "error", err)
		}
	}()
}

// Stop shuts down the server and closes all adapter connections.
func (bs *BridgeServer) Stop() {
	bs.mu.Lock()
	for _, a := range bs.adapters {
		a.conn.Close()
	}
	bs.adapters = make(map[string]*bridgeAdapter)
	bs.mu.Unlock()

	if bs.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		bs.server.Shutdown(ctx)
	}
}

// ConnectedAdapters returns the names of currently connected adapters.
func (bs *BridgeServer) ConnectedAdapters() []string {
	bs.mu.RLock()
	defer bs.mu.RUnlock()
	names := make([]string, 0, len(bs.adapters))
	for name := range bs.adapters {
		names = append(names, name)
	}
	return names
}

// ---------------------------------------------------------------------------
// BridgePlatform — per-engine Platform that delegates to BridgeServer
// ---------------------------------------------------------------------------

// BridgePlatform implements core.Platform for a single project.
// It is a lightweight handle; the actual WebSocket server lives in BridgeServer.
type BridgePlatform struct {
	server     *BridgeServer
	project    string
	handler    MessageHandler
	navHandler CardNavigationHandler
}

// Compile-time interface checks.
var (
	_ Platform                  = (*BridgePlatform)(nil)
	_ CardSender                = (*BridgePlatform)(nil)
	_ InlineButtonSender        = (*BridgePlatform)(nil)
	_ MessageUpdater            = (*BridgePlatform)(nil)
	_ PreviewStarter            = (*BridgePlatform)(nil)
	_ PreviewCleaner            = (*BridgePlatform)(nil)
	_ TypingIndicator           = (*BridgePlatform)(nil)
	_ AudioSender               = (*BridgePlatform)(nil)
	_ CardNavigable             = (*BridgePlatform)(nil)
	_ ReplyContextReconstructor = (*BridgePlatform)(nil)
)

func (bp *BridgePlatform) Name() string { return "bridge" }

func (bp *BridgePlatform) Start(handler MessageHandler) error {
	bp.handler = handler
	return nil
}

func (bp *BridgePlatform) Stop() error { return nil }

func (bp *BridgePlatform) Reply(ctx context.Context, replyCtx any, content string) error {
	rc, ok := replyCtx.(*bridgeReplyCtx)
	if !ok {
		return fmt.Errorf("bridge: invalid reply context type %T", replyCtx)
	}
	return bp.server.sendToAdapter(rc.Platform, map[string]any{
		"type":        "reply",
		"session_key": rc.SessionKey,
		"reply_ctx":   rc.ReplyCtx,
		"content":     content,
		"format":      "text",
	})
}

func (bp *BridgePlatform) Send(ctx context.Context, replyCtx any, content string) error {
	return bp.Reply(ctx, replyCtx, content)
}

func (bp *BridgePlatform) ReconstructReplyCtx(sessionKey string) (any, error) {
	platform := bp.server.platformFromSessionKey(sessionKey)
	if platform == "" {
		return nil, fmt.Errorf("bridge: cannot determine adapter from session key %q", sessionKey)
	}
	a := bp.server.getAdapter(platform)
	if a == nil {
		return nil, fmt.Errorf("bridge: adapter %q not connected", platform)
	}
	if !a.capabilities["reconstruct_reply"] {
		return nil, fmt.Errorf("bridge: adapter %q does not support reconstruct_reply", platform)
	}
	return &bridgeReplyCtx{
		Platform:   platform,
		SessionKey: sessionKey,
		ReplyCtx:   sessionKey,
	}, nil
}

func (bp *BridgePlatform) SendCard(ctx context.Context, replyCtx any, card *Card) error {
	rc, ok := replyCtx.(*bridgeReplyCtx)
	if !ok {
		return fmt.Errorf("bridge: invalid reply context")
	}
	a := bp.server.getAdapter(rc.Platform)
	if a == nil || !a.capabilities["card"] {
		return bp.Reply(ctx, replyCtx, card.RenderText())
	}
	return bp.server.sendToAdapter(rc.Platform, map[string]any{
		"type":        "card",
		"session_key": rc.SessionKey,
		"reply_ctx":   rc.ReplyCtx,
		"card":        serializeCard(card),
	})
}

func (bp *BridgePlatform) ReplyCard(ctx context.Context, replyCtx any, card *Card) error {
	return bp.SendCard(ctx, replyCtx, card)
}

func (bp *BridgePlatform) SendWithButtons(ctx context.Context, replyCtx any, content string, buttons [][]ButtonOption) error {
	rc, ok := replyCtx.(*bridgeReplyCtx)
	if !ok {
		return fmt.Errorf("bridge: invalid reply context")
	}
	a := bp.server.getAdapter(rc.Platform)
	if a == nil || !a.capabilities["buttons"] {
		return bp.Reply(ctx, replyCtx, content)
	}
	return bp.server.sendToAdapter(rc.Platform, map[string]any{
		"type":        "buttons",
		"session_key": rc.SessionKey,
		"reply_ctx":   rc.ReplyCtx,
		"content":     content,
		"buttons":     buttons,
	})
}

func (bp *BridgePlatform) UpdateMessage(ctx context.Context, replyCtx any, content string) error {
	rc, ok := replyCtx.(*bridgeReplyCtx)
	if !ok {
		return fmt.Errorf("bridge: invalid reply context")
	}
	a := bp.server.getAdapter(rc.Platform)
	if a == nil || !a.capabilities["update_message"] {
		return ErrNotSupported
	}
	return bp.server.sendToAdapter(rc.Platform, map[string]any{
		"type":           "update_message",
		"session_key":    rc.SessionKey,
		"preview_handle": rc.ReplyCtx,
		"content":        content,
	})
}

func (bp *BridgePlatform) SendPreviewStart(ctx context.Context, replyCtx any, content string) (previewHandle any, err error) {
	rc, ok := replyCtx.(*bridgeReplyCtx)
	if !ok {
		return nil, fmt.Errorf("bridge: invalid reply context")
	}
	a := bp.server.getAdapter(rc.Platform)
	if a == nil || !a.capabilities["preview"] {
		return nil, ErrNotSupported
	}

	refID := fmt.Sprintf("prev-%d", time.Now().UnixNano())
	ch := make(chan string, 1)

	a.previewMu.Lock()
	a.previewRequests[refID] = ch
	a.previewMu.Unlock()

	if err := bp.server.sendToAdapter(rc.Platform, map[string]any{
		"type":        "preview_start",
		"ref_id":      refID,
		"session_key": rc.SessionKey,
		"reply_ctx":   rc.ReplyCtx,
		"content":     content,
	}); err != nil {
		a.previewMu.Lock()
		delete(a.previewRequests, refID)
		a.previewMu.Unlock()
		return nil, err
	}

	select {
	case handle := <-ch:
		return &bridgeReplyCtx{
			Platform:   rc.Platform,
			SessionKey: rc.SessionKey,
			ReplyCtx:   handle,
		}, nil
	case <-time.After(10 * time.Second):
		a.previewMu.Lock()
		delete(a.previewRequests, refID)
		a.previewMu.Unlock()
		return nil, fmt.Errorf("bridge: preview_ack timeout")
	case <-ctx.Done():
		a.previewMu.Lock()
		delete(a.previewRequests, refID)
		a.previewMu.Unlock()
		return nil, ctx.Err()
	}
}

func (bp *BridgePlatform) DeletePreviewMessage(ctx context.Context, previewHandle any) error {
	rc, ok := previewHandle.(*bridgeReplyCtx)
	if !ok {
		return fmt.Errorf("bridge: invalid preview handle")
	}
	a := bp.server.getAdapter(rc.Platform)
	if a == nil || !a.capabilities["delete_message"] {
		return ErrNotSupported
	}
	return bp.server.sendToAdapter(rc.Platform, map[string]any{
		"type":           "delete_message",
		"session_key":    rc.SessionKey,
		"preview_handle": rc.ReplyCtx,
	})
}

func (bp *BridgePlatform) StartTyping(ctx context.Context, replyCtx any) (stop func()) {
	rc, ok := replyCtx.(*bridgeReplyCtx)
	if !ok {
		return func() {}
	}
	a := bp.server.getAdapter(rc.Platform)
	if a == nil || !a.capabilities["typing"] {
		return func() {}
	}
	_ = bp.server.sendToAdapter(rc.Platform, map[string]any{
		"type":        "typing_start",
		"session_key": rc.SessionKey,
		"reply_ctx":   rc.ReplyCtx,
	})
	return func() {
		_ = bp.server.sendToAdapter(rc.Platform, map[string]any{
			"type":        "typing_stop",
			"session_key": rc.SessionKey,
			"reply_ctx":   rc.ReplyCtx,
		})
	}
}

func (bp *BridgePlatform) SendAudio(ctx context.Context, replyCtx any, audio []byte, format string) error {
	rc, ok := replyCtx.(*bridgeReplyCtx)
	if !ok {
		return fmt.Errorf("bridge: invalid reply context")
	}
	a := bp.server.getAdapter(rc.Platform)
	if a == nil || !a.capabilities["audio"] {
		return ErrNotSupported
	}
	return bp.server.sendToAdapter(rc.Platform, map[string]any{
		"type":        "audio",
		"session_key": rc.SessionKey,
		"reply_ctx":   rc.ReplyCtx,
		"data":        base64.StdEncoding.EncodeToString(audio),
		"format":      format,
	})
}

func (bp *BridgePlatform) SetCardNavigationHandler(h CardNavigationHandler) {
	bp.navHandler = h
}

// ---------------------------------------------------------------------------
// WebSocket connection handling (on BridgeServer)
// ---------------------------------------------------------------------------

func (bs *BridgeServer) handleWS(w http.ResponseWriter, r *http.Request) {
	if !bs.authenticate(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("bridge: websocket upgrade failed", "error", err)
		return
	}

	slog.Info("bridge: new connection", "remote", conn.RemoteAddr())
	bs.handleConnection(conn)
}

func (bs *BridgeServer) handleConnection(conn *websocket.Conn) {
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		return nil
	})

	// First message must be "register"
	_, raw, err := conn.ReadMessage()
	if err != nil {
		slog.Error("bridge: read register failed", "error", err)
		return
	}

	var reg bridgeRegister
	if err := json.Unmarshal(raw, &reg); err != nil || reg.Type != "register" {
		writeJSON(conn, nil, map[string]any{"type": "register_ack", "ok": false, "error": "first message must be register"})
		return
	}

	if reg.Platform == "" {
		writeJSON(conn, nil, map[string]any{"type": "register_ack", "ok": false, "error": "platform name is required"})
		return
	}

	caps := make(map[string]bool, len(reg.Capabilities))
	for _, c := range reg.Capabilities {
		caps[c] = true
	}
	caps["text"] = true

	adapter := &bridgeAdapter{
		platform:        reg.Platform,
		capabilities:    caps,
		conn:            conn,
		server:          bs,
		previewRequests: make(map[string]chan string),
	}

	bs.mu.Lock()
	if old, exists := bs.adapters[reg.Platform]; exists {
		old.conn.Close()
		slog.Info("bridge: replaced existing adapter", "platform", reg.Platform)
	}
	bs.adapters[reg.Platform] = adapter
	bs.mu.Unlock()

	writeJSON(conn, &adapter.writeMu, map[string]any{"type": "register_ack", "ok": true})
	slog.Info("bridge: adapter registered", "platform", reg.Platform, "capabilities", reg.Capabilities)

	defer func() {
		bs.mu.Lock()
		if bs.adapters[reg.Platform] == adapter {
			delete(bs.adapters, reg.Platform)
		}
		bs.mu.Unlock()
		slog.Info("bridge: adapter disconnected", "platform", reg.Platform)
	}()

	for {
		conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				slog.Debug("bridge: read error", "platform", reg.Platform, "error", err)
			}
			return
		}

		var base bridgeMsg
		if err := json.Unmarshal(raw, &base); err != nil {
			slog.Debug("bridge: invalid JSON", "platform", reg.Platform, "error", err)
			continue
		}

		switch base.Type {
		case "message":
			adapter.handleMessage(raw)
		case "card_action":
			adapter.handleCardAction(raw)
		case "preview_ack":
			adapter.handlePreviewAck(raw)
		case "ping":
			writeJSON(conn, &adapter.writeMu, map[string]any{"type": "pong", "ts": time.Now().UnixMilli()})
		default:
			slog.Debug("bridge: unknown message type", "platform", reg.Platform, "type", base.Type)
		}
	}
}

// ---------------------------------------------------------------------------
// Adapter message handlers
// ---------------------------------------------------------------------------

func (a *bridgeAdapter) handleMessage(raw json.RawMessage) {
	var m bridgeMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		slog.Debug("bridge: invalid message payload", "error", err)
		return
	}

	if m.SessionKey == "" || m.UserID == "" {
		slog.Debug("bridge: message missing required fields", "platform", a.platform)
		return
	}

	ref := a.server.resolveEngine(m.SessionKey)
	if ref == nil {
		slog.Warn("bridge: no engine for session", "platform", a.platform, "session_key", m.SessionKey)
		return
	}

	msg := &Message{
		SessionKey: m.SessionKey,
		Platform:   a.platform,
		MessageID:  m.MsgID,
		UserID:     m.UserID,
		UserName:   m.UserName,
		Content:    m.Content,
		ReplyCtx: &bridgeReplyCtx{
			Platform:   a.platform,
			SessionKey: m.SessionKey,
			ReplyCtx:   m.ReplyCtx,
		},
	}

	for _, img := range m.Images {
		data, err := base64.StdEncoding.DecodeString(img.Data)
		if err != nil {
			slog.Debug("bridge: invalid image base64", "error", err)
			continue
		}
		msg.Images = append(msg.Images, ImageAttachment{
			MimeType: img.MimeType, Data: data, FileName: img.FileName,
		})
	}

	for _, f := range m.Files {
		data, err := base64.StdEncoding.DecodeString(f.Data)
		if err != nil {
			slog.Debug("bridge: invalid file base64", "error", err)
			continue
		}
		msg.Files = append(msg.Files, FileAttachment{
			MimeType: f.MimeType, Data: data, FileName: f.FileName,
		})
	}

	if m.Audio != nil {
		if data, err := base64.StdEncoding.DecodeString(m.Audio.Data); err == nil {
			msg.Audio = &AudioAttachment{
				MimeType: m.Audio.MimeType, Data: data,
				Format: m.Audio.Format, Duration: m.Audio.Duration,
			}
		}
	}

	slog.Info("bridge: message received",
		"platform", a.platform, "session_key", m.SessionKey,
		"user", m.UserID, "content_len", len(m.Content),
	)

	if ref.platform.handler != nil {
		ref.platform.handler(ref.platform, msg)
	}
}

func (a *bridgeAdapter) handleCardAction(raw json.RawMessage) {
	var ca bridgeCardAction
	if err := json.Unmarshal(raw, &ca); err != nil {
		slog.Debug("bridge: invalid card_action payload", "error", err)
		return
	}

	slog.Debug("bridge: card_action", "platform", a.platform, "action", ca.Action, "session_key", ca.SessionKey)

	ref := a.server.resolveEngine(ca.SessionKey)
	if ref == nil || ref.platform.navHandler == nil {
		return
	}

	card := ref.platform.navHandler(ca.Action, ca.SessionKey)
	if card == nil {
		return
	}

	rc := &bridgeReplyCtx{
		Platform:   a.platform,
		SessionKey: ca.SessionKey,
		ReplyCtx:   ca.ReplyCtx,
	}

	if a.capabilities["card"] {
		_ = a.server.sendToAdapter(a.platform, map[string]any{
			"type":        "card",
			"session_key": ca.SessionKey,
			"reply_ctx":   ca.ReplyCtx,
			"card":        serializeCard(card),
		})
	} else {
		_ = ref.platform.Reply(context.Background(), rc, card.RenderText())
	}
}

func (a *bridgeAdapter) handlePreviewAck(raw json.RawMessage) {
	var ack bridgePreviewAck
	if err := json.Unmarshal(raw, &ack); err != nil {
		return
	}

	a.previewMu.Lock()
	ch, ok := a.previewRequests[ack.RefID]
	if ok {
		delete(a.previewRequests, ack.RefID)
	}
	a.previewMu.Unlock()

	if ok {
		ch <- ack.PreviewHandle
	}
}

// ---------------------------------------------------------------------------
// Session management REST API (on BridgeServer)
// ---------------------------------------------------------------------------

// authHTTP wraps an HTTP handler with token authentication.
func (bs *BridgeServer) authHTTP(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !bs.authenticate(r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "unauthorized"})
			return
		}
		handler(w, r)
	}
}

func bridgeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data})
}

func bridgeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": msg})
}

// resolveEngineForSessionKey returns the engine ref for a given session key.
func (bs *BridgeServer) resolveEngineForSessionKey(sessionKey string) *bridgeEngineRef {
	return bs.resolveEngine(sessionKey)
}

// handleSessions handles GET /bridge/sessions and POST /bridge/sessions.
func (bs *BridgeServer) handleSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		sessionKey := r.URL.Query().Get("session_key")
		if sessionKey == "" {
			bridgeError(w, http.StatusBadRequest, "session_key query parameter is required")
			return
		}
		ref := bs.resolveEngineForSessionKey(sessionKey)
		if ref == nil {
			bridgeError(w, http.StatusNotFound, "no engine found for session key")
			return
		}

		sessions := ref.engine.sessions.ListSessions(sessionKey)
		activeID := ref.engine.sessions.ActiveSessionID(sessionKey)

		list := make([]map[string]any, len(sessions))
		for i, s := range sessions {
			list[i] = map[string]any{
				"id":            s.ID,
				"name":          s.GetName(),
				"history_count": len(s.History),
			}
		}

		bridgeJSON(w, http.StatusOK, map[string]any{
			"sessions":          list,
			"active_session_id": activeID,
		})

	case http.MethodPost:
		var body struct {
			SessionKey string `json:"session_key"`
			Name       string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			bridgeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if body.SessionKey == "" {
			bridgeError(w, http.StatusBadRequest, "session_key is required")
			return
		}
		ref := bs.resolveEngineForSessionKey(body.SessionKey)
		if ref == nil {
			bridgeError(w, http.StatusNotFound, "no engine found for session key")
			return
		}
		name := body.Name
		if name == "" {
			name = "default"
		}
		s := ref.engine.sessions.NewSession(body.SessionKey, name)
		bridgeJSON(w, http.StatusOK, map[string]any{
			"id":      s.ID,
			"name":    s.GetName(),
			"message": "session created",
		})

	default:
		bridgeError(w, http.StatusMethodNotAllowed, "GET or POST only")
	}
}

// handleSessionRoutes dispatches /bridge/sessions/{sub} routes.
func (bs *BridgeServer) handleSessionRoutes(w http.ResponseWriter, r *http.Request) {
	sub := strings.TrimPrefix(r.URL.Path, "/bridge/sessions/")
	if sub == "" {
		bridgeError(w, http.StatusBadRequest, "session id required")
		return
	}

	// POST /bridge/sessions/switch
	if sub == "switch" {
		bs.handleSessionSwitch(w, r)
		return
	}

	// GET or DELETE /bridge/sessions/{id}
	sessionKey := r.URL.Query().Get("session_key")
	if sessionKey == "" {
		bridgeError(w, http.StatusBadRequest, "session_key query parameter is required")
		return
	}
	ref := bs.resolveEngineForSessionKey(sessionKey)
	if ref == nil {
		bridgeError(w, http.StatusNotFound, "no engine found for session key")
		return
	}

	switch r.Method {
	case http.MethodGet:
		s := ref.engine.sessions.FindByID(sub)
		if s == nil {
			bridgeError(w, http.StatusNotFound, "session not found")
			return
		}
		histLimit := 50
		if v := r.URL.Query().Get("history_limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				histLimit = n
			}
		}
		hist := s.GetHistory(histLimit)
		histJSON := make([]map[string]any, len(hist))
		for i, h := range hist {
			histJSON[i] = map[string]any{
				"role":    h.Role,
				"content": h.Content,
			}
		}
		bridgeJSON(w, http.StatusOK, map[string]any{
			"id":      s.ID,
			"name":    s.GetName(),
			"history": histJSON,
		})

	case http.MethodDelete:
		if ref.engine.sessions.DeleteByID(sub) {
			bridgeJSON(w, http.StatusOK, map[string]string{"message": "session deleted"})
		} else {
			bridgeError(w, http.StatusNotFound, "session not found")
		}

	default:
		bridgeError(w, http.StatusMethodNotAllowed, "GET or DELETE only")
	}
}

// handleSessionSwitch handles POST /bridge/sessions/switch.
func (bs *BridgeServer) handleSessionSwitch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		bridgeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var body struct {
		SessionKey string `json:"session_key"`
		Target     string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		bridgeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.SessionKey == "" || body.Target == "" {
		bridgeError(w, http.StatusBadRequest, "session_key and target are required")
		return
	}
	ref := bs.resolveEngineForSessionKey(body.SessionKey)
	if ref == nil {
		bridgeError(w, http.StatusNotFound, "no engine found for session key")
		return
	}
	s, err := ref.engine.sessions.SwitchSession(body.SessionKey, body.Target)
	if err != nil {
		bridgeError(w, http.StatusNotFound, err.Error())
		return
	}
	bridgeJSON(w, http.StatusOK, map[string]any{
		"message":           "session switched",
		"active_session_id": s.ID,
	})
}

// ---------------------------------------------------------------------------
// Internal helpers (on BridgeServer)
// ---------------------------------------------------------------------------

func (bs *BridgeServer) authenticate(r *http.Request) bool {
	if bs.token == "" {
		return true
	}
	if auth := r.Header.Get("Authorization"); auth != "" {
		if strings.HasPrefix(auth, "Bearer ") {
			return subtle.ConstantTimeCompare([]byte(auth[7:]), []byte(bs.token)) == 1
		}
	}
	if tok := r.Header.Get("X-Bridge-Token"); tok != "" {
		return subtle.ConstantTimeCompare([]byte(tok), []byte(bs.token)) == 1
	}
	if tok := r.URL.Query().Get("token"); tok != "" {
		return subtle.ConstantTimeCompare([]byte(tok), []byte(bs.token)) == 1
	}
	return false
}

func (bs *BridgeServer) getAdapter(platform string) *bridgeAdapter {
	bs.mu.RLock()
	defer bs.mu.RUnlock()
	return bs.adapters[platform]
}

func (bs *BridgeServer) sendToAdapter(platform string, msg map[string]any) error {
	bs.mu.RLock()
	a, ok := bs.adapters[platform]
	bs.mu.RUnlock()
	if !ok {
		return fmt.Errorf("bridge: adapter %q not connected", platform)
	}
	return writeJSON(a.conn, &a.writeMu, msg)
}

func (bs *BridgeServer) platformFromSessionKey(sessionKey string) string {
	if idx := strings.Index(sessionKey, ":"); idx > 0 {
		candidate := sessionKey[:idx]
		bs.mu.RLock()
		_, ok := bs.adapters[candidate]
		bs.mu.RUnlock()
		if ok {
			return candidate
		}
	}
	return ""
}

// resolveEngine finds the engine to handle a session_key.
// If only one engine is registered, it returns that one.
func (bs *BridgeServer) resolveEngine(sessionKey string) *bridgeEngineRef {
	bs.enginesMu.RLock()
	defer bs.enginesMu.RUnlock()

	if len(bs.engines) == 1 {
		for _, ref := range bs.engines {
			return ref
		}
	}

	// TODO: support project routing via adapter register or session_key convention
	// For now, return the first engine.
	for _, ref := range bs.engines {
		return ref
	}
	return nil
}

func writeJSON(conn *websocket.Conn, mu *sync.Mutex, v any) error {
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}
	return conn.WriteJSON(v)
}

// serializeCard converts a Card into a JSON-friendly map for the bridge protocol.
func serializeCard(c *Card) map[string]any {
	result := make(map[string]any)
	if c.Header != nil {
		result["header"] = map[string]string{
			"title": c.Header.Title,
			"color": c.Header.Color,
		}
	}
	var elements []map[string]any
	for _, elem := range c.Elements {
		switch e := elem.(type) {
		case CardMarkdown:
			elements = append(elements, map[string]any{"type": "markdown", "content": e.Content})
		case CardDivider:
			elements = append(elements, map[string]any{"type": "divider"})
		case CardActions:
			var btns []map[string]any
			for _, b := range e.Buttons {
				btns = append(btns, map[string]any{
					"text": b.Text, "btn_type": b.Type, "value": b.Value,
				})
			}
			elements = append(elements, map[string]any{
				"type": "actions", "buttons": btns, "layout": string(e.Layout),
			})
		case CardNote:
			m := map[string]any{"type": "note", "text": e.Text}
			if e.Tag != "" {
				m["tag"] = e.Tag
			}
			elements = append(elements, m)
		case CardListItem:
			elements = append(elements, map[string]any{
				"type": "list_item", "text": e.Text,
				"btn_text": e.BtnText, "btn_type": e.BtnType, "btn_value": e.BtnValue,
			})
		case CardSelect:
			var opts []map[string]string
			for _, o := range e.Options {
				opts = append(opts, map[string]string{"text": o.Text, "value": o.Value})
			}
			elements = append(elements, map[string]any{
				"type": "select", "placeholder": e.Placeholder,
				"options": opts, "init_value": e.InitValue,
			})
		}
	}
	result["elements"] = elements
	return result
}
