package matrix

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"
)

func init() {
	core.RegisterPlatform("matrix", New)
}

type replyContext struct {
	roomID     id.RoomID
	messageID  id.EventID
	threadID   id.EventID // m.thread root this message belongs to ("" if none)
	lastID     id.EventID // message being answered (thread fallback quote target)
	sessionKey string     // agent session this outgoing message belongs to (event→session mapping)
}

// eventSessionMap remembers which agent session an event belongs to, so that
// replies to a specific message (m.in_reply_to) or thread messages
// (rel_type m.thread) can be routed back into the same conversation.
// In-memory only: after a restart, replies fall back to the default
// room/sender session until new messages re-seed the mapping.
type eventSessionMap struct {
	mu      sync.Mutex
	entries map[string]eventSessionEntry
}

type eventSessionEntry struct {
	session string
	fromBot bool
	at      time.Time
}

const (
	eventSessionTTL      = 7 * 24 * time.Hour
	eventSessionMaxItems = 20000
)

func newEventSessionMap() *eventSessionMap {
	return &eventSessionMap{entries: make(map[string]eventSessionEntry)}
}

func (m *eventSessionMap) remember(eventID, session string) {
	m.rememberWithOrigin(eventID, session, false)
}

// rememberWithOrigin records the event→session mapping; fromBot marks events
// sent by the bot itself (used to detect "replying to the bot" addressing).
func (m *eventSessionMap) rememberWithOrigin(eventID, session string, fromBot bool) {
	if m == nil || eventID == "" || session == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	if len(m.entries) >= eventSessionMaxItems {
		for k, e := range m.entries {
			if now.Sub(e.at) > eventSessionTTL {
				delete(m.entries, k)
			}
		}
		for len(m.entries) >= eventSessionMaxItems { // hard cap: drop arbitrary entries
			for k := range m.entries {
				delete(m.entries, k)
				break
			}
		}
	}
	m.entries[eventID] = eventSessionEntry{session: session, fromBot: fromBot, at: now}
}

func (m *eventSessionMap) lookup(eventID string) (string, bool) {
	if m == nil || eventID == "" {
		return "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[eventID]
	if !ok {
		return "", false
	}
	if time.Since(e.at) > eventSessionTTL {
		delete(m.entries, eventID)
		return "", false
	}
	return e.session, true
}

// isFromBot reports whether the event was sent by the bot (and is still known).
func (m *eventSessionMap) isFromBot(eventID string) bool {
	if m == nil || eventID == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[eventID]
	if !ok || time.Since(e.at) > eventSessionTTL {
		return false
	}
	return e.fromBot
}

// isKnown reports whether we have any mapping for the event (bot or user).
func (m *eventSessionMap) isKnown(eventID string) bool {
	if m == nil || eventID == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[eventID]
	if !ok {
		return false
	}
	return time.Since(e.at) <= eventSessionTTL
}

type Platform struct {
	homeserver            string
	accessToken           string
	userID                string
	allowFrom             string
	shareSessionInChannel bool
	threadSessions        bool
	renderMermaidDiagrams bool
	krokiURL              string
	groupReplyAll         bool
	autoJoin              bool
	autoVerify            bool
	proxyURL              string

	mu                   sync.RWMutex
	client               *mautrix.Client
	selfUserID           id.UserID
	handler              core.MessageHandler
	lifecycleHandler     core.PlatformLifecycleHandler
	cancel               context.CancelFunc
	stopping             bool
	generation           uint64
	everConnected        bool
	unavailableNotified  bool
	dedup                core.MessageDedup
	httpClient           *http.Client
	eventSessions        *eventSessionMap
	cryptoHelper         any //nolint:unused // *cryptohelper.CryptoHelper when built with goolm tag
	crossSigningPassword string
}

const (
	initialBackoff = 2 * time.Second
	maxBackoff     = 60 * time.Second
	stableWindow   = 10 * time.Second
)

func New(opts map[string]any) (core.Platform, error) {
	homeserver, _ := opts["homeserver"].(string)
	if homeserver == "" {
		return nil, fmt.Errorf("matrix: homeserver is required")
	}
	accessToken, _ := opts["access_token"].(string)
	if accessToken == "" {
		return nil, fmt.Errorf("matrix: access_token is required")
	}
	userID, _ := opts["user_id"].(string)
	allowFrom, _ := opts["allow_from"].(string)
	core.CheckAllowFrom("matrix", allowFrom)

	groupReplyAll, _ := opts["group_reply_all"].(bool)
	shareSession, _ := opts["share_session_in_channel"].(bool)
	threadSessions, _ := opts["thread_sessions"].(bool)
	renderMermaidDiagrams, _ := opts["render_mermaid"].(bool)
	krokiURL, _ := opts["kroki_url"].(string)
	if krokiURL == "" {
		krokiURL = "https://kroki.io"
	}
	autoJoin, _ := opts["auto_join"].(bool)
	if !autoJoin {
		_, hasKey := opts["auto_join"]
		if !hasKey {
			autoJoin = true // default true
		}
	}
	autoVerify, _ := opts["auto_verify"].(bool)
	if !autoVerify {
		_, hasKey := opts["auto_verify"]
		if !hasKey {
			autoVerify = true // default true
		}
	}
	proxyURL, _ := opts["proxy"].(string)
	crossSigningPassword, _ := opts["cross_signing_password"].(string)
	if env := os.Getenv("MATRIX_CROSS_SIGNING_PASSWORD"); env != "" {
		crossSigningPassword = env
	}

	slog.Info("matrix: platform options",
		"thread_sessions", threadSessions,
		"share_session_in_channel", shareSession,
		"group_reply_all", groupReplyAll,
		"render_mermaid", renderMermaidDiagrams)

	httpClient := &http.Client{Timeout: 120 * time.Second}
	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("matrix: invalid proxy URL %q: %w", proxyURL, err)
		}
		httpClient.Transport = &http.Transport{Proxy: http.ProxyURL(u)}
		slog.Info("matrix: using proxy", "proxy", u.Host)
	}

	return &Platform{
		homeserver:            homeserver,
		accessToken:           accessToken,
		userID:                userID,
		allowFrom:             allowFrom,
		groupReplyAll:         groupReplyAll,
		shareSessionInChannel: shareSession,
		threadSessions:        threadSessions,
		renderMermaidDiagrams: renderMermaidDiagrams,
		krokiURL:              strings.TrimRight(krokiURL, "/"),
		autoJoin:              autoJoin,
		proxyURL:              proxyURL,
		autoVerify:            autoVerify,
		crossSigningPassword:  crossSigningPassword,
		httpClient:            httpClient,
		dedup:                 core.MessageDedup{},
		eventSessions:         newEventSessionMap(),
	}, nil
}

func (p *Platform) Name() string { return "matrix" }

func (p *Platform) Start(handler core.MessageHandler) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.stopping {
		return fmt.Errorf("matrix: platform stopped")
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.handler = handler
	p.cancel = cancel

	go p.connectLoop(ctx)
	return nil
}

func (p *Platform) SetLifecycleHandler(h core.PlatformLifecycleHandler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lifecycleHandler = h
}

func (p *Platform) connectLoop(ctx context.Context) {
	backoff := initialBackoff

	for {
		if ctx.Err() != nil || p.isStopping() {
			return
		}

		startedAt := time.Now()
		err := p.runConnection(ctx)
		if ctx.Err() != nil || p.isStopping() {
			return
		}

		wait := backoff
		if time.Since(startedAt) >= stableWindow {
			wait = initialBackoff
			backoff = initialBackoff
		} else if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}

		if err != nil {
			slog.Warn("matrix: connection error, retrying", "error", core.RedactToken(err.Error(), p.accessToken), "backoff", wait)
			p.notifyUnavailable(err)
		}

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (p *Platform) runConnection(ctx context.Context) error {
	client, err := mautrix.NewClient(p.homeserver, "", p.accessToken)
	if err != nil {
		return fmt.Errorf("matrix: create client: %w", err)
	}
	client.Client = p.httpClient

	// Always call Whoami to validate token and get device ID (needed for E2EE)
	selfUserID := id.UserID(p.userID)
	var deviceID id.DeviceID
	resp, err := client.Whoami(ctx)
	if err != nil {
		return fmt.Errorf("matrix: whoami: %w", err)
	}
	if selfUserID == "" {
		selfUserID = resp.UserID
	}
	deviceID = resp.DeviceID
	client.UserID = selfUserID
	client.DeviceID = deviceID

	if ctx.Err() != nil || p.isStopping() {
		return nil
	}

	gen, ok := p.publishClient(client, selfUserID)
	if !ok {
		return nil
	}

	// Initialize E2EE crypto helper
	p.initE2EE(ctx, client)

	slog.Info("matrix: connected", "user_id", selfUserID)
	p.emitReady(gen)

	// Register event handlers.
	// Note: EventEncrypted is handled by cryptohelper which decrypts and
	// re-dispatches as EventMessage, so we only need EventMessage here.
	syncer := client.Syncer.(*mautrix.DefaultSyncer)
	syncer.OnEventType(event.EventMessage, func(ctx context.Context, evt *event.Event) {
		p.handleMessage(ctx, evt)
	})
	syncer.OnEventType(event.StateMember, func(ctx context.Context, evt *event.Event) {
		p.handleMemberState(ctx, evt)
	})

	// Blocks until ctx cancelled or fatal error
	err = client.SyncWithContext(ctx)

	// Cleanup
	p.closeCryptoHelper()
	p.clearClient(gen, client)
	if ctx.Err() != nil {
		return nil
	}
	return fmt.Errorf("matrix: sync ended: %w", err)
}

func (p *Platform) handleMessage(ctx context.Context, evt *event.Event) {
	content, ok := evt.Content.Parsed.(*event.MessageEventContent)
	if !ok || content == nil {
		return
	}

	// Skip own messages
	selfID := p.getSelfUserID()
	if evt.Sender == selfID {
		return
	}

	// Dedup
	if p.dedup.IsDuplicate(evt.ID.String()) {
		return
	}

	// Old message check
	msgTime := time.UnixMilli(evt.Timestamp)
	if core.IsOldMessage(msgTime) {
		slog.Debug("matrix: ignoring old message", "event_id", evt.ID, "time", msgTime)
		return
	}

	// Allow-from check
	senderStr := evt.Sender.String()
	if !core.AllowList(p.allowFrom, senderStr) {
		slog.Debug("matrix: message from unauthorized user", "user", senderStr)
		return
	}

	roomID := evt.RoomID
	isDM := p.isDMRoom(ctx, roomID)

	// Group addressing gate: in rooms with more participants than the bot and
	// one user, stay silent unless directly addressed — via @mention, or by
	// quoting (m.in_reply_to) a message that belongs to a bot conversation.
	if !isDM && !p.groupReplyAll {
		target := content.RelatesTo.GetNonFallbackReplyTo()
		if target == "" {
			target = content.RelatesTo.GetThreadParent()
		}
		addressedViaQuote := target != "" &&
			(p.eventSessions.isFromBot(target.String()) || p.eventSessions.isKnown(target.String()))
		if !p.isDirectedAtBot(content, selfID) && !p.mentionsWakeWord(content) && !addressedViaQuote {
			slog.Debug("matrix: ignoring group message not directed at bot", "event_id", evt.ID)
			return
		}
	}

	userName := displayName(evt.Sender)
	// thread_sessions: a top-level message (groups and DMs alike) becomes the
	// root of a new conversation thread, so each conversation keeps its own
	// isolated context/session.
	var rootOverride id.EventID
	if p.threadSessions &&
		content.RelatesTo.GetThreadParent() == "" &&
		content.RelatesTo.GetNonFallbackReplyTo() == "" &&
		!p.eventSessions.isKnown(evt.ID.String()) {
		// Only a relation-free event can start a thread: homeservers reject
		// m.thread rooted at an event that already carries a relation
		// (edits/m.replace, reactions, ...).
		if canStartThread(content.RelatesTo) {
			rootOverride = evt.ID
		}
	}
	sessionKey := p.routeSessionKey(roomID, evt.Sender, content, rootOverride)
	if rootOverride != "" {
		slog.Info("matrix: thread session started",
			"root", rootOverride.String(), "session_key", sessionKey)
	}
	channelKey := roomID.String()

	rctx := replyContext{
		roomID:     roomID,
		messageID:  evt.ID,
		threadID:   content.RelatesTo.GetThreadParent(),
		lastID:     evt.ID,
		sessionKey: sessionKey,
	}
	if rootOverride != "" {
		rctx.threadID = rootOverride
	}
	p.eventSessions.remember(evt.ID.String(), sessionKey)

	// Handle different message types
	msgType := content.MsgType
	switch msgType {
	case event.MsgText, event.MsgNotice, event.MsgEmote:
		text := stripBotMention(content.Body, selfID)
		p.dispatch(&core.Message{
			SessionKey: sessionKey, Platform: "matrix",
			UserID: senderStr, UserName: userName,
			Content: text, MessageID: evt.ID.String(),
			ChannelKey: channelKey, ReplyCtx: rctx,
		})
	case event.MsgImage:
		img, err := p.downloadMedia(ctx, content)
		if err != nil {
			slog.Error("matrix: download image failed", "error", err)
			return
		}
		caption := stripBotMention(content.Body, selfID)
		p.dispatch(&core.Message{
			SessionKey: sessionKey, Platform: "matrix",
			UserID: senderStr, UserName: userName,
			Content: caption, MessageID: evt.ID.String(),
			ChannelKey: channelKey, ReplyCtx: rctx,
			Images: []core.ImageAttachment{*img},
		})
	case event.MsgFile:
		file, err := p.downloadFileMedia(ctx, content)
		if err != nil {
			slog.Error("matrix: download file failed", "error", err)
			return
		}
		caption := stripBotMention(content.Body, selfID)
		p.dispatch(&core.Message{
			SessionKey: sessionKey, Platform: "matrix",
			UserID: senderStr, UserName: userName,
			Content: caption, MessageID: evt.ID.String(),
			ChannelKey: channelKey, ReplyCtx: rctx,
			Files: []core.FileAttachment{*file},
		})
	case event.MsgAudio:
		audio, err := p.downloadAudioMedia(ctx, content)
		if err != nil {
			slog.Error("matrix: download audio failed", "error", err)
			return
		}
		p.dispatch(&core.Message{
			SessionKey: sessionKey, Platform: "matrix",
			UserID: senderStr, UserName: userName,
			MessageID:  evt.ID.String(),
			ChannelKey: channelKey, ReplyCtx: rctx,
			Audio: audio,
		})
	default:
		slog.Debug("matrix: ignoring unsupported message type", "type", msgType)
	}
}

func (p *Platform) handleMemberState(ctx context.Context, evt *event.Event) {
	if !p.autoJoin {
		return
	}
	content, ok := evt.Content.Parsed.(*event.MemberEventContent)
	if !ok {
		return
	}
	selfID := p.getSelfUserID()
	if content.Membership == event.MembershipInvite && evt.StateKey != nil && id.UserID(*evt.StateKey) == selfID {
		client := p.getClient()
		if client == nil {
			return
		}
		_, err := client.JoinRoomByID(ctx, evt.RoomID)
		if err != nil {
			slog.Error("matrix: auto-join failed", "room", evt.RoomID, "error", err)
		} else {
			slog.Info("matrix: auto-joined room", "room", evt.RoomID)
		}
	}
}

func (p *Platform) dispatch(msg *core.Message) {
	handler := p.getHandler()
	if handler == nil {
		return
	}
	handler(p, msg)
}

// sendRoomEvent sends an event to a room, encrypting it if E2EE is available and the room is encrypted.
// sendRoomEvent sends an event to a room, encrypting it if E2EE is available and the room is encrypted.
// It returns the event ID of the sent event ("" when unknown).
func (p *Platform) sendRoomEvent(ctx context.Context, roomID id.RoomID, evtType event.Type, content any) (id.EventID, error) {
	client := p.getClient()
	if client == nil {
		return "", fmt.Errorf("matrix: not connected")
	}

	// Try E2EE path first (only available when built with goolm tag)
	if handled, evtID, err := p.tryEncryptAndSend(ctx, client, roomID, evtType, content); handled {
		return evtID, err
	}

	resp, err := client.SendMessageEvent(ctx, roomID, evtType, content)
	if err != nil {
		// Safety net: some roots are rejected ("Cannot start threads from an
		// event with a relation"). Retry once without the thread relation so
		// the answer is not lost; it still lands in the room.
		if strings.Contains(err.Error(), "relation") && evtType == event.EventMessage {
			if mc, ok := content.(*event.MessageEventContent); ok {
				saved := mc.RelatesTo
				mc.RelatesTo = nil
				resp2, err2 := client.SendMessageEvent(ctx, roomID, evtType, content)
				mc.RelatesTo = saved
				if err2 == nil {
					slog.Warn("matrix: resent without thread relation after rejection", "room", roomID.String())
					return resp2.EventID, nil
				}
			}
		}
		return "", fmt.Errorf("matrix: send: %w", err)
	}
	return resp.EventID, nil
}

func (p *Platform) Reply(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("matrix: invalid reply context type %T", rctx)
	}

	parsed := format.RenderMarkdown(content, true, false)
	parsed.Body = content
	switch {
	case rc.threadID != "":
		rel := &event.RelatesTo{}
		rel.SetThread(rc.threadID, rc.messageID)
		parsed.RelatesTo = rel
	case content != "":
		parsed.RelatesTo = &event.RelatesTo{}
		parsed.RelatesTo.SetReplyTo(rc.messageID)
	}

	evtID, err := p.sendRoomEvent(ctx, rc.roomID, event.EventMessage, &parsed)
	p.rememberSent(rc, evtID)
	return err
}

func (p *Platform) Send(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("matrix: invalid reply context type %T", rctx)
	}

	if imgs := func() []core.ImageAttachment { c, i := p.renderMermaid(content); content = c; return i }(); len(imgs) > 0 {
		defer func() {
			for _, img := range imgs {
				_ = p.SendImage(ctx, rc, img)
			}
		}()
	}
	parsed := format.RenderMarkdown(content, true, false)
	parsed.Body = content
	applyThreadRelation(&parsed.RelatesTo, rc)

	evtID, err := p.sendRoomEvent(ctx, rc.roomID, event.EventMessage, &parsed)
	p.rememberSent(rc, evtID)
	return err
}

func (p *Platform) Stop() error {
	p.mu.Lock()
	if p.stopping {
		p.mu.Unlock()
		return nil
	}
	p.stopping = true
	cancel := p.cancel
	p.cancel = nil
	p.client = nil
	p.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	return nil
}

// --- Optional interfaces ---

func (p *Platform) SendImage(ctx context.Context, rctx any, img core.ImageAttachment) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("matrix: invalid reply context type %T", rctx)
	}
	client := p.getClient()
	if client == nil {
		return fmt.Errorf("matrix: not connected")
	}

	mime := img.MimeType
	if mime == "" {
		mime = "image/png"
	}
	name := img.FileName
	if name == "" {
		name = "image"
	}

	uri, err := client.UploadMedia(ctx, mautrix.ReqUploadMedia{
		ContentBytes: img.Data,
		ContentType:  mime,
		FileName:     name,
	})
	if err != nil {
		return fmt.Errorf("matrix: upload image: %w", err)
	}

	content := &event.MessageEventContent{
		MsgType: event.MsgImage,
		Body:    name,
		Info: &event.FileInfo{
			MimeType: mime,
			Size:     len(img.Data),
		},
	}
	if !uri.ContentURI.IsEmpty() {
		content.URL = uri.ContentURI.CUString()
	} else {
		content.File = &event.EncryptedFileInfo{
			URL: uri.ContentURI.CUString(),
		}
	}

	applyThreadRelation(&content.RelatesTo, rc)
	evtID, err := p.sendRoomEvent(ctx, rc.roomID, event.EventMessage, content)
	p.rememberSent(rc, evtID)
	return err
}

func (p *Platform) SendFile(ctx context.Context, rctx any, file core.FileAttachment) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("matrix: invalid reply context type %T", rctx)
	}
	client := p.getClient()
	if client == nil {
		return fmt.Errorf("matrix: not connected")
	}

	mime := file.MimeType
	if mime == "" {
		mime = "application/octet-stream"
	}
	name := file.FileName
	if name == "" {
		name = "attachment"
	}

	uri, err := client.UploadMedia(ctx, mautrix.ReqUploadMedia{
		ContentBytes: file.Data,
		ContentType:  mime,
		FileName:     name,
	})
	if err != nil {
		return fmt.Errorf("matrix: upload file: %w", err)
	}

	content := &event.MessageEventContent{
		MsgType: event.MsgFile,
		Body:    name,
		Info: &event.FileInfo{
			MimeType: mime,
			Size:     len(file.Data),
		},
	}
	if !uri.ContentURI.IsEmpty() {
		content.URL = uri.ContentURI.CUString()
	} else {
		content.File = &event.EncryptedFileInfo{
			URL: uri.ContentURI.CUString(),
		}
	}

	applyThreadRelation(&content.RelatesTo, rc)
	evtID, err := p.sendRoomEvent(ctx, rc.roomID, event.EventMessage, content)
	p.rememberSent(rc, evtID)
	return err
}

func (p *Platform) StartTyping(ctx context.Context, rctx any) (stop func()) {
	rc, ok := rctx.(replyContext)
	if !ok {
		return func() {}
	}

	client := p.getClient()
	if client == nil {
		return func() {}
	}

	// Set typing with 30s timeout, refresh every 25s
	_, _ = client.UserTyping(ctx, rc.roomID, true, 30*time.Second)

	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				c := p.getClient()
				if c != nil {
					_, _ = c.UserTyping(context.Background(), rc.roomID, false, 0)
				}
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				c := p.getClient()
				if c == nil {
					return
				}
				_, _ = c.UserTyping(ctx, rc.roomID, true, 30*time.Second)
			}
		}
	}()

	return func() { close(done) }
}

func (p *Platform) UpdateMessage(ctx context.Context, previewHandle any, content string) error {
	rc, ok := previewHandle.(replyContext)
	if !ok {
		return fmt.Errorf("matrix: invalid preview handle type %T", previewHandle)
	}

	parsed := format.RenderMarkdown(content, true, false)
	parsed.Body = content

	// Copy the new content for m.replace relation
	newContent := parsed
	newContent.Mentions = nil

	if imgs := func() []core.ImageAttachment { c, i := p.renderMermaid(content); content = c; return i }(); len(imgs) > 0 {
		defer func() {
			for _, img := range imgs {
				_ = p.SendImage(ctx, rc, img)
			}
		}()
	}
	parsed.NewContent = &newContent
	parsed.RelatesTo = &event.RelatesTo{
		Type:    event.RelReplace,
		EventID: rc.messageID,
	}
	parsed.Body = "* " + content

	evtID, err := p.sendRoomEvent(ctx, rc.roomID, event.EventMessage, &parsed)
	p.rememberSent(rc, evtID)
	return err
}

func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	// Formats:
	//   matrix:{roomID}:{userID}   - per-user session
	//   matrix:{roomID}            - shared session
	// Room IDs contain a colon (!localpart:server), so we can't simply split on colons.
	if !strings.HasPrefix(sessionKey, "matrix:") {
		return nil, fmt.Errorf("matrix: invalid session key %q", sessionKey)
	}
	rest := sessionKey[len("matrix:"):]
	if rest == "" {
		return nil, fmt.Errorf("matrix: invalid session key %q", sessionKey)
	}

	// Find boundary between room ID and optional user ID.
	// User IDs start with @, so ":@" only appears at the roomID:userID boundary.
	var roomIDStr string
	if idx := strings.Index(rest, ":@"); idx >= 0 {
		roomIDStr = rest[:idx]
	} else {
		roomIDStr = rest
	}

	// Optional thread segment: matrix:{room}:t-{eventID}(:{userID})
	var threadID id.EventID
	if tIdx := strings.Index(roomIDStr, ":t-"); tIdx >= 0 {
		threadID = id.EventID(roomIDStr[tIdx+3:])
		roomIDStr = roomIDStr[:tIdx]
	}

	if !strings.HasPrefix(roomIDStr, "!") {
		return nil, fmt.Errorf("matrix: invalid room ID in %q", sessionKey)
	}
	return replyContext{roomID: id.RoomID(roomIDStr), threadID: threadID}, nil
}

// --- Internal helpers ---

// routeSessionKey resolves the agent session for an incoming message:
//  1. exact mapping when the message quotes/continues a known event —
//     replying to any previous bot/user message resumes that conversation;
//  2. a per-thread fork when the message continues an unknown thread root;
//  3. otherwise the default room/sender key.
func (p *Platform) routeSessionKey(roomID id.RoomID, sender id.UserID, content *event.MessageEventContent, rootOverride id.EventID) string {
	rel := content.RelatesTo
	threadRoot := rel.GetThreadParent()
	replyTo := rel.GetNonFallbackReplyTo()
	if rootOverride != "" {
		threadRoot = rootOverride
		replyTo = ""
	}

	routeID := threadRoot
	if routeID == "" {
		routeID = replyTo
	}
	if routeID != "" {
		if sk, ok := p.eventSessions.lookup(routeID.String()); ok {
			return sk
		}
	}
	if threadRoot != "" {
		return p.buildThreadSessionKey(roomID, sender, threadRoot)
	}
	return p.buildSessionKey(roomID, sender)
}

// canStartThread reports whether an event is free of relations and therefore
// allowed by homeservers to become the root of a new m.thread conversation.
func canStartThread(rel *event.RelatesTo) bool {
	return rel == nil || (rel.Type == "" && rel.InReplyTo == nil && rel.Key == "")
}

func (p *Platform) buildSessionKey(roomID id.RoomID, sender id.UserID) string {
	if p.shareSessionInChannel {
		return fmt.Sprintf("matrix:%s", roomID)
	}
	return fmt.Sprintf("matrix:%s:%s", roomID, sender)
}

func (p *Platform) buildThreadSessionKey(roomID id.RoomID, sender id.UserID, root id.EventID) string {
	if p.shareSessionInChannel {
		return fmt.Sprintf("matrix:%s:t-%s", roomID, root)
	}
	return fmt.Sprintf("matrix:%s:t-%s:%s", roomID, root, sender)
}

// applyThreadRelation attaches an m.thread relation so outgoing messages stay
// inside the conversation thread, with a fallback quote pointing at the
// message being answered. No-op when not in a thread.
func applyThreadRelation(rel **event.RelatesTo, rc replyContext) {
	if rc.threadID == "" {
		return
	}
	r := &event.RelatesTo{}
	r.SetThread(rc.threadID, rc.lastID)
	*rel = r
}

// rememberSent records the event→session mapping for outgoing messages so
// later replies to them resume the same session.
func (p *Platform) rememberSent(rc replyContext, evtID id.EventID) {
	if evtID != "" && rc.sessionKey != "" {
		p.eventSessions.rememberWithOrigin(evtID.String(), rc.sessionKey, true)
	}
}

func (p *Platform) isDMRoom(ctx context.Context, roomID id.RoomID) bool {
	client := p.getClient()
	if client == nil {
		return false
	}
	members, err := client.Members(ctx, roomID)
	if err != nil {
		slog.Debug("matrix: failed to get room members, assuming group", "error", err)
		return false
	}
	return len(members.Chunk) <= 2
}

// mentionsWakeWord reports whether the message text addresses the bot by its
// nickname ("Вася"/"vasya", case-insensitive) without a formal @mention.
func (p *Platform) mentionsWakeWord(content *event.MessageEventContent) bool {
	if content == nil {
		return false
	}
	t := strings.ToLower(content.Body)
	return strings.Contains(t, "вася") || strings.Contains(t, "vasya")
}

func (p *Platform) isDirectedAtBot(content *event.MessageEventContent, selfID id.UserID) bool {
	// Check formatted body for matrix.to link
	if content.FormattedBody != "" {
		mention := fmt.Sprintf("https://matrix.to/#/%s", selfID)
		if strings.Contains(content.FormattedBody, mention) {
			return true
		}
	}
	// Check plain body for @user:server mention
	if strings.Contains(content.Body, selfID.String()) {
		return true
	}
	return false
}

func (p *Platform) downloadMediaContent(ctx context.Context, contentURL id.ContentURIString) ([]byte, error) {
	client := p.getClient()
	if client == nil {
		return nil, fmt.Errorf("not connected")
	}
	parsed, err := contentURL.Parse()
	if err != nil {
		return nil, fmt.Errorf("parse content URI: %w", err)
	}
	resp, err := client.Download(ctx, parsed)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	return io.ReadAll(resp.Body)
}

func (p *Platform) downloadMedia(ctx context.Context, content *event.MessageEventContent) (*core.ImageAttachment, error) {
	data, err := p.downloadMediaContent(ctx, content.URL)
	if err != nil {
		return nil, err
	}
	mime := ""
	if content.Info != nil {
		mime = content.Info.MimeType
	}
	if mime == "" {
		mime = "image/png"
	}
	name := content.Body
	return &core.ImageAttachment{
		MimeType: mime,
		Data:     data,
		FileName: name,
	}, nil
}

func (p *Platform) downloadFileMedia(ctx context.Context, content *event.MessageEventContent) (*core.FileAttachment, error) {
	data, err := p.downloadMediaContent(ctx, content.URL)
	if err != nil {
		return nil, err
	}
	mime := ""
	if content.Info != nil {
		mime = content.Info.MimeType
	}
	if mime == "" {
		mime = "application/octet-stream"
	}
	return &core.FileAttachment{
		MimeType: mime,
		Data:     data,
		FileName: content.Body,
	}, nil
}

func (p *Platform) downloadAudioMedia(ctx context.Context, content *event.MessageEventContent) (*core.AudioAttachment, error) {
	data, err := p.downloadMediaContent(ctx, content.URL)
	if err != nil {
		return nil, err
	}
	mime := ""
	audiFmt := ""
	duration := 0
	if content.Info != nil {
		mime = content.Info.MimeType
		duration = content.Info.Duration / 1000
	}
	if mime == "" {
		mime = "audio/ogg"
	}
	if parts := strings.SplitN(mime, "/", 2); len(parts) == 2 {
		audiFmt = parts[1]
	}
	if audiFmt == "" {
		audiFmt = "ogg"
	}
	return &core.AudioAttachment{
		MimeType: mime,
		Data:     data,
		Format:   audiFmt,
		Duration: duration,
	}, nil
}

func stripBotMention(text string, selfID id.UserID) string {
	if selfID == "" {
		return text
	}
	// Strip matrix.to links first (longer pattern), then plain user ID
	text = strings.ReplaceAll(text, fmt.Sprintf("https://matrix.to/#/%s", selfID), "")
	text = strings.ReplaceAll(text, selfID.String(), "")
	return strings.TrimSpace(text)
}

func displayName(userID id.UserID) string {
	// Use the localpart as a fallback display name
	localpart, _, _ := strings.Cut(userID.String(), ":")
	return strings.TrimPrefix(localpart, "@")
}

// --- Concurrency-safe accessors ---

func (p *Platform) isStopping() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.stopping
}

func (p *Platform) getClient() *mautrix.Client {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.client
}

func (p *Platform) getSelfUserID() id.UserID {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.selfUserID
}

func (p *Platform) getHandler() core.MessageHandler {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.handler
}

func (p *Platform) publishClient(client *mautrix.Client, selfUserID id.UserID) (uint64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopping {
		return 0, false
	}
	p.generation++
	p.client = client
	p.selfUserID = selfUserID
	return p.generation, true
}

func (p *Platform) emitReady(gen uint64) {
	p.mu.RLock()
	if p.stopping || p.generation != gen || p.client == nil {
		p.mu.RUnlock()
		return
	}
	handler := p.lifecycleHandler
	p.mu.RUnlock()

	p.mu.Lock()
	p.everConnected = true
	p.unavailableNotified = false
	p.mu.Unlock()

	if handler != nil {
		handler.OnPlatformReady(p)
	}
}

func (p *Platform) clearClient(gen uint64, client *mautrix.Client) {
	notify := false
	p.mu.Lock()
	if p.client == client && p.generation == gen {
		p.client = nil
		notify = !p.stopping
	}
	p.mu.Unlock()

	if notify {
		p.notifyUnavailable(fmt.Errorf("matrix: connection lost"))
	}
}

func (p *Platform) notifyUnavailable(err error) {
	var handler core.PlatformLifecycleHandler

	p.mu.Lock()
	if p.stopping || err == nil || p.unavailableNotified {
		p.mu.Unlock()
		return
	}
	p.unavailableNotified = true
	handler = p.lifecycleHandler
	p.mu.Unlock()

	if handler != nil {
		handler.OnPlatformUnavailable(p, err)
	}
}

// Interface compliance checks
var (
	_ core.Platform                  = (*Platform)(nil)
	_ core.AsyncRecoverablePlatform  = (*Platform)(nil)
	_ core.ReplyContextReconstructor = (*Platform)(nil)
	_ core.ImageSender               = (*Platform)(nil)
	_ core.FileSender                = (*Platform)(nil)
	_ core.MessageUpdater            = (*Platform)(nil)
	_ core.TypingIndicator           = (*Platform)(nil)
)
