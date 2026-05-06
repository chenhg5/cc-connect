package max

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterPlatform("max", New)
}

const (
	defaultAPIBase = "https://platform-api.max.ru"
	// pollTimeout — long-poll timeout sent to MAX (seconds). API allows 0–90,
	// dev docs default = 30. Using 30 balances responsiveness and load.
	pollTimeout = 30
	// httpTimeout caps the HTTP client wait. Must be much larger than
	// pollTimeout, otherwise transient MAX backend lag pushes header arrival
	// past the deadline and the client cancels the long-poll, triggering a
	// retry storm.
	httpTimeout = 90 * time.Second
	initialReconnectBackoff = time.Second
	maxReconnectBackoff     = 30 * time.Second
	stableConnectionWindow  = 10 * time.Second
	typingInterval          = 4 * time.Second
	maxAttachmentBytes      = 25 * 1024 * 1024 // 25 MiB cap per downloaded attachment
	attachmentDownloadTO    = 60 * time.Second
	attachmentUploadTO      = 5 * time.Minute
	// attachmentReadyDelay is the pause between CDN upload and POST /messages.
	// Without it MAX may reject the message with "attachment.not.ready" while
	// it is still indexing the freshly uploaded blob.
	attachmentReadyDelay   = 600 * time.Millisecond
	attachmentReadyRetries = 4
)

// replyContext carries the information needed to send a reply.
type replyContext struct {
	chatID    string
	messageID string // populated from incoming message, used only by UpdateMessage
}

// Platform implements core.Platform for the MAX messenger bot API.
type Platform struct {
	token     string
	apiBase   string
	allowFrom string

	// Webhook mode: if webhookURL is set, the platform registers a
	// subscription with MAX, listens on webhookListen for incoming updates
	// and DOES NOT run the long-poll loop. Required by MAX from 2026-05-11
	// (long-polling is being throttled to 2 RPS).
	webhookURL          string
	webhookListen       string
	webhookPath         string
	webhookSecret       string
	resubscribeInterval time.Duration

	mu           sync.RWMutex
	handler      core.MessageHandler
	cancel       context.CancelFunc
	stopping     bool
	client       *http.Client // general API calls — httpTimeout
	uploadClient *http.Client // CDN uploads — attachmentUploadTO (overrides short client Timeout)
	dedup        core.MessageDedup
	webServer    *http.Server
}

// New creates a MAX platform from config options.
//
//	[[projects.platforms]]
//	type = "max"
//	[projects.platforms.options]
//	token          = "<bot-token>"
//	allow_from     = "<user_id>,<user_id>"   # optional, "*" or empty = all
//	api_base       = "https://platform-api.max.ru"  # optional override
//	webhook_url    = "https://your.domain/webhook"  # optional; switches
//	                                               # platform to webhook mode
//	webhook_listen = ":8080"                       # optional, default ":8080"
//	webhook_path   = "/webhook"                    # optional, default "/webhook";
//	                                               # must match the path in webhook_url
//	webhook_secret = "<random-string>"             # optional; if set, sent to MAX
//	                                               # so MAX includes it in the
//	                                               # X-Max-Bot-Api-Secret header
//	                                               # of every webhook POST (?s= also
//	                                               # accepted for manual testing)
//	webhook_resubscribe_interval = "5m"            # optional, default 5m; cc-connect
//	                                               # periodically re-POSTs the
//	                                               # subscription because MAX has been
//	                                               # observed to silently drop it
func New(opts map[string]any) (core.Platform, error) {
	token, _ := opts["token"].(string)
	if token == "" {
		return nil, fmt.Errorf("max: token is required")
	}
	apiBase, _ := opts["api_base"].(string)
	if apiBase == "" {
		apiBase = defaultAPIBase
	}
	allowFrom, _ := opts["allow_from"].(string)
	core.CheckAllowFrom("max", allowFrom)

	webhookURL, _ := opts["webhook_url"].(string)
	webhookListen, _ := opts["webhook_listen"].(string)
	webhookPath, _ := opts["webhook_path"].(string)
	webhookSecret, _ := opts["webhook_secret"].(string)
	if webhookURL != "" && webhookListen == "" {
		webhookListen = ":8080"
	}
	if webhookPath == "" {
		webhookPath = "/webhook"
	} else if !strings.HasPrefix(webhookPath, "/") {
		webhookPath = "/" + webhookPath
	}

	resubscribeInterval := 5 * time.Minute
	if raw, ok := opts["webhook_resubscribe_interval"].(string); ok && raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("max: webhook_resubscribe_interval: %w", err)
		}
		resubscribeInterval = d
	}

	return &Platform{
		token:               token,
		apiBase:             apiBase,
		allowFrom:           allowFrom,
		webhookURL:          webhookURL,
		webhookListen:       webhookListen,
		webhookPath:         webhookPath,
		webhookSecret:       webhookSecret,
		resubscribeInterval: resubscribeInterval,
		client:              &http.Client{Timeout: httpTimeout},
		uploadClient:        &http.Client{Timeout: attachmentUploadTO},
	}, nil
}

func (p *Platform) Name() string { return "max" }

func (p *Platform) Start(handler core.MessageHandler) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.stopping {
		return fmt.Errorf("max: platform stopped")
	}
	p.handler = handler

	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	// Verify token at startup
	if name, id, err := p.getMe(ctx); err != nil {
		slog.Warn("max: could not verify bot token", "error", err)
	} else {
		slog.Info("max: connected", "bot", name, "id", id)
	}

	if p.webhookURL != "" {
		if err := p.startWebhook(ctx); err != nil {
			cancel()
			return fmt.Errorf("max: start webhook: %w", err)
		}
		return nil
	}

	go p.pollLoop(ctx)
	return nil
}

func (p *Platform) Stop() error {
	p.mu.Lock()
	srv := p.webServer
	url := p.webhookURL
	p.stopping = true
	if p.cancel != nil {
		p.cancel()
	}
	p.mu.Unlock()
	if srv != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}
	if url != "" {
		// Best-effort unsubscribe so MAX doesn't keep delivering events to a
		// dead URL. Failures here are not fatal — service is shutting down.
		unsubCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := p.unsubscribe(unsubCtx, url); err != nil {
			slog.Warn("max: unsubscribe webhook failed", "url", url, "err", err)
		}
	}
	return nil
}

// startWebhook registers a webhook subscription with MAX and brings up an
// HTTP server on webhookListen so MAX can POST updates to webhookURL.
// Called from Start() when webhook_url is configured. Long-polling is NOT
// started in webhook mode — the two are mutually exclusive (MAX delivers
// each update to one transport).
func (p *Platform) startWebhook(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc(p.webhookPath, p.webhookHandler)
	srv := &http.Server{
		Addr:              p.webhookListen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	// Caller (Start) already holds p.mu, so assign directly — re-locking
	// a non-reentrant sync.RWMutex would deadlock.
	p.webServer = srv

	go func() {
		slog.Info("max: webhook listening", "addr", p.webhookListen, "path", p.webhookPath, "url", p.webhookURL)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("max: webhook listener stopped", "err", err)
		}
	}()

	if err := p.subscribe(ctx, p.webhookURL); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	slog.Info("max: webhook subscribed", "url", p.webhookURL)

	// MAX has been observed to silently drop the webhook subscription
	// server-side without any delivery error. The documented 8h failure
	// window does not match the observed cadence (drops every 25–60min),
	// so we periodically re-POST the subscription. MAX overwrites the
	// existing registration in-place, so re-subscribing is idempotent.
	if p.resubscribeInterval > 0 {
		go p.resubscribeLoop(ctx)
	}
	return nil
}

func (p *Platform) resubscribeLoop(ctx context.Context) {
	t := time.NewTicker(p.resubscribeInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			rsCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := p.subscribe(rsCtx, p.webhookURL); err != nil {
				slog.Warn("max: periodic re-subscribe failed", "err", err)
			} else {
				slog.Debug("max: periodic re-subscribe ok")
			}
			cancel()
		}
	}
}

// webhookHandler accepts a POST from MAX with a single update and routes it
// through the same handleUpdate path used by long-polling.
func (p *Platform) webhookHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if p.webhookSecret != "" {
		// MAX sends the secret in X-Max-Bot-Api-Secret on every webhook POST
		// when the subscription was created with a "secret" field.
		// ?s= query is accepted as a fallback for manual curl testing.
		got := r.Header.Get("X-Max-Bot-Api-Secret")
		if got == "" {
			got = r.URL.Query().Get("s")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(p.webhookSecret)) != 1 {
			slog.Warn("max: webhook secret mismatch", "remote", r.RemoteAddr)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024*1024))
	if err != nil {
		slog.Warn("max: webhook read body", "err", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var upd maxUpdate
	if err := json.Unmarshal(body, &upd); err != nil {
		slog.Warn("max: webhook unmarshal", "err", err, "body", string(body[:min(len(body), 256)]))
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	// MAX expects a fast 200 — process the update asynchronously so we
	// never let agent latency back-pressure the delivery side.
	go func() {
		p.mu.RLock()
		ctx := context.Background()
		if p.cancel != nil {
			// Use the platform's own cancellation context so a Stop() also
			// short-circuits in-flight handler work.
			ctx2, cancel := context.WithCancel(ctx)
			_ = cancel
			ctx = ctx2
		}
		p.mu.RUnlock()
		p.handleUpdate(ctx, &upd)
	}()
	w.WriteHeader(http.StatusOK)
}

// subscribe registers a webhook URL with MAX. The MAX bot API supports
// only one webhook per bot — if an old URL is registered, MAX overwrites
// it on a successful subscribe, so no explicit cleanup is required.
func (p *Platform) subscribe(ctx context.Context, url string) error {
	payload := map[string]any{
		"url":          url,
		"update_types": []string{"message_created", "message_callback"},
	}
	if p.webhookSecret != "" {
		payload["secret"] = p.webhookSecret
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.apiBase+"/subscriptions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	p.setAuth(req)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, respBody)
	}
	return nil
}

// unsubscribe removes the webhook registration. Only used during Stop().
func (p *Platform) unsubscribe(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, p.apiBase+"/subscriptions", nil)
	if err != nil {
		return err
	}
	p.setAuth(req)
	q := req.URL.Query()
	q.Set("url", url)
	req.URL.RawQuery = q.Encode()
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, respBody)
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// --- Sending ---

func (p *Platform) Reply(ctx context.Context, replyCtx any, content string) error {
	return p.sendText(ctx, replyCtx, content, nil)
}

func (p *Platform) Send(ctx context.Context, replyCtx any, content string) error {
	return p.sendText(ctx, replyCtx, content, nil)
}

// SendWithButtons implements core.InlineButtonSender — sends message with callback buttons.
func (p *Platform) SendWithButtons(ctx context.Context, replyCtx any, content string, buttons [][]core.ButtonOption) error {
	maxButtons := make([][]maxButton, 0, len(buttons))
	for _, row := range buttons {
		maxRow := make([]maxButton, 0, len(row))
		for _, btn := range row {
			maxRow = append(maxRow, maxButton{
				Type:    "callback",
				Text:    btn.Text,
				Payload: btn.Data,
			})
		}
		maxButtons = append(maxButtons, maxRow)
	}
	return p.sendText(ctx, replyCtx, content, maxButtons)
}

// SendImage implements core.ImageSender.
func (p *Platform) SendImage(ctx context.Context, replyCtx any, img core.ImageAttachment) error {
	rctx, ok := replyCtx.(replyContext)
	if !ok {
		return fmt.Errorf("max: unexpected replyCtx type %T", replyCtx)
	}
	token, err := p.uploadAttachment(ctx, "image", img.Data, img.FileName)
	if err != nil {
		return fmt.Errorf("max: upload image: %w", err)
	}
	body := &maxSendBody{
		Attachments: []maxOutAttachment{{
			Type:    "image",
			Payload: maxTokenPayload{Token: token},
		}},
	}
	return p.postMessage(ctx, rctx.chatID, body)
}

// SendFile implements core.FileSender. MAX routes images uploaded via the file
// endpoint as type="file" in the message, so we honor the declared kind: if the
// mime says image/*, we upload as image so the recipient sees a proper image
// preview instead of a generic file card.
func (p *Platform) SendFile(ctx context.Context, replyCtx any, file core.FileAttachment) error {
	rctx, ok := replyCtx.(replyContext)
	if !ok {
		return fmt.Errorf("max: unexpected replyCtx type %T", replyCtx)
	}
	kind := "file"
	attType := "file"
	if strings.HasPrefix(file.MimeType, "image/") {
		kind = "image"
		attType = "image"
	} else if strings.HasPrefix(file.MimeType, "video/") {
		kind = "video"
		attType = "video"
	} else if strings.HasPrefix(file.MimeType, "audio/") {
		kind = "audio"
		attType = "audio"
	}
	token, err := p.uploadAttachment(ctx, kind, file.Data, file.FileName)
	if err != nil {
		return fmt.Errorf("max: upload file: %w", err)
	}
	body := &maxSendBody{
		Attachments: []maxOutAttachment{{
			Type:    attType,
			Payload: maxTokenPayload{Token: token},
		}},
	}
	return p.postMessage(ctx, rctx.chatID, body)
}

// SendAudio implements core.AudioSender — uploads a voice/audio blob and sends
// it as a native MAX audio attachment. Used by the TTS pipeline to reply in
// voice when [tts] is enabled in config.
func (p *Platform) SendAudio(ctx context.Context, replyCtx any, audio []byte, format string) error {
	rctx, ok := replyCtx.(replyContext)
	if !ok {
		return fmt.Errorf("max: unexpected replyCtx type %T", replyCtx)
	}
	if format == "" {
		format = "mp3"
	}
	token, err := p.uploadAttachment(ctx, "audio", audio, "voice."+format)
	if err != nil {
		return fmt.Errorf("max: upload audio: %w", err)
	}
	body := &maxSendBody{
		Attachments: []maxOutAttachment{{
			Type:    "audio",
			Payload: maxTokenPayload{Token: token},
		}},
	}
	return p.postMessage(ctx, rctx.chatID, body)
}

// UpdateMessage implements core.MessageUpdater via PUT /messages?message_id=.
func (p *Platform) UpdateMessage(ctx context.Context, replyCtx any, content string) error {
	rctx, ok := replyCtx.(replyContext)
	if !ok {
		return fmt.Errorf("max: unexpected replyCtx type %T", replyCtx)
	}
	if rctx.messageID == "" {
		return fmt.Errorf("max: update message: no message id in reply context")
	}
	body := maxSendBody{Text: content, Format: "markdown"}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, p.apiBase+"/messages", bytes.NewReader(data))
	if err != nil {
		return err
	}
	p.setAuth(req)
	q := req.URL.Query()
	q.Set("message_id", rctx.messageID)
	req.URL.RawQuery = q.Encode()
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("max: edit message: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("max: edit message: HTTP %d: %s", resp.StatusCode, respBody)
	}
	return nil
}

// uploadAttachment performs the two-step MAX upload: request an upload URL from
// /uploads?type=<kind>, then POST the binary as multipart/form-data field "data"
// to that URL. Returns the token to embed in a subsequent /messages attachment.
func (p *Platform) uploadAttachment(ctx context.Context, kind string, data []byte, filename string) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("empty attachment data")
	}
	// Use a 5-minute context AND a dedicated http.Client with a matching Timeout.
	// p.client has a 35 s Timeout which fires independently of the context deadline
	// and would abort large CDN uploads before the context expires.
	uploadCtx, cancel := context.WithTimeout(ctx, attachmentUploadTO)
	defer cancel()

	urlReq, err := http.NewRequestWithContext(uploadCtx, http.MethodPost, p.apiBase+"/uploads", nil)
	if err != nil {
		return "", err
	}
	p.setAuth(urlReq)
	q := urlReq.URL.Query()
	q.Set("type", kind)
	urlReq.URL.RawQuery = q.Encode()

	urlResp, err := p.uploadClient.Do(urlReq)
	if err != nil {
		return "", fmt.Errorf("request upload url: %w", err)
	}
	defer urlResp.Body.Close()
	if urlResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(urlResp.Body, 512))
		return "", fmt.Errorf("upload url: HTTP %d: %s", urlResp.StatusCode, body)
	}
	var urlInfo struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(urlResp.Body).Decode(&urlInfo); err != nil {
		return "", fmt.Errorf("decode upload url: %w", err)
	}
	if urlInfo.URL == "" {
		return "", fmt.Errorf("upload url: empty url in response")
	}

	if filename == "" {
		filename = defaultFilename(kind)
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("data", filename)
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(data); err != nil {
		return "", err
	}
	if err := mw.Close(); err != nil {
		return "", err
	}

	cdnReq, err := http.NewRequestWithContext(uploadCtx, http.MethodPost, urlInfo.URL, &buf)
	if err != nil {
		return "", err
	}
	p.setAuth(cdnReq)
	cdnReq.Header.Set("Content-Type", mw.FormDataContentType())

	cdnResp, err := p.uploadClient.Do(cdnReq)
	if err != nil {
		return "", fmt.Errorf("cdn upload: %w", err)
	}
	defer cdnResp.Body.Close()
	if cdnResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(cdnResp.Body, 512))
		return "", fmt.Errorf("cdn upload: HTTP %d: %s", cdnResp.StatusCode, body)
	}
	cdnBody, err := io.ReadAll(io.LimitReader(cdnResp.Body, 64*1024))
	if err != nil {
		return "", fmt.Errorf("read cdn response: %w", err)
	}
	// MAX CDN uses different response shapes per attachment kind:
	//   image: {"photos": {"<photo_id>": {"token": "..."}}}
	//   file:  {"token": "..."}
	//   video/audio: "<retval>1</retval>" (XML) — the real token is already in urlInfo.Token
	if token := extractCDNToken(kind, cdnBody); token != "" {
		return token, nil
	}
	if urlInfo.Token != "" {
		return urlInfo.Token, nil
	}
	return "", fmt.Errorf("cdn upload: no token in response: %s", cdnBody)
}

// extractCDNToken parses the token out of a MAX CDN upload response. Returns
// "" if not found; the caller is expected to fall back to urlInfo.Token.
func extractCDNToken(kind string, body []byte) string {
	switch kind {
	case "image":
		var resp struct {
			Photos map[string]struct {
				Token string `json:"token"`
			} `json:"photos"`
		}
		if err := json.Unmarshal(body, &resp); err == nil {
			for _, ph := range resp.Photos {
				if ph.Token != "" {
					return ph.Token
				}
			}
		}
	case "video", "audio":
		// CDN returns XML for video/audio; token lives in urlInfo.Token. Nothing to extract here.
	default: // file
		var resp struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(body, &resp); err == nil && resp.Token != "" {
			return resp.Token
		}
	}
	return ""
}

func defaultFilename(kind string) string {
	switch kind {
	case "image":
		return "image.png"
	case "video":
		return "video.mp4"
	case "audio":
		return "audio.mp3"
	}
	return "file.bin"
}

// StartTyping implements core.TypingIndicator — drives the MAX "is typing"
// presence indicator via POST /chats/{id}/actions {"action":"typing_on"}.
// MAX clears the indicator automatically after ~10s of inactivity, so we
// re-arm it on a ticker until the returned cancel func is called.
func (p *Platform) StartTyping(ctx context.Context, replyCtx any) (stop func()) {
	rctx, ok := replyCtx.(replyContext)
	if !ok || rctx.chatID == "" {
		return func() {}
	}
	tickCtx, cancel := context.WithCancel(ctx)
	_ = p.sendChatAction(tickCtx, rctx.chatID, "typing_on")
	go func() {
		ticker := time.NewTicker(typingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-tickCtx.Done():
				return
			case <-ticker.C:
				_ = p.sendChatAction(tickCtx, rctx.chatID, "typing_on")
			}
		}
	}()
	return cancel
}

// sendChatAction posts a presence action to MAX (typing_on / mark_seen).
// Best-effort: errors are logged at debug level only and never block the
// main message-handling flow.
func (p *Platform) sendChatAction(ctx context.Context, chatID, action string) error {
	if chatID == "" || action == "" {
		return nil
	}
	body, err := json.Marshal(map[string]string{"action": action})
	if err != nil {
		return err
	}
	url := p.apiBase + "/chats/" + chatID + "/actions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	p.setAuth(req)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		slog.Debug("max: chat action failed", "chat", chatID, "action", action, "err", err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		slog.Debug("max: chat action non-2xx",
			"chat", chatID, "action", action, "status", resp.StatusCode, "body", string(respBody))
		return fmt.Errorf("max: chat action %s: status %d", action, resp.StatusCode)
	}
	return nil
}

// FormattingInstructions implements core.FormattingInstructionProvider.
// The engine appends this to the agent system prompt so Claude uses only
// MAX-supported markdown syntax.
func (p *Platform) FormattingInstructions() string {
	return `Formatting rules for MAX messenger:
- **bold** and _italic_ are supported
- Inline code: ` + "`code`" + ` and fenced code blocks (` + "```" + `) are supported
- Bullet lists with - or * are supported as plain text
- Do NOT use headers (# ## ###)
- Do NOT use horizontal rules (--- or ***)
- Do NOT use tables
- Do NOT use HTML tags
Keep responses concise and use plain text where possible.`
}

// ReconstructReplyCtx implements core.ReplyContextReconstructor.
// Session key format: "max:{chatID}" or "max:{chatID}:{userID}".
func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	rest, ok := strings.CutPrefix(sessionKey, "max:")
	if !ok {
		return nil, fmt.Errorf("max: cannot reconstruct reply ctx from %q", sessionKey)
	}
	chatID, _, _ := strings.Cut(rest, ":")
	if chatID == "" {
		return nil, fmt.Errorf("max: cannot reconstruct reply ctx from %q", sessionKey)
	}
	return replyContext{chatID: chatID}, nil
}

// --- MAX API types ---

type maxButton struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Payload string `json:"payload"`
}

// maxOutAttachment is the generic outgoing attachment wrapper used for both
// inline_keyboard (with maxKbPayload) and image/file/video/audio (with
// maxTokenPayload).
type maxOutAttachment struct {
	Type    string `json:"type"`
	Payload any    `json:"payload,omitempty"`
}

type maxKbPayload struct {
	Buttons [][]maxButton `json:"buttons"`
}

type maxTokenPayload struct {
	Token string `json:"token"`
}

type maxSendBody struct {
	Text        string             `json:"text"`
	Format      string             `json:"format,omitempty"`
	Attachments []maxOutAttachment `json:"attachments,omitempty"`
}

type maxUpdate struct {
	UpdateType string `json:"update_type"`
	Timestamp  int64  `json:"timestamp"`

	// message_created
	Message *maxMessage `json:"message,omitempty"`

	// message_callback
	Callback *maxCallback `json:"callback,omitempty"`
}

type maxMessage struct {
	Sender    maxUser      `json:"sender"`
	Recipient maxRecipient `json:"recipient"`
	Timestamp int64        `json:"timestamp"`
	Body      maxBody      `json:"body"`
	// Link is set when the message is a forward or a reply. For forwarded
	// messages the actual content (text + attachments) lives inside Link.Message,
	// while Body may be empty. We surface those attachments to the agent.
	Link *maxLink `json:"link,omitempty"`
}

// maxLink mirrors the LinkedMessage object from MAX bot API. Type is "forward"
// or "reply"; for forwarded messages the inner Message contains the original
// text and attachments.
type maxLink struct {
	Type    string  `json:"type"`
	Sender  maxUser `json:"sender,omitempty"`
	ChatID  int64   `json:"chat_id,omitempty"`
	Message maxBody `json:"message"`
}

type maxBody struct {
	Mid         string             `json:"mid"`
	Text        string             `json:"text"`
	Attachments []maxAttachmentRaw `json:"attachments,omitempty"`
}

// maxAttachmentRaw mirrors what MAX API delivers in message_created updates.
// Known types: "image", "video", "audio", "file", "sticker", "share".
// "image" carries payload.url directly; "video"/"audio" only carry payload.token
// and require an extra API round-trip (/videos/{token}, /audios/{token}) to
// resolve the actual download URL.
type maxAttachmentRaw struct {
	Type     string             `json:"type"`
	Payload  maxAttachmentPayld `json:"payload"`
	Filename string             `json:"filename,omitempty"`
}

type maxAttachmentPayld struct {
	URL   string `json:"url,omitempty"`
	Token string `json:"token,omitempty"`
}

type maxUser struct {
	UserID int64  `json:"user_id"`
	Name   string `json:"name"`
}

type maxRecipient struct {
	ChatID int64 `json:"chat_id"`
}

type maxCallback struct {
	CallbackID string     `json:"callback_id"`
	Payload    string     `json:"payload"`
	User       maxUser    `json:"user"`
	Message    maxMessage `json:"message"`
}

type maxUpdatesResponse struct {
	Updates []maxUpdate `json:"updates"`
	Marker  *int64      `json:"marker"`
}

// --- Long polling ---

func (p *Platform) pollLoop(ctx context.Context) {
	var marker *int64
	backoff := initialReconnectBackoff

	for {
		if ctx.Err() != nil {
			return
		}

		startedAt := time.Now()
		newMarker, err := p.poll(ctx, marker)
		if ctx.Err() != nil {
			return
		}

		if err != nil {
			elapsed := time.Since(startedAt)
			wait := backoff
			if elapsed >= stableConnectionWindow {
				wait = initialReconnectBackoff
				backoff = initialReconnectBackoff
			} else {
				backoff *= 2
				if backoff > maxReconnectBackoff {
					backoff = maxReconnectBackoff
				}
			}
			slog.Warn("max: poll error, retrying", "error", err, "backoff", wait)
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			continue
		}

		backoff = initialReconnectBackoff
		if newMarker != nil {
			marker = newMarker
		}
	}
}

func (p *Platform) poll(ctx context.Context, marker *int64) (*int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.apiBase+"/updates", nil)
	if err != nil {
		return nil, err
	}
	p.setAuth(req)
	q := req.URL.Query()
	q.Set("timeout", strconv.Itoa(pollTimeout))
	q.Set("limit", "20")
	q.Set("types", "message_created,message_callback")
	if marker != nil {
		q.Set("marker", strconv.FormatInt(*marker, 10))
	}
	req.URL.RawQuery = q.Encode()

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("poll request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("poll: HTTP %d: %s", resp.StatusCode, body)
	}

	var result maxUpdatesResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("poll decode: %w", err)
	}

	for i := range result.Updates {
		p.handleUpdate(ctx, &result.Updates[i])
	}

	return result.Marker, nil
}

func (p *Platform) handleUpdate(ctx context.Context, upd *maxUpdate) {
	switch upd.UpdateType {
	case "message_created":
		if upd.Message != nil {
			p.handleMessage(ctx, upd.Message)
		}
	case "message_callback":
		if upd.Callback != nil {
			p.handleCallback(ctx, upd.Callback)
		}
	}
}

func (p *Platform) handleMessage(ctx context.Context, msg *maxMessage) {
	msgTime := time.UnixMilli(msg.Timestamp)
	if core.IsOldMessage(msgTime) {
		slog.Debug("max: ignoring old message after restart", "date", msgTime)
		return
	}
	if p.dedup.IsDuplicate(msg.Body.Mid) {
		slog.Debug("max: duplicate message ignored", "message_id", msg.Body.Mid)
		return
	}

	text := msg.Body.Text
	atts := msg.Body.Attachments

	// Forwarded message: text and attachments live inside link.message.
	// We merge them into the visible payload so the agent sees the file.
	// For replies (link.type == "reply") we keep the user's own text/atts
	// untouched — the quoted message is just context.
	if msg.Link != nil && msg.Link.Type == "forward" {
		if text == "" {
			text = msg.Link.Message.Text
		} else if msg.Link.Message.Text != "" {
			text = text + "\n\n[forwarded] " + msg.Link.Message.Text
		}
		if len(msg.Link.Message.Attachments) > 0 {
			atts = append(atts, msg.Link.Message.Attachments...)
		}
	}

	if text == "" && len(atts) == 0 {
		return
	}

	userID := strconv.FormatInt(msg.Sender.UserID, 10)
	if !core.AllowList(p.allowFrom, userID) {
		slog.Debug("max: message from unauthorized user", "user_id", userID)
		return
	}

	chatID := strconv.FormatInt(msg.Recipient.ChatID, 10)
	sessionKey := fmt.Sprintf("max:%s:%s", chatID, userID)
	rctx := replyContext{chatID: chatID, messageID: msg.Body.Mid}

	// Acknowledge the message so the user gets a "read" tick in MAX.
	// Fire-and-forget — must never block the routing flow.
	go func() { _ = p.sendChatAction(ctx, chatID, "mark_seen") }()

	images, files, audio := p.fetchAttachments(ctx, atts)

	if text == "" && audio == nil && (len(images) > 0 || len(files) > 0) {
		switch {
		case len(images) > 0 && len(files) == 0:
			text = "Please look at the attached image."
		case len(files) > 0 && len(images) == 0:
			text = "Please look at the attached file."
		default:
			text = "Please look at the attached files."
		}
	}

	slog.Debug("max: message received",
		"user", msg.Sender.Name, "chat", chatID,
		"text_len", len(text), "images", len(images), "files", len(files), "has_audio", audio != nil)

	handler := p.getHandler()
	if handler == nil {
		return
	}
	handler(p, &core.Message{
		SessionKey: sessionKey,
		Platform:   "max",
		MessageID:  msg.Body.Mid,
		UserID:     userID,
		UserName:   msg.Sender.Name,
		Content:    text,
		Images:     images,
		Files:      files,
		Audio:      audio,
		ReplyCtx:   rctx,
	})
}

// fetchAttachments downloads every supported attachment from a MAX message
// and splits them into images, files, and at most one audio blob. Audio is
// returned separately so the core engine can route it through the speech
// transcription pipeline instead of exposing a raw .mp3 to the agent.
// Unsupported types (sticker, share, contact) are silently dropped.
func (p *Platform) fetchAttachments(ctx context.Context, atts []maxAttachmentRaw) ([]core.ImageAttachment, []core.FileAttachment, *core.AudioAttachment) {
	if len(atts) == 0 {
		return nil, nil, nil
	}
	var images []core.ImageAttachment
	var files []core.FileAttachment
	var audio *core.AudioAttachment
	for _, a := range atts {
		switch a.Type {
		case "image":
			data, mime, err := p.downloadAttachment(ctx, a.Payload.URL)
			if err != nil {
				slog.Warn("max: image download failed", "error", err)
				continue
			}
			if mime == "" || mime == "application/octet-stream" {
				mime = sniffImageMime(data)
			}
			images = append(images, core.ImageAttachment{
				MimeType: mime,
				Data:     data,
				FileName: a.Filename,
			})
		case "file":
			url := a.Payload.URL
			if url == "" {
				slog.Warn("max: file attachment without payload.url, skipping", "filename", a.Filename)
				continue
			}
			data, mime, err := p.downloadAttachment(ctx, url)
			if err != nil {
				slog.Warn("max: file download failed", "error", err, "filename", a.Filename)
				continue
			}
			effectiveMime := mime
			if effectiveMime == "" || effectiveMime == "application/octet-stream" {
				if sniffed := sniffImageMime(data); sniffed != "application/octet-stream" {
					effectiveMime = sniffed
				}
			}
			if strings.HasPrefix(effectiveMime, "image/") {
				images = append(images, core.ImageAttachment{
					MimeType: effectiveMime,
					Data:     data,
					FileName: a.Filename,
				})
				continue
			}
			if strings.HasPrefix(effectiveMime, "audio/") && audio == nil {
				audio = &core.AudioAttachment{
					MimeType: effectiveMime,
					Data:     data,
					Format:   audioFormatFromMime(effectiveMime, a.Filename),
				}
				continue
			}
			if effectiveMime == "" {
				effectiveMime = "application/octet-stream"
			}
			files = append(files, core.FileAttachment{
				MimeType: effectiveMime,
				Data:     data,
				FileName: a.Filename,
			})
		case "audio":
			url := a.Payload.URL
			if url == "" {
				url, _, _ = p.resolveMediaURL(ctx, a.Type, a.Payload.Token)
			}
			if url == "" {
				slog.Warn("max: audio has no download URL")
				continue
			}
			data, mime, err := p.downloadAttachment(ctx, url)
			if err != nil {
				slog.Warn("max: audio download failed", "error", err)
				continue
			}
			if audio == nil {
				audio = &core.AudioAttachment{
					MimeType: mime,
					Data:     data,
					Format:   audioFormatFromMime(mime, a.Filename),
				}
			}
		case "video":
			url := a.Payload.URL
			if url == "" {
				url, _, _ = p.resolveMediaURL(ctx, a.Type, a.Payload.Token)
			}
			if url == "" {
				slog.Warn("max: video has no download URL")
				continue
			}
			data, mime, err := p.downloadAttachment(ctx, url)
			if err != nil {
				slog.Warn("max: video download failed", "error", err)
				continue
			}
			fname := a.Filename
			files = append(files, core.FileAttachment{
				MimeType: mime,
				Data:     data,
				FileName: fname,
			})
		default:
			slog.Debug("max: skipping unsupported attachment type", "type", a.Type)
		}
	}
	return images, files, audio
}

// audioFormatFromMime derives the short format hint ("ogg", "mp3", "m4a", …)
// expected by core.AudioAttachment.Format. MAX voice messages are typically
// ogg/opus; audio files uploaded via paperclip can be anything.
func audioFormatFromMime(mime, filename string) string {
	if mime != "" {
		if i := strings.Index(mime, "/"); i >= 0 {
			sub := mime[i+1:]
			switch sub {
			case "mpeg":
				return "mp3"
			case "mp4", "x-m4a":
				return "m4a"
			case "ogg", "webm", "wav":
				return sub
			}
			if sub != "" {
				return sub
			}
		}
	}
	if filename != "" {
		if i := strings.LastIndex(filename, "."); i >= 0 && i < len(filename)-1 {
			return strings.ToLower(filename[i+1:])
		}
	}
	return "ogg"
}

// downloadAttachment GETs an arbitrary URL (typically a pre-signed CDN link
// from MAX), capping the response at maxAttachmentBytes. The URLs MAX serves
// for image/file payloads are already authenticated, so no bot token is
// attached to the request.
func (p *Platform) downloadAttachment(ctx context.Context, url string) ([]byte, string, error) {
	if url == "" {
		return nil, "", fmt.Errorf("empty url")
	}
	dlCtx, cancel := context.WithTimeout(ctx, attachmentDownloadTO)
	defer cancel()
	req, err := http.NewRequestWithContext(dlCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAttachmentBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxAttachmentBytes {
		return nil, "", fmt.Errorf("attachment exceeds %d bytes", maxAttachmentBytes)
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// resolveMediaURL asks MAX for the playable/downloadable URL of a video or
// audio attachment. MAX delivers only an opaque token in the message payload
// and exposes /videos/{token} and /audios/{token} for resolution.
func (p *Platform) resolveMediaURL(ctx context.Context, kind, token string) (string, string, error) {
	if token == "" {
		return "", "", fmt.Errorf("empty token")
	}
	endpoint := "/videos/"
	if kind == "audio" {
		endpoint = "/audios/"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.apiBase+endpoint+token, nil)
	if err != nil {
		return "", "", err
	}
	p.setAuth(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}
	var info struct {
		URL   string `json:"url"`
		Files struct {
			MP4 struct {
				URL string `json:"url"`
			} `json:"mp4"`
		} `json:"files"`
		Filename string `json:"filename"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", "", err
	}
	url := info.URL
	if url == "" {
		url = info.Files.MP4.URL
	}
	return url, info.Filename, nil
}

// sniffImageMime is a tiny fallback when the CDN returned no Content-Type.
func sniffImageMime(data []byte) string {
	if len(data) >= 8 && data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G' {
		return "image/png"
	}
	if len(data) >= 2 && data[0] == 0xFF && data[1] == 0xD8 {
		return "image/jpeg"
	}
	if len(data) >= 4 && string(data[:4]) == "GIF8" {
		return "image/gif"
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "image/webp"
	}
	return "application/octet-stream"
}

func (p *Platform) handleCallback(ctx context.Context, cb *maxCallback) {
	if p.dedup.IsDuplicate(cb.CallbackID) {
		slog.Debug("max: duplicate callback ignored", "callback_id", cb.CallbackID)
		return
	}
	userID := strconv.FormatInt(cb.User.UserID, 10)
	if !core.AllowList(p.allowFrom, userID) {
		slog.Debug("max: callback from unauthorized user", "user_id", userID)
		return
	}

	chatID := strconv.FormatInt(cb.Message.Recipient.ChatID, 10)
	sessionKey := fmt.Sprintf("max:%s:%s", chatID, userID)
	rctx := replyContext{chatID: chatID, messageID: cb.Message.Body.Mid}

	slog.Debug("max: callback received", "user", cb.User.Name, "payload", cb.Payload)

	handler := p.getHandler()
	if handler == nil {
		return
	}
	handler(p, &core.Message{
		SessionKey: sessionKey,
		Platform:   "max",
		MessageID:  cb.CallbackID,
		UserID:     userID,
		UserName:   cb.User.Name,
		Content:    cb.Payload,
		ReplyCtx:   rctx,
	})
}

// --- HTTP helpers ---

// normalizeLineBreaks converts single newlines to markdown hard breaks
// (two trailing spaces + \n). MAX markdown parser renders a bare \n as
// literal `'n` on the client; CommonMark spec treats a single \n as just
// whitespace, so we explicitly mark intended line breaks. Paragraph breaks
// (consecutive newlines) are preserved, and fenced code blocks are left
// untouched so code indentation stays intact.
func normalizeLineBreaks(s string) string {
	if !strings.Contains(s, "\n") {
		return s
	}
	lines := strings.Split(s, "\n")
	var sb strings.Builder
	sb.Grow(len(s) + len(lines)*2)
	inCodeBlock := false
	for i, line := range lines {
		isFence := strings.HasPrefix(strings.TrimSpace(line), "```")
		if isFence {
			inCodeBlock = !inCodeBlock
		}
		sb.WriteString(line)
		if i < len(lines)-1 {
			next := lines[i+1]
			// Do not add hard break when: inside code block, on a fence
			// line, empty line, empty next line, or already has trailing
			// double-space (hard break) / backslash (escaped break).
			if !inCodeBlock && !isFence && line != "" && next != "" &&
				!strings.HasSuffix(line, "  ") && !strings.HasSuffix(line, `\`) {
				sb.WriteString("  ")
			}
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

func (p *Platform) sendText(ctx context.Context, replyCtx any, content string, buttons [][]maxButton) error {
	rctx, ok := replyCtx.(replyContext)
	if !ok {
		return fmt.Errorf("max: unexpected replyCtx type %T", replyCtx)
	}

	var kbAttachments []maxOutAttachment
	if len(buttons) > 0 {
		kbAttachments = []maxOutAttachment{{
			Type:    "inline_keyboard",
			Payload: maxKbPayload{Buttons: buttons},
		}}
	}

	content = normalizeLineBreaks(content)
	// MAX API caps body around 4000 bytes; 1500 runes ≈ 3000 bytes of Cyrillic UTF-8
	// (or 1500 bytes of ASCII), staying safely under the limit for any script.
	const maxLen = 1500
	chunks := splitMessage(content, maxLen)
	for i, chunk := range chunks {
		chunkBody := maxSendBody{Text: chunk, Format: "markdown"}
		if i == len(chunks)-1 && len(kbAttachments) > 0 {
			chunkBody.Attachments = kbAttachments
		}
		if err := p.postMessage(ctx, rctx.chatID, &chunkBody); err != nil {
			return err
		}
		if len(chunks) > 1 && i < len(chunks)-1 {
			time.Sleep(300 * time.Millisecond)
		}
	}
	return nil
}

// postMessage sends one /messages request. It is the single HTTP call used by
// sendText, SendImage, SendFile — kept separate so retry/backoff for
// "attachment.not.ready" lives in one place.
func (p *Platform) postMessage(ctx context.Context, chatID string, body *maxSendBody) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	backoff := attachmentReadyDelay
	for attempt := 0; attempt <= attachmentReadyRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.apiBase+"/messages", bytes.NewReader(data))
		if err != nil {
			return err
		}
		p.setAuth(req)
		q := req.URL.Query()
		q.Set("chat_id", chatID)
		req.URL.RawQuery = q.Encode()
		req.Header.Set("Content-Type", "application/json")

		resp, err := p.client.Do(req)
		if err != nil {
			return fmt.Errorf("max: send message: %w", err)
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			return nil
		}
		if isAttachmentNotReady(respBody) && attempt < attachmentReadyRetries {
			slog.Debug("max: attachment not ready, retrying", "attempt", attempt+1, "backoff", backoff)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
			continue
		}
		slog.Warn("max: send message failed", "status", resp.StatusCode, "chat", chatID, "body", string(respBody))
		return fmt.Errorf("max: send message: HTTP %d: %s", resp.StatusCode, respBody)
	}
	return fmt.Errorf("max: send message: attachment not ready after %d retries", attachmentReadyRetries)
}

func isAttachmentNotReady(body []byte) bool {
	return bytes.Contains(body, []byte("attachment.not.ready")) ||
		bytes.Contains(body, []byte("not.ready"))
}

func (p *Platform) getMe(ctx context.Context) (name string, id int64, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.apiBase+"/me", nil)
	if err != nil {
		return "", 0, err
	}
	p.setAuth(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	var info struct {
		Name   string `json:"name"`
		UserID int64  `json:"user_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", 0, err
	}
	return info.Name, info.UserID, nil
}

func (p *Platform) getHandler() core.MessageHandler {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.handler
}

// setAuth adds the Authorization header with the bot token.
func (p *Platform) setAuth(req *http.Request) {
	req.Header.Set("Authorization", p.token)
}

// splitMessage chunks long text under maxLen Unicode code points (runes).
// Counting in runes (not bytes) is critical for non-ASCII content like
// Cyrillic, where each character is 2 bytes in UTF-8: a byte-based cut
// can split a multi-byte character mid-sequence and leave the next chunk
// with a malformed leading byte that the MAX server may reject.
//
// Cut preference:
//  1. paragraph break (consecutive \n\n) — keeps logical blocks together
//  2. single newline
//  3. word boundary (space)
//  4. exact maxLen — rune-safe by construction
//
// minCut prevents tiny chunks if a low-position newline is encountered.
func splitMessage(text string, maxLen int) []string {
	runes := []rune(text)
	if len(runes) <= maxLen {
		return []string{text}
	}

	var chunks []string
	for len(runes) > 0 {
		if len(runes) <= maxLen {
			chunks = append(chunks, string(runes))
			break
		}

		cut := maxLen
		minCut := maxLen / 4

		// 1. paragraph break
		for i := maxLen - 1; i > minCut; i-- {
			if runes[i] == '\n' && i+1 < len(runes) && runes[i+1] == '\n' {
				cut = i
				break
			}
		}
		// 2. single newline
		if cut == maxLen {
			for i := maxLen - 1; i > minCut; i-- {
				if runes[i] == '\n' {
					cut = i
					break
				}
			}
		}
		// 3. word boundary
		if cut == maxLen {
			for i := maxLen - 1; i > maxLen/2; i-- {
				if runes[i] == ' ' {
					cut = i
					break
				}
			}
		}
		// 4. fall through: cut at maxLen (rune-safe, never splits a code point)

		chunks = append(chunks, string(runes[:cut]))
		runes = runes[cut:]

		// trim leading whitespace on next chunk
		for len(runes) > 0 && (runes[0] == '\n' || runes[0] == ' ') {
			runes = runes[1:]
		}
	}
	return chunks
}

// Compile-time interface compliance assertions.
var (
	_ core.Platform                    = (*Platform)(nil)
	_ core.ImageSender                 = (*Platform)(nil)
	_ core.FileSender                  = (*Platform)(nil)
	_ core.AudioSender                 = (*Platform)(nil)
	_ core.InlineButtonSender          = (*Platform)(nil)
	_ core.MessageUpdater              = (*Platform)(nil)
	_ core.TypingIndicator             = (*Platform)(nil)
	_ core.FormattingInstructionProvider = (*Platform)(nil)
	_ core.ReplyContextReconstructor   = (*Platform)(nil)
)
