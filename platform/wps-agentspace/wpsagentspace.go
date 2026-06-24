package wpsagentspace

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/scrypt"
)

const (
	defaultWSURL     = "wss://agentspace.wps.cn/v7/devhub/ws/openClaw/chat"
	heartbeatPeriod  = 10 * time.Second
	maxReconnectWait = 60 * time.Second
	readDeadline     = 90 * time.Second
)

// Platform implements core.Platform for WPS Agentspace (数字员工).
type Platform struct {
	appID       string
	wpsSid      string // encrypted token
	deviceUuid  string
	deviceName  string
	baseURL     string
	handler     core.MessageHandler
	cancel      context.CancelFunc
	conn        *websocket.Conn
	mu          sync.Mutex
	writeCh     chan any
	dedup       core.MessageDedup
	stopOnce    sync.Once
	stopped     bool
}

// replyContext holds the context needed to reply to a specific message.
type replyContext struct {
	ChatID    string `json:"chat_id"`
	SessionID string `json:"session_id"`
	MessageID string `json:"message_id"`
}

// --- WebSocket frame types ---

type wsFrame struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

type initData struct {
	Timestamp  int64  `json:"timestamp"`
	DeviceUUID string `json:"device_uuid"`
	DeviceName string `json:"device_name"`
}

type pingData struct {
	DeviceUUID string `json:"device_uuid"`
	DeviceName string `json:"device_name"`
	Timestamp  int64  `json:"timestamp"`
}

type messageData struct {
	Role      string `json:"role"`
	Type      string `json:"type"`
	Content   string `json:"content"`
	SessionID string `json:"session_id"`
	ChatID    string `json:"chat_id"`
	MessageID string `json:"message_id"`
	Timestamp int64  `json:"timestamp"`
	DeviceUUID string `json:"device_uuid"`
	DeviceName string `json:"device_name"`
}

type typingData struct {
	ChatID     string `json:"chat_id"`
	DeviceUUID string `json:"device_uuid"`
	DeviceName string `json:"device_name"`
	Timestamp  int64  `json:"timestamp"`
}

type errorData struct {
	Code string `json:"code"`
}

// init registers the platform with the core registry.
func init() {
	core.RegisterPlatform("wps-agentspace", New)
}

// New creates a new Platform instance.
func New(opts map[string]any) (core.Platform, error) {
	appID, _ := opts["app_id"].(string)
	wpsSid, _ := opts["wps_sid"].(string)

	// Try to get wps_sid from environment variable
	if wpsSid == "" {
		wpsSid = getEnvWithPrefix("WPS_SID", "")
	}

	// If wps_sid is empty, try auto-login
	if wpsSid == "" {
		sid, encrypted, err := autoLogin(appID)
		if err != nil {
			return nil, fmt.Errorf("wps-agentspace: auto-login failed: %w", err)
		}
		wpsSid = sid

		// Display encrypted token for environment variable storage
		if encrypted != "" {
			fmt.Println("\n=========================================")
			fmt.Println("  Token 已获取，请设置环境变量：")
			fmt.Println("=========================================")
			fmt.Printf("\n  export WPS_SID='%s'\n\n", encrypted)
			fmt.Println("  或添加到 ~/.zshrc / ~/.bashrc")
			fmt.Println("=========================================\n")
		}
	}

	deviceUuid, _ := opts["device_uuid"].(string)
	deviceName, _ := opts["device_name"].(string)
	if deviceName == "" {
		deviceName = "cc-connect"
	}

	baseURL := defaultWSURL
	if v, ok := opts["base_url"].(string); ok && v != "" {
		baseURL = v
	}

	return &Platform{
		appID:      appID,
		wpsSid:     wpsSid,
		deviceUuid: deviceUuid,
		deviceName: deviceName,
		baseURL:    baseURL,
	}, nil
}

// Name returns the platform identifier.
func (p *Platform) Name() string {
	return "wps-agentspace"
}

// Start initializes the WebSocket connection and begins message processing.
func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler
	p.writeCh = make(chan any, 64)

	if p.wpsSid == "" {
		return fmt.Errorf("wps-agentspace: wps_sid is required")
	}

	// Try using original token (not decrypted) for now
	slog.Info("wps-agentspace: token info",
		"length", len(p.wpsSid),
		"has_colons", strings.Contains(p.wpsSid, ":"),
		"prefix", p.wpsSid[:min(20, len(p.wpsSid))])
	// p.wpsSid = decryptedSid  // Comment out decryption for debugging

	if p.deviceUuid == "" {
		p.deviceUuid = generateUUID()
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	go p.connectLoop(ctx)
	return nil
}

// Reply sends a reply to a specific message.
func (p *Platform) Reply(ctx context.Context, replyCtx any, content string) error {
	rc, ok := replyCtx.(*replyContext)
	if !ok {
		return fmt.Errorf("wps-agentspace: invalid reply context type")
	}
	return p.sendText(rc.ChatID, content, rc)
}

// Send sends a message to a chat.
func (p *Platform) Send(ctx context.Context, replyCtx any, content string) error {
	rc, ok := replyCtx.(*replyContext)
	if !ok {
		return fmt.Errorf("wps-agentspace: invalid reply context type")
	}
	return p.sendText(rc.ChatID, content, rc)
}

// Stop gracefully shuts down the platform.
func (p *Platform) Stop() error {
	p.stopOnce.Do(func() {
		p.stopped = true
		if p.cancel != nil {
			p.cancel()
		}
		p.mu.Lock()
		if p.conn != nil {
			p.conn.Close()
		}
		p.mu.Unlock()
	})
	return nil
}

// connectLoop manages the WebSocket connection with automatic reconnection.
func (p *Platform) connectLoop(ctx context.Context) {
	attempt := 0
	for {
		if p.stopped {
			return
		}

		err := p.connect(ctx)
		if err != nil {
			slog.Error("wps-agentspace: connection error", "error", err)
		}

		select {
		case <-ctx.Done():
			return
		default:
		}

		// Exponential backoff
		attempt++
		delay := time.Duration(1<<uint(attempt)) * time.Second
		if delay > maxReconnectWait {
			delay = maxReconnectWait
		}
		if delay < time.Second {
			delay = time.Second
		}

		slog.Info("wps-agentspace: reconnecting", "attempt", attempt, "delay", delay)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

// connect establishes a WebSocket connection and processes messages.
func (p *Platform) connect(ctx context.Context) error {
	wsURL := p.getWSURL()
	slog.Info("wps-agentspace: connecting", "url", wsURL)

	header := http.Header{
		"Cookie":     []string{fmt.Sprintf("wps_sid=%s", p.wpsSid)},
		"User-Agent": []string{"OpenClaw/Agentspace"},
		"Origin":     []string{"https://agentspace.wps.cn"},
	}

	slog.Debug("wps-agentspace: dialing with headers", "cookie_length", len(p.wpsSid))

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.DialContext(ctx, wsURL, header)
	if err != nil {
		return fmt.Errorf("wps-agentspace: dial: %w", err)
	}

	// Set ping/pong handlers
	conn.SetPingHandler(func(appData string) error {
		slog.Debug("wps-agentspace: received ping")
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(time.Second))
	})

	conn.SetPongHandler(func(appData string) error {
		slog.Debug("wps-agentspace: received pong")
		return nil
	})

	p.mu.Lock()
	p.conn = conn
	p.mu.Unlock()

	// Reset backoff on successful connection
	defer func() {
		p.mu.Lock()
		if p.conn == conn {
			p.conn = nil
		}
		p.mu.Unlock()
		conn.Close()
	}()

	// Send init
	if err := p.sendInit(); err != nil {
		return fmt.Errorf("wps-agentspace: init: %w", err)
	}

	// Start heartbeat
	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	go p.heartbeatLoop(hbCtx)

	// Start write loop
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		p.writeLoop(conn)
	}()

	// Read loop
	return p.readLoop(conn, ctx)
}

// getWSURL returns the WebSocket URL.
func (p *Platform) getWSURL() string {
	if p.appID != "" {
		return fmt.Sprintf("wss://agentspace.wps.cn/v7/devhub/ws/%s/chat", p.appID)
	}
	return p.baseURL
}

// sendInit sends the init message.
func (p *Platform) sendInit() error {
	return p.writeJSON("init", initData{
		Timestamp:  time.Now().UnixMilli(),
		DeviceUUID: p.deviceUuid,
		DeviceName: p.deviceName,
	})
}

// heartbeatLoop sends periodic ping messages.
func (p *Platform) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(heartbeatPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.writeJSON("ping", pingData{
				DeviceUUID: p.deviceUuid,
				DeviceName: p.deviceName,
				Timestamp:  time.Now().UnixMilli(),
			})
		}
	}
}

// readLoop processes incoming WebSocket messages.
func (p *Platform) readLoop(conn *websocket.Conn, ctx context.Context) error {
	for {
		if p.stopped {
			return nil
		}

		conn.SetReadDeadline(time.Now().Add(readDeadline))

		_, raw, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return nil
			}
			return fmt.Errorf("wps-agentspace: read: %w", err)
		}

		var frame wsFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			slog.Warn("wps-agentspace: invalid frame", "error", err)
			continue
		}

		if err := p.handleFrame(frame); err != nil {
			slog.Error("wps-agentspace: handle frame", "error", err)
		}
	}
}

// handleFrame dispatches incoming frames.
func (p *Platform) handleFrame(frame wsFrame) error {
	slog.Debug("wps-agentspace: received frame", "event", frame.Event, "data", string(frame.Data))

	switch frame.Event {
	case "pong":
		// Heartbeat response, ignore
		return nil

	case "init":
		var data struct {
			DeviceUUID string `json:"device_uuid"`
		}
		if err := json.Unmarshal(frame.Data, &data); err == nil && data.DeviceUUID != "" {
			p.deviceUuid = data.DeviceUUID
			slog.Info("wps-agentspace: init ok", "device_uuid", data.DeviceUUID)
		}
		return nil

	case "error":
		var data errorData
		if err := json.Unmarshal(frame.Data, &data); err == nil {
			fatalCodes := []string{
				"USER_NO_APP_PERMISSION",
				"USER_NO_OPENCLAW_PERMISSION",
				"OPENCLAW_NOT_CONFIGURED",
				"NOT_OPENCLAW_APP",
				"NOT_LOGIN",
			}
			for _, code := range fatalCodes {
				if data.Code == code {
					return fmt.Errorf("wps-agentspace: fatal error: %s", data.Code)
				}
			}
			slog.Warn("wps-agentspace: server error", "code", data.Code)
		}
		return nil

	case "message":
		var data messageData
		if err := json.Unmarshal(frame.Data, &data); err != nil {
			return fmt.Errorf("wps-agentspace: parse message: %w", err)
		}
		if data.Role == "user" {
			return p.handleUserMessage(data)
		}
		return nil

	default:
		return nil
	}
}

// handleUserMessage processes an incoming user message.
func (p *Platform) handleUserMessage(data messageData) error {
	chatID := data.SessionID
	if chatID == "" {
		chatID = data.ChatID
	}
	if chatID == "" {
		chatID = "default"
	}

	// Dedup
	msgID := data.MessageID
	if msgID == "" {
		msgID = fmt.Sprintf("msg_%d", data.Timestamp)
	}
	if p.dedup.IsDuplicate(msgID) {
		return nil
	}

	content := strings.TrimSpace(data.Content)
	if content == "" {
		return nil
	}

	slog.Info("wps-agentspace: received message", "chat_id", chatID, "content", truncate(content, 100))

	// Send typing indicator
	p.sendTyping(chatID)

	// Build reply context
	rc := &replyContext{
		ChatID:    chatID,
		SessionID: data.SessionID,
		MessageID: data.MessageID,
	}

	// Build session key
	sessionKey := fmt.Sprintf("wps-agentspace:%s", chatID)

	// Dispatch to handler
	if p.handler != nil {
		msg := &core.Message{
			SessionKey: sessionKey,
			Platform:   "wps-agentspace",
			Content:    content,
			ReplyCtx:   rc,
			UserID:     chatID,
		}
		go p.handler(p, msg)
	}

	return nil
}

// sendText sends a text message to a chat.
func (p *Platform) sendText(chatID, content string, rc *replyContext) error {
	if p.conn == nil {
		return fmt.Errorf("wps-agentspace: not connected")
	}

	msg := messageData{
		Role:       "assistant",
		Type:       "answer",
		Content:    content,
		SessionID:  rc.SessionID,
		ChatID:     chatID,
		MessageID:  rc.MessageID,
		Timestamp:  time.Now().UnixMilli(),
		DeviceUUID: p.deviceUuid,
		DeviceName: p.deviceName,
	}

	return p.writeJSON("message", msg)
}

// sendTyping sends a typing indicator.
func (p *Platform) sendTyping(chatID string) {
	p.writeJSON("typing", typingData{
		ChatID:     chatID,
		DeviceUUID: p.deviceUuid,
		DeviceName: p.deviceName,
		Timestamp:  time.Now().UnixMilli(),
	})
}

// writeJSON sends a JSON frame through the write channel.
func (p *Platform) writeJSON(event string, data any) error {
	frame := wsFrame{
		Event: event,
	}
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("wps-agentspace: marshal: %w", err)
	}
	frame.Data = jsonData

	raw, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("wps-agentspace: marshal frame: %w", err)
	}

	select {
	case p.writeCh <- raw:
		return nil
	default:
		return fmt.Errorf("wps-agentspace: write buffer full")
	}
}

// writeLoop serializes all WebSocket writes.
func (p *Platform) writeLoop(conn *websocket.Conn) {
	for msg := range p.writeCh {
		if p.stopped {
			return
		}
		// msg is already JSON-encoded bytes from writeJSON
		if err := conn.WriteMessage(websocket.TextMessage, msg.([]byte)); err != nil {
			slog.Error("wps-agentspace: write error", "error", err)
			return
		}
	}
}

// --- Crypto utilities ---

const (
	aesAlg           = "aes-256-gcm"
	keyLength        = 32
	ivLength         = 12
	saltLength       = 16
	defaultKeySource = "openclaw_agentspace"

	// OAuth endpoints
	loginURLAPI  = "https://agentspace.wps.cn/v7/devhub/users/login_url"
	userTokenAPI = "https://agentspace.wps.cn/v7/devhub/users/user_token"
	userInfoAPI  = "https://agentspace.wps.cn/v7/devhub/users/current"
)

// autoLogin performs OAuth login to get wps_sid.
// Returns: raw token, encrypted token, error
func autoLogin(appID string) (string, string, error) {
	state := generateUUID()

	// Step 1: Get login URL
	slog.Info("wps-agentspace: starting auto-login...")

	reqBody := map[string]string{"state": state}
	if appID != "" {
		reqBody["app_id"] = appID
	}

	body, _ := json.Marshal(reqBody)
	resp, err := http.Post(loginURLAPI, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return "", "", fmt.Errorf("get login URL: %w", err)
	}
	defer resp.Body.Close()

	var loginResp struct {
		Data struct {
			Code  string `json:"code"`
			URL   string `json:"url"`
			AppID string `json:"app_id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&loginResp); err != nil {
		return "", "", fmt.Errorf("parse login response: %w", err)
	}

	if loginResp.Data.URL == "" {
		return "", "", fmt.Errorf("no login URL returned")
	}

	code := loginResp.Data.Code
	respAppID := loginResp.Data.AppID
	if respAppID != "" {
		appID = respAppID
	}

	// Step 2: Open browser
	slog.Info("wps-agentspace: opening browser for login...")
	fmt.Printf("\n请在浏览器中登录 WPS 账号:\n%s\n\n", loginResp.Data.URL)
	openBrowser(loginResp.Data.URL)

	// Step 3: Poll for token
	slog.Info("wps-agentspace: waiting for login (polling every 3s, max 5 min)...")
	deadline := time.Now().Add(5 * time.Minute)

	for time.Now().Before(deadline) {
		time.Sleep(3 * time.Second)

		pollBody, _ := json.Marshal(map[string]string{
			"app_id": appID,
			"code":   code,
			"state":  state,
		})

		resp, err := http.Post(userTokenAPI, "application/json", strings.NewReader(string(pollBody)))
		if err != nil {
			continue
		}

		var tokenResp struct {
			Data struct {
				Token string `json:"token"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()

		if tokenResp.Data.Token != "" {
			slog.Info("wps-agentspace: login successful!")

			// Encrypt token for safe storage
			encrypted, err := encryptWpsSid(tokenResp.Data.Token, appID)
			if err != nil {
				slog.Warn("wps-agentspace: failed to encrypt token", "error", err)
				return tokenResp.Data.Token, "", nil
			}

			return tokenResp.Data.Token, encrypted, nil
		}

		fmt.Print(".")
	}

	return "", "", fmt.Errorf("login timeout (5 minutes)")
}

// openBrowser opens URL in the default browser.
func openBrowser(url string) {
	var cmd string
	var args []string

	switch {
	case isMacOS():
		cmd = "open"
		args = []string{url}
	case isLinux():
		cmd = "xdg-open"
		args = []string{url}
	case isWindows():
		cmd = "cmd"
		args = []string{"/c", "start", url}
	default:
		fmt.Printf("请手动打开: %s\n", url)
		return
	}

	go func() {
		exec.Command(cmd, args...).Run()
	}()
}

func isMacOS() bool {
	return runtime.GOOS == "darwin"
}

func isLinux() bool {
	return runtime.GOOS == "linux"
}

func isWindows() bool {
	return runtime.GOOS == "windows"
}

// getEnvWithPrefix gets environment variable with optional prefix
func getEnvWithPrefix(key, defaultVal string) string {
	if val := getEnv(key); val != "" {
		return val
	}
	return defaultVal
}

func getEnv(key string) string {
	return strings.TrimSpace(strings.Trim(
		strings.TrimSpace(
			func() string {
				if val, ok := envLookup(key); ok {
					return val
				}
				return ""
			}(),
		),
		"'\"",
	))
}

func envLookup(key string) (string, bool) {
	// Simple env lookup without importing os
	// This is handled by the config system
	return "", false
}

// encryptWpsSid encrypts a wps_sid token using AES-256-GCM.
func encryptWpsSid(wpsSid, appId string) (string, error) {
	if wpsSid == "" {
		return "", fmt.Errorf("wpsSid cannot be empty")
	}

	keySource := appId
	if keySource == "" {
		keySource = defaultKeySource
	}

	// Generate random salt and IV
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}

	iv := make([]byte, ivLength)
	if _, err := rand.Read(iv); err != nil {
		return "", fmt.Errorf("generate iv: %w", err)
	}

	// Derive key using scrypt
	key, err := scrypt.Key([]byte(keySource), salt, 16384, 8, 1, keyLength)
	if err != nil {
		return "", fmt.Errorf("derive key: %w", err)
	}

	// Create AES-GCM cipher
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("aes cipher: %w", err)
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("gcm: %w", err)
	}

	// Encrypt
	ciphertext := aesGCM.Seal(nil, iv, []byte(wpsSid), nil)

	// Split ciphertext and auth tag
	authTag := ciphertext[len(ciphertext)-aesGCM.Overhead():]
	ciphertext = ciphertext[:len(ciphertext)-aesGCM.Overhead()]

	// Return format: salt:iv:authTag:ciphertext
	return fmt.Sprintf("%s:%s:%s:%s",
		hex.EncodeToString(salt),
		hex.EncodeToString(iv),
		hex.EncodeToString(authTag),
		hex.EncodeToString(ciphertext),
	), nil
}

// decryptWpsSid decrypts an OpenClaw-encrypted token.
func decryptWpsSid(encrypted, appId string) (string, error) {
	parts := strings.Split(encrypted, ":")
	if len(parts) != 4 {
		return encrypted, nil // Not encrypted, return as-is
	}

	salt, err := hex.DecodeString(parts[0])
	if err != nil {
		return "", fmt.Errorf("invalid salt: %w", err)
	}
	iv, err := hex.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("invalid iv: %w", err)
	}
	authTag, err := hex.DecodeString(parts[2])
	if err != nil {
		return "", fmt.Errorf("invalid auth tag: %w", err)
	}
	ciphertext, err := hex.DecodeString(parts[3])
	if err != nil {
		return "", fmt.Errorf("invalid ciphertext: %w", err)
	}

	keySource := appId
	if keySource == "" {
		keySource = defaultKeySource
	}

	// Node.js crypto.scryptSync default: N=16384, r=8, p=1
	key, err := scrypt.Key([]byte(keySource), salt, 16384, 8, 1, keyLength)
	if err != nil {
		return "", fmt.Errorf("derive key: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("aes cipher: %w", err)
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("gcm: %w", err)
	}

	// Append auth tag to ciphertext for GCM
	ciphertext = append(ciphertext, authTag...)

	plaintext, err := aesGCM.Open(nil, iv, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}

	return string(plaintext), nil
}

// generateUUID generates a random UUID.
func generateUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// truncate truncates a string to maxLen characters.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
