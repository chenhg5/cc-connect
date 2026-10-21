package webex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

func init() {
	core.RegisterPlatform("webex", New)
}

// replyContext carries what Reply/Send need to target a Webex room.
type replyContext struct {
	roomID    string
	messageID string
	personID  string
}

// Platform is the Webex adapter implementing core.Platform.
type Platform struct {
	token     string
	allowFrom []string // lowercased email allowlist; empty = allow all

	client webexClient

	mu               sync.RWMutex
	handler          core.MessageHandler
	lifecycleHandler core.PlatformLifecycleHandler
	cancel           context.CancelFunc
	stopping         bool
	selfID           string // bot's own personId
	selfEmail        string // bot's own email (Mercury actor uses email)
	deviceURL        string // for cleanup on Stop()
}

// New constructs a Webex platform from config options.
func New(opts map[string]any) (core.Platform, error) {
	token, _ := opts["token"].(string)
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("webex: token is required")
	}
	rawAllow, _ := opts["allow_from"].(string)
	core.CheckAllowFrom("webex", rawAllow)

	return &Platform{
		token:     token,
		allowFrom: parseAllowFrom(rawAllow),
		client:    newHTTPClient(token),
	}, nil
}

func (p *Platform) Name() string { return "webex" }

// self returns the bot's own personId under lock.
func (p *Platform) self() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.selfID
}

// selfEmailAddr returns the bot's own email under lock.
func (p *Platform) selfEmailAddr() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.selfEmail
}

// parseAllowFrom splits and lowercases a comma-separated email list.
func parseAllowFrom(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []string
	for _, e := range strings.Split(raw, ",") {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, strings.ToLower(e))
		}
	}
	return out
}

// isAllowed reports whether an email may use the bot.
// Empty allowlist permits everyone (a startup warning was already logged).
func (p *Platform) isAllowed(email string) bool {
	if len(p.allowFrom) == 0 {
		return true
	}
	email = strings.ToLower(strings.TrimSpace(email))
	for _, a := range p.allowFrom {
		if a == "*" || a == email {
			return true
		}
	}
	return false
}

var sparkMentionRe = regexp.MustCompile(`(?s)<spark-mention[^>]*>.*?</spark-mention>`)

// stripMention removes Webex <spark-mention> tags and trims the result.
func stripMention(text string) string {
	return strings.TrimSpace(sparkMentionRe.ReplaceAllString(text, ""))
}

// isMentioned reports whether the bot's selfID appears in mentionedPeople.
func (p *Platform) isMentioned(m *message) bool {
	self := p.self()
	for _, id := range m.MentionedPeople {
		if id == self {
			return true
		}
	}
	return false
}

// shouldProcess applies the gate: allowlist + group-mention requirement.
func (p *Platform) shouldProcess(m *message) bool {
	if !p.isAllowed(m.PersonEmail) {
		return false
	}
	if m.RoomType == "group" && !p.isMentioned(m) {
		return false
	}
	return true
}

// buildMessage converts a fetched Webex message into a core.Message,
// downloading any attachments and stripping group @mentions.
func (p *Platform) buildMessage(ctx context.Context, m *message) *core.Message {
	content := m.Text
	if m.RoomType == "group" {
		content = stripMention(content)
	}

	cm := &core.Message{
		SessionKey: fmt.Sprintf("webex:%s:%s", m.RoomID, m.PersonID),
		Platform:   "webex",
		MessageID:  m.ID,
		ChannelID:  m.RoomID,
		ChannelKey: m.RoomID,
		UserID:     m.PersonEmail,
		// Webex message API exposes no display name; use email. A /people lookup could enrich this later.
		UserName: m.PersonEmail,
		Content:    content,
		ReplyCtx:   replyContext{roomID: m.RoomID, messageID: m.ID, personID: m.PersonID},
	}

	for _, url := range m.Files {
		f, err := p.client.DownloadFile(ctx, url)
		if err != nil {
			slog.Error("webex: download file failed", "error", err)
			continue
		}
		if strings.HasPrefix(f.MimeType, "image/") {
			cm.Images = append(cm.Images, core.ImageAttachment{
				MimeType: f.MimeType, Data: f.Data, FileName: f.FileName,
			})
		} else {
			cm.Files = append(cm.Files, core.FileAttachment{
				MimeType: f.MimeType, Data: f.Data, FileName: f.FileName,
			})
		}
	}
	return cm
}

func (p *Platform) messageHandler() core.MessageHandler {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.handler
}

func (p *Platform) isStopping() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.stopping
}

const (
	initialBackoff   = time.Second
	maxBackoff       = 30 * time.Second
	stableConnWindow = 10 * time.Second
)

// Start fetches the bot identity, registers a device, and launches the
// reconnecting WebSocket read loop in the background.
func (p *Platform) Start(handler core.MessageHandler) error {
	p.mu.Lock()
	if p.stopping {
		p.mu.Unlock()
		return fmt.Errorf("webex: platform stopped")
	}
	p.handler = handler
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.mu.Unlock()

	me, err := p.client.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("webex: getMe: %w", err)
	}
	p.mu.Lock()
	p.selfID = me.ID
	if len(me.Emails) > 0 {
		p.selfEmail = me.Emails[0]
	}
	p.mu.Unlock()
	slog.Info("webex: authenticated", "bot", me.DisplayName)

	go p.connectLoop(ctx)
	return nil
}

// Stop cancels the read loop and deletes the registered device.
func (p *Platform) Stop() error {
	p.mu.Lock()
	p.stopping = true
	cancel := p.cancel
	deviceURL := p.deviceURL
	p.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if deviceURL != "" {
		ctx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		if err := p.client.DeleteDevice(ctx, deviceURL); err != nil {
			slog.Warn("webex: delete device failed", "error", err)
		}
	}
	return nil
}

// SetLifecycleHandler implements core.AsyncRecoverablePlatform.
func (p *Platform) SetLifecycleHandler(h core.PlatformLifecycleHandler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lifecycleHandler = h
}

// connectLoop registers a device, opens the WebSocket, and reconnects with
// exponential backoff until the context is cancelled.
func (p *Platform) connectLoop(ctx context.Context) {
	backoff := initialBackoff
	for {
		if ctx.Err() != nil || p.isStopping() {
			return
		}
		started := time.Now()
		err := p.runConnection(ctx)
		if ctx.Err() != nil || p.isStopping() {
			return
		}
		if err != nil {
			if errors.Is(err, errUnauthorized) {
				slog.Error("webex: authentication failed, not retrying", "error", core.RedactToken(err.Error(), p.token))
				if h := p.lifecycle(); h != nil {
					h.OnPlatformUnavailable(p, err)
				}
				return
			}
			slog.Warn("webex: connection ended", "error", core.RedactToken(err.Error(), p.token), "backoff", backoff)
			if h := p.lifecycle(); h != nil {
				h.OnPlatformUnavailable(p, err)
			}
		}
		if time.Since(started) >= stableConnWindow {
			backoff = initialBackoff
		} else if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

func (p *Platform) lifecycle() core.PlatformLifecycleHandler {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.lifecycleHandler
}

// runConnection registers a device, dials the WebSocket, and reads until the
// connection drops or the context is cancelled.
func (p *Platform) runConnection(ctx context.Context) error {
	dev, err := p.client.CreateDevice(ctx)
	if err != nil {
		return fmt.Errorf("create device: %w", err)
	}
	p.mu.Lock()
	prevDevice := p.deviceURL
	p.deviceURL = dev.URL
	p.mu.Unlock()
	if prevDevice != "" && prevDevice != dev.URL {
		if err := p.client.DeleteDevice(ctx, prevDevice); err != nil {
			slog.Debug("webex: delete stale device failed", "error", err)
		}
	}

	header := map[string][]string{"Authorization": {"Bearer " + p.token}}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, dev.WebSocketURL, header)
	if err != nil {
		return fmt.Errorf("dial websocket: %s", core.RedactToken(err.Error(), p.token))
	}
	defer func() { _ = conn.Close() }()

	slog.Info("webex: websocket connected")
	if h := p.lifecycle(); h != nil {
		h.OnPlatformReady(p)
	}

	connClosed := make(chan struct{})
	defer close(connClosed)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-connClosed:
		}
	}()

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read websocket: %w", err)
		}
		p.handleFrame(ctx, data)
	}
}

// handleFrame parses one Mercury WebSocket frame and dispatches qualifying
// messages. The frame body is encrypted, so we fetch the decrypted message
// via REST using the activity ID.
func (p *Platform) handleFrame(ctx context.Context, data []byte) {
	var ev wsEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		slog.Debug("webex: non-JSON frame", "error", err)
		return
	}
	// "post" = text message, "share" = file/image upload. Other verbs (e.g.
	// "update" for malware-scan completion, "delete") are re-notifications we
	// must ignore to avoid double-processing.
	if ev.Data.EventType != "conversation.activity" {
		return
	}
	if ev.Data.Activity.Verb != "post" && ev.Data.Activity.Verb != "share" {
		return
	}
	// Skip our own messages (actor email matches the bot).
	if self := p.selfEmailAddr(); self != "" && strings.EqualFold(ev.Data.Activity.Actor.EmailAddress, self) {
		return
	}
	msgID := activityIDToMessageID(ev.Data.Activity.ID)
	if msgID == "" {
		return
	}
	m, err := p.client.GetMessage(ctx, msgID)
	if err != nil {
		slog.Error("webex: fetch message failed", "error", err)
		return
	}
	if !p.shouldProcess(m) {
		slog.Debug("webex: message gated out", "room_type", m.RoomType, "from", m.PersonEmail)
		return
	}
	handler := p.messageHandler()
	if handler == nil {
		return
	}
	handler(p, p.buildMessage(ctx, m))
}

// activityIDToMessageID converts a Mercury activity UUID into the public Webex
// message ID (base64 of the ciscospark://us/MESSAGE/{uuid} URI) accepted by
// GET /v1/messages/{id}.
func activityIDToMessageID(activityID string) string {
	if activityID == "" {
		return ""
	}
	return base64.StdEncoding.EncodeToString([]byte("ciscospark://us/MESSAGE/" + activityID))
}
