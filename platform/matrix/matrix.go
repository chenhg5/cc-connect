package matrix

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/crypto/verificationhelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"
)

func init() {
	core.RegisterPlatform("matrix", New)
}

type replyContext struct {
	roomID    id.RoomID
	messageID id.EventID
}

type Platform struct {
	homeserver          string
	accessToken         string
	userID              string
	allowFrom           string
	shareSessionInChan  bool
	groupReplyAll       bool
	autoJoin            bool
	autoVerify          bool
	proxyURL            string

	mu                  sync.RWMutex
	client              *mautrix.Client
	selfUserID          id.UserID
	handler             core.MessageHandler
	lifecycleHandler    core.PlatformLifecycleHandler
	cancel              context.CancelFunc
	stopping            bool
	generation          uint64
	everConnected       bool
	unavailableNotified bool
	dedup               core.MessageDedup
	httpClient          *http.Client
	cryptoHelper        *cryptohelper.CryptoHelper
	verificationHelper  *verificationhelper.VerificationHelper
	verifyDeviceID      id.DeviceID
		crossSigningPassword   string
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
		homeserver:         homeserver,
		accessToken:        accessToken,
		userID:             userID,
		allowFrom:          allowFrom,
		groupReplyAll:      groupReplyAll,
		shareSessionInChan: shareSession,
		autoJoin:           autoJoin,
		proxyURL:           proxyURL,
			autoVerify:         autoVerify,
		crossSigningPassword: crossSigningPassword,
		httpClient:         httpClient,
		dedup:              core.MessageDedup{},
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
			slog.Warn("matrix: connection error, retrying", "error", err, "backoff", wait)
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
	ch, err := p.initCrypto(ctx, client)
	if err != nil {
		slog.Warn("matrix: E2EE not available, encrypted rooms won't work", "error", err)
	} else {
		ch.DecryptErrorCallback = func(evt *event.Event, decryptErr error) {
			slog.Warn("matrix: decrypt failed", "event_id", evt.ID, "sender", evt.Sender, "room", evt.RoomID, "error", decryptErr)
		}
		ch.CustomPostDecrypt = func(ctx context.Context, evt *event.Event) {
			slog.Debug("matrix: decrypted event", "type", evt.Type.Type, "event_id", evt.ID, "sender", evt.Sender, "room", evt.RoomID)

			// Fix transaction ID for in-room verification events from other users.
			// The verificationhelper's wrapHandler uses evt.ID as the transaction ID,
			// but for in-room events the correct transaction ID is in m.relates_to.event_id
			// (pointing to the original m.key.verification.request message).
			if evt.RoomID != "" && evt.Sender != client.UserID && strings.HasPrefix(evt.Type.Type, "m.key.verification.") {
				if relatable, ok := evt.Content.Parsed.(event.Relatable); ok {
					if rel := relatable.OptionalGetRelatesTo(); rel != nil && rel.EventID != "" {
						slog.Debug("matrix: fixing verification event txn ID", "original_id", evt.ID, "txn_id", rel.EventID)
						evt.ID = rel.EventID
					}
				}
			}


			// Workaround for mautrix library bug: onVerificationMAC uses
			// GetOwnCrossSigningPublicKeys() instead of GetCrossSigningPublicKeys(ctx, theirUserID)
			// for cross-user verification, causing "unknown key ID" errors.
			// Intercept MAC events from other users and handle device trust directly.
			if evt.Type.Type == "m.key.verification.mac" && evt.RoomID != "" && evt.Sender != client.UserID {
				p.handleVerificationMAC(ctx, client, ch, evt)
				return
			}
			client.Syncer.(mautrix.DispatchableSyncer).Dispatch(ctx, evt)
		}
		p.setCryptoHelper(ch)
		slog.Info("matrix: E2EE enabled", "device_id", client.DeviceID)

		// Bootstrap cross-signing: generate keys, publish to server,
		// and sign our own device. Without this, Element shows
		// "encrypted by a device not verified by its owner".
		p.setupCrossSigning(ctx, ch)

		// client.Crypto must be set for VerificationHelper
		client.Crypto = ch

		// Initialize SAS verification helper (auto-accept verification requests)
		if p.autoVerify {
			if vErr := p.initVerification(ctx, ch); vErr != nil {
				slog.Warn("matrix: verification helper not available", "error", vErr)
			} else {
				slog.Info("matrix: SAS verification enabled", "mode", "auto-verify")
			}
		}
	}

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

func (p *Platform) initCrypto(ctx context.Context, client *mautrix.Client) (*cryptohelper.CryptoHelper, error) {
	if client.DeviceID == "" {
		return nil, fmt.Errorf("device ID not available from whoami")
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("get home dir: %w", err)
	}
	cryptoDir := filepath.Join(homeDir, ".cc-connect")
	if err := os.MkdirAll(cryptoDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	dbPath := filepath.Join(cryptoDir, fmt.Sprintf("matrix-crypto-%s.db", client.DeviceID))

	// Derive a stable pickle key from the access token
	h := sha256.Sum256([]byte(p.accessToken))
	pickleKey := make([]byte, 32)
	copy(pickleKey, h[:])

	return p.tryInitCrypto(ctx, client, pickleKey, dbPath, false)
}

func (p *Platform) tryInitCrypto(ctx context.Context, client *mautrix.Client, pickleKey []byte, dbPath string, isRetry bool) (*cryptohelper.CryptoHelper, error) {
	ch, err := cryptohelper.NewCryptoHelper(client, pickleKey, dbPath)
	if err != nil {
		return nil, fmt.Errorf("create crypto helper: %w", err)
	}
	ch.DBAccountID = client.UserID.String()

	if err := ch.Init(ctx); err != nil {
		if !isRetry && strings.Contains(err.Error(), "not marked as shared") {
			// Init failed because the server has old device keys that don't match
			// our fresh local account. Force-upload new keys to replace them.
			// After Init fails at verifyDeviceKeysOnServer, the olm machine is
			// initialized and the account is loaded, so Machine() is safe.
			slog.Warn("matrix: stale device keys on server, force-uploading new keys")
			func() {
				defer func() { recover() }() // Machine() panics if mach is nil
				if mach := ch.Machine(); mach != nil {
					if shareErr := mach.ShareKeys(ctx, -1); shareErr != nil {
						slog.Error("matrix: failed to force-share keys", "error", shareErr)
					}
				}
			}()
			ch.Close()
			client.StateStore = nil
			client.Store = mautrix.NewMemorySyncStore()
			return p.tryInitCrypto(ctx, client, pickleKey, dbPath, true)
		}
		p.cleanupFailedCrypto(client, ch)
		return nil, fmt.Errorf("init crypto: %w", err)
	}
	return ch, nil
}

func (p *Platform) cleanupFailedCrypto(client *mautrix.Client, ch *cryptohelper.CryptoHelper) {
	ch.Close()
	// Reset client stores to avoid references to the closed DB.
	// NewCryptoHelper sets client.StateStore; Init() may overwrite client.Store.
	client.StateStore = nil
	client.Store = mautrix.NewMemorySyncStore()
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

	// Group mention check
	if !isDM && !p.groupReplyAll {
		if !p.isDirectedAtBot(content, selfID) {
			return
		}
	}

	userName := displayName(evt.Sender)
	sessionKey := p.buildSessionKey(roomID, evt.Sender)
	channelKey := roomID.String()

	rctx := replyContext{roomID: roomID, messageID: evt.ID}

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
			MessageID: evt.ID.String(),
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

// sendRoomEvent sends an event to a room, encrypting it if the room has E2EE enabled.
func (p *Platform) sendRoomEvent(ctx context.Context, roomID id.RoomID, evtType event.Type, content any) error {
	client := p.getClient()
	if client == nil {
		return fmt.Errorf("matrix: not connected")
	}

	ch := p.getCryptoHelper()
	encrypted := ch != nil && p.isRoomEncrypted(ctx, roomID)
	if encrypted {
		encContent, err := ch.Encrypt(ctx, roomID, evtType, content)
		if err != nil {
			return fmt.Errorf("matrix: encrypt: %w", err)
		}
		_, err = client.SendMessageEvent(ctx, roomID, event.EventEncrypted, encContent)
		if err != nil {
			return fmt.Errorf("matrix: send encrypted: %w", err)
		}
		return nil
	}

	_, err := client.SendMessageEvent(ctx, roomID, evtType, content)
	if err != nil {
		return fmt.Errorf("matrix: send: %w", err)
	}
	return nil
}

func (p *Platform) isRoomEncrypted(ctx context.Context, roomID id.RoomID) bool {
	client := p.getClient()
	if client == nil || client.StateStore == nil {
		return false
	}
	ss, ok := client.StateStore.(crypto.StateStore)
	if !ok {
		return false
	}
	enc, err := ss.IsEncrypted(ctx, roomID)
	if err != nil {
		return false
	}
	return enc
}

func (p *Platform) Reply(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("matrix: invalid reply context type %T", rctx)
	}

	parsed := format.RenderMarkdown(content, true, false)
	parsed.Body = content
	if content != "" {
		parsed.RelatesTo = &event.RelatesTo{}
		parsed.RelatesTo.SetReplyTo(rc.messageID)
	}

	return p.sendRoomEvent(ctx, rc.roomID, event.EventMessage, &parsed)
}

func (p *Platform) Send(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("matrix: invalid reply context type %T", rctx)
	}

	parsed := format.RenderMarkdown(content, true, false)
	parsed.Body = content

	return p.sendRoomEvent(ctx, rc.roomID, event.EventMessage, &parsed)
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

	return p.sendRoomEvent(ctx, rc.roomID, event.EventMessage, content)
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

	return p.sendRoomEvent(ctx, rc.roomID, event.EventMessage, content)
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

	parsed.NewContent = &newContent
	parsed.RelatesTo = &event.RelatesTo{
		Type:    event.RelReplace,
		EventID: rc.messageID,
	}
	parsed.Body = "* " + content

	return p.sendRoomEvent(ctx, rc.roomID, event.EventMessage, &parsed)
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

	if !strings.HasPrefix(roomIDStr, "!") {
		return nil, fmt.Errorf("matrix: invalid room ID in %q", sessionKey)
	}
	return replyContext{roomID: id.RoomID(roomIDStr)}, nil
}

// --- Internal helpers ---

func (p *Platform) buildSessionKey(roomID id.RoomID, sender id.UserID) string {
	if p.shareSessionInChan {
		return fmt.Sprintf("matrix:%s", roomID)
	}
	return fmt.Sprintf("matrix:%s:%s", roomID, sender)
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
	defer resp.Body.Close()
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

func (p *Platform) getCryptoHelper() *cryptohelper.CryptoHelper {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cryptoHelper
}

func (p *Platform) setCryptoHelper(ch *cryptohelper.CryptoHelper) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cryptoHelper = ch
}

func (p *Platform) getVerificationHelper() *verificationhelper.VerificationHelper {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.verificationHelper
}

func (p *Platform) setVerificationHelper(vh *verificationhelper.VerificationHelper) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.verificationHelper = vh
}

func (p *Platform) closeCryptoHelper() {
	p.mu.Lock()
	ch := p.cryptoHelper
	p.cryptoHelper = nil
	p.mu.Unlock()

	if ch != nil {
		ch.Close()
	}
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
