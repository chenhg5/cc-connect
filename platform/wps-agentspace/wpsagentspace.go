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
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
	"github.com/google/uuid"
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
	appID      string
	wpsSid     string // wps_sid token; encrypted until Start() decrypts it
	deviceUuid string
	deviceName string
	baseURL    string
	handler    core.MessageHandler
	cancel     context.CancelFunc
	conn       *websocket.Conn
	mu         sync.Mutex
	writeCh    chan any
	dedup      core.MessageDedup
	stopOnce   sync.Once
	stopped    atomic.Bool
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
	Role       string   `json:"role"`
	Type       string   `json:"type"`
	Content    string   `json:"content"`
	SessionID  string   `json:"session_id"`
	ChatID     string   `json:"chat_id"`
	MessageID  string   `json:"message_id"`
	Timestamp  int64    `json:"timestamp"`
	DeviceUUID string   `json:"device_uuid"`
	DeviceName string   `json:"device_name"`
	MediaURLs  []string `json:"media_urls,omitempty"`
	MediaURL   string   `json:"media_url,omitempty"`
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

		// Auto-save to config file
		if encrypted != "" {
			saved := saveToConfig(appID, encrypted)
			if saved {
				fmt.Println("\n✓ Token 已自动保存到配置文件")
			} else {
				fmt.Println("\n=========================================")
				fmt.Println("  Token 已获取，请设置环境变量：")
				fmt.Println("=========================================")
				fmt.Printf("\n  export WPS_SID='%s'\n\n", encrypted)
				fmt.Println("  或添加到 ~/.zshrc / ~/.bashrc")
				fmt.Println("=========================================")
			}
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

	decryptedSid, err := decryptWpsSid(p.wpsSid, p.appID)
	if err != nil {
		return fmt.Errorf("wps-agentspace: failed to decrypt wps_sid: %w", err)
	}
	p.wpsSid = decryptedSid
	slog.Info("wps-agentspace: token loaded", "length", len(p.wpsSid), "has_colons", strings.Contains(p.wpsSid, ":"))

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

// SendFile implements core.FileSender. The WPS Agentspace WebSocket protocol
// has no native binary file frame, so we persist the file to a shared
// directory and reply with a path the user can open locally.
func (p *Platform) SendFile(ctx context.Context, replyCtx any, file core.FileAttachment) error {
	rc, ok := replyCtx.(*replyContext)
	if !ok {
		return fmt.Errorf("wps-agentspace: SendFile: invalid reply context type %T", replyCtx)
	}

	path, err := p.saveAttachment(file.FileName, file.Data)
	if err != nil {
		return fmt.Errorf("wps-agentspace: SendFile: %w", err)
	}

	notice := fmt.Sprintf("📎 文件已保存到本地：\n%s", path)
	return p.sendText(rc.ChatID, notice, rc)
}

// SendImage implements core.ImageSender. Same disk-persist fallback as SendFile.
func (p *Platform) SendImage(ctx context.Context, replyCtx any, img core.ImageAttachment) error {
	rc, ok := replyCtx.(*replyContext)
	if !ok {
		return fmt.Errorf("wps-agentspace: SendImage: invalid reply context type %T", replyCtx)
	}

	name := img.FileName
	if name == "" {
		ext := ".png"
		if strings.HasPrefix(img.MimeType, "image/jpeg") {
			ext = ".jpg"
		} else if strings.HasPrefix(img.MimeType, "image/gif") {
			ext = ".gif"
		} else if strings.HasPrefix(img.MimeType, "image/webp") {
			ext = ".webp"
		}
		name = "image_" + time.Now().Format("20060102_150405") + ext
	}

	path, err := p.saveAttachment(name, img.Data)
	if err != nil {
		return fmt.Errorf("wps-agentspace: SendImage: %w", err)
	}

	notice := fmt.Sprintf("🖼 图片已保存到本地：\n%s", path)
	return p.sendText(rc.ChatID, notice, rc)
}

// saveAttachment writes data to ~/.cc-connect/attachments/<name> and returns
// the absolute path. The filename is sanitized to a basename to prevent path
// traversal.
func (p *Platform) saveAttachment(name string, data []byte) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	dir := filepath.Join(home, ".cc-connect", "attachments")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}

	name = filepath.Base(name)
	if name == "" || name == "." || name == "/" {
		name = fmt.Sprintf("file_%d", time.Now().UnixMilli())
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	return path, nil
}

// Stop gracefully shuts down the platform.
func (p *Platform) Stop() error {
	p.stopOnce.Do(func() {
		p.stopped.Store(true)
		if p.cancel != nil {
			p.cancel()
		}
		p.mu.Lock()
		if p.conn != nil {
			_ = p.conn.Close()
		}
		p.mu.Unlock()
	})
	return nil
}

// connectLoop manages the WebSocket connection with automatic reconnection.
func (p *Platform) connectLoop(ctx context.Context) {
	attempt := 0
	for {
		if p.stopped.Load() {
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
		_ = conn.Close()
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
			_ = p.writeJSON("ping", pingData{
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
		if p.stopped.Load() {
			return nil
		}

		_ = conn.SetReadDeadline(time.Now().Add(readDeadline))

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

	case "ping":
		// Server sends JSON ping frames in addition to WebSocket control pings;
		// reply with a pong event so the server treats this device as responsive.
		_ = p.writeJSON("pong", pingData{
			DeviceUUID: p.deviceUuid,
			DeviceName: p.deviceName,
			Timestamp:  time.Now().UnixMilli(),
		})
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
		slog.Debug("wps-agentspace: message frame", "role", data.Role, "type", data.Type, "content_len", len(data.Content))
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

	// Collect media URLs (file attachments from user)
	var mediaURLs []string
	if len(data.MediaURLs) > 0 {
		mediaURLs = data.MediaURLs
	} else if data.MediaURL != "" {
		mediaURLs = []string{data.MediaURL}
	}

	// If only media without text, use first URL as content
	if content == "" && len(mediaURLs) > 0 {
		content = mediaURLs[0]
	}

	if content == "" {
		return nil
	}

	// Append media URLs to content so Claude can access files
	if len(mediaURLs) > 0 {
		content = content + "\n\n[附件]\n" + strings.Join(mediaURLs, "\n")
	}

	slog.Info("wps-agentspace: received message", "chat_id", chatID, "content", truncate(content, 100), "media_count", len(mediaURLs))

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
	_ = p.writeJSON("typing", typingData{
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
		if p.stopped.Load() {
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
)

// OAuth endpoints (variables so tests can override them).
var (
	loginURLAPI  = "https://agentspace.wps.cn/v7/devhub/users/login_url"
	userTokenAPI = "https://agentspace.wps.cn/v7/devhub/users/user_token"

	// pollInterval controls how often autoLogin polls for the user token.
	pollInterval = 3 * time.Second
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
	defer func() { _ = resp.Body.Close() }()

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

	client := &http.Client{Timeout: 10 * time.Second}

	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)

		pollBody, _ := json.Marshal(map[string]string{
			"app_id": appID,
			"code":   code,
			"state":  state,
		})

		pollResp, err := client.Post(userTokenAPI, "application/json", strings.NewReader(string(pollBody)))
		if err != nil {
			continue
		}

		var tokenResp struct {
			Data struct {
				Token string `json:"token"`
			} `json:"data"`
		}
		decodeErr := json.NewDecoder(pollResp.Body).Decode(&tokenResp)
		_ = pollResp.Body.Close()
		if decodeErr != nil {
			continue
		}

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
// Overridable for tests.
var openBrowser = defaultOpenBrowser

func defaultOpenBrowser(rawURL string) {
	var cmd string
	var args []string

	switch {
	case isMacOS():
		cmd = "open"
		args = []string{rawURL}
	case isLinux():
		cmd = "xdg-open"
		args = []string{rawURL}
	case isWindows():
		// "start" is a cmd builtin; first quoted arg is the window title.
		cmd = "cmd"
		args = []string{"/c", "start", "", rawURL}
	default:
		fmt.Printf("请手动打开: %s\n", rawURL)
		return
	}

	go func() {
		if err := exec.Command(cmd, args...).Run(); err != nil {
			slog.Warn("wps-agentspace: failed to open browser", "url", rawURL, "error", err)
			fmt.Printf("请手动打开: %s\n", rawURL)
		}
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

// saveToConfig saves the encrypted token to the cc-connect config file.
// It validates the file as TOML first, then performs a surgical line-level
// update inside the wps-agentspace platform options block so comments and
// field order outside that block are preserved.
func saveToConfig(appID, encryptedToken string) bool {
	configPath := findConfigFile()
	if configPath == "" {
		return false
	}

	data, err := readFile(configPath)
	if err != nil {
		slog.Warn("wps-agentspace: failed to read config", "error", err)
		return false
	}

	// Validate existing TOML before modifying it. If it's already broken,
	// don't risk making it worse.
	if _, err := toml.Decode(string(data), &struct{}{}); err != nil {
		slog.Warn("wps-agentspace: config is not valid TOML, skipping auto-save", "error", err)
		return false
	}

	updated, ok := updateWpsSidLine(string(data), encryptedToken)
	if !ok {
		return false
	}

	if err := writeFile(configPath, updated); err != nil {
		slog.Warn("wps-agentspace: failed to write config", "error", err)
		return false
	}

	return true
}

// updateWpsSidLine replaces or inserts the wps_sid key inside the
// wps-agentspace platform options block. It returns the updated content and
// true on success.
func updateWpsSidLine(content, encryptedToken string) (string, bool) {
	lines := strings.Split(content, "\n")

	typeLine := regexp.MustCompile(`^\s*type\s*=\s*"wps-agentspace"\s*$`)
	optsHeader := regexp.MustCompile(`^\s*\[projects\.platforms\.options\]\s*$`)
	wpsSidLine := regexp.MustCompile(`^\s*wps_sid\s*=.*$`)
	sectionStart := regexp.MustCompile(`^\s*\[`)

	for i, line := range lines {
		if !typeLine.MatchString(line) {
			continue
		}

		// Find the options header that belongs to this platform entry.
		optsIdx := -1
		for j := i + 1; j < len(lines); j++ {
			if sectionStart.MatchString(lines[j]) && !optsHeader.MatchString(lines[j]) {
				break
			}
			if optsHeader.MatchString(lines[j]) {
				optsIdx = j
				break
			}
		}
		if optsIdx == -1 {
			continue
		}

		// Look for an existing wps_sid inside this options block.
		for k := optsIdx + 1; k < len(lines); k++ {
			if sectionStart.MatchString(lines[k]) {
				break
			}
			if wpsSidLine.MatchString(lines[k]) {
				lines[k] = fmt.Sprintf(`wps_sid = "%s"  # auto-saved`, quoteTomlBasicString(encryptedToken))
				return strings.Join(lines, "\n"), true
			}
		}

		// Not found: insert after the options header.
		newSidLine := fmt.Sprintf(`wps_sid = "%s"  # auto-saved`, quoteTomlBasicString(encryptedToken))
		newLines := make([]string, len(lines)+1)
		copy(newLines, lines[:optsIdx+1])
		newLines[optsIdx+1] = newSidLine
		copy(newLines[optsIdx+2:], lines[optsIdx+1:])
		return strings.Join(newLines, "\n"), true
	}

	return "", false
}

// quoteTomlBasicString escapes characters that are special in a TOML basic string.
func quoteTomlBasicString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}

// findConfigFile finds the cc-connect config file. It prefers the path cc-connect
// actually loaded (set by main via config.ConfigPath, so --config flags and
// non-default locations just work), falls back to an explicit CC_CONNECT_CONFIG
// env override, then to the default discovery locations.
func findConfigFile() string {
	// Prefer the path cc-connect actually loaded at startup.
	if cp := config.ConfigPath; cp != "" && fileExists(cp) {
		return cp
	}

	// Allow explicit override via environment variable.
	if envPath := os.Getenv("CC_CONNECT_CONFIG"); envPath != "" {
		expanded := expandPath(envPath)
		if fileExists(expanded) {
			return expanded
		}
	}

	locations := []string{
		"config.toml",
		"~/.cc-connect/config.toml",
	}

	for _, loc := range locations {
		expanded := expandPath(loc)
		if fileExists(expanded) {
			return expanded
		}
	}
	return ""
}

func expandPath(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}

	// "~" or "~/..." → current user's home.
	if path == "~" || strings.HasPrefix(path, "~/") {
		home := os.Getenv("HOME")
		if home == "" {
			home = os.Getenv("USERPROFILE")
		}
		return strings.Replace(path, "~", home, 1)
	}

	// "~user" or "~user/..." → that user's home directory.
	rest := path[1:]
	end := strings.IndexByte(rest, '/')
	name := rest
	if end != -1 {
		name = rest[:end]
	}
	u, err := user.Lookup(name)
	if err != nil {
		// Can't resolve (unknown user, headless/CGO-disabled build, Windows).
		// Leave the path untouched rather than returning a broken value.
		return path
	}
	if end == -1 {
		return u.HomeDir
	}
	return u.HomeDir + rest[end:]
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func writeFile(path string, data string) error {
	return os.WriteFile(path, []byte(data), 0o644)
}

// getEnvWithPrefix gets environment variable
func getEnvWithPrefix(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return strings.TrimSpace(strings.Trim(val, "'\""))
	}
	return defaultVal
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

	plaintext, err := decryptGCM(salt, iv, authTag, ciphertext, keySource)
	if err != nil {
		// Fallback: a token encrypted before an appID change (or while appID
		// was empty) used defaultKeySource. Retry with it. AES-GCM
		// authenticates via the auth tag, so a wrong key fails cleanly
		// instead of returning garbage — the fallback is safe.
		if keySource != defaultKeySource {
			plaintext, err = decryptGCM(salt, iv, authTag, ciphertext, defaultKeySource)
		}
		if err != nil {
			return "", fmt.Errorf("decrypt: %w", err)
		}
	}

	return string(plaintext), nil
}

// decryptGCM derives a key from keySource and decrypts the GCM ciphertext.
// ciphertext and authTag are not mutated.
func decryptGCM(salt, iv, authTag, ciphertext []byte, keySource string) ([]byte, error) {
	// Node.js crypto.scryptSync default: N=16384, r=8, p=1
	key, err := scrypt.Key([]byte(keySource), salt, 16384, 8, 1, keyLength)
	if err != nil {
		return nil, fmt.Errorf("derive key: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}

	// GCM expects the auth tag appended to the ciphertext. Copy into a new
	// slice so the caller's ciphertext is not mutated (needed for the
	// defaultKeySource fallback retry).
	gcmPayload := make([]byte, 0, len(ciphertext)+len(authTag))
	gcmPayload = append(gcmPayload, ciphertext...)
	gcmPayload = append(gcmPayload, authTag...)

	return aesGCM.Open(nil, iv, gcmPayload, nil)
}

// generateUUID generates a random v4 UUID.
func generateUUID() string {
	return uuid.NewString()
}

// truncate truncates a string to maxLen characters.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
