// Package googlechat connects cc-connect to Google Chat.
//
// Google Chat has no native socket/long-poll inbound for self-hosted apps
// without a public endpoint. This adapter uses a registered Google Chat app
// whose Cloud Pub/Sub connection publishes Chat events to a topic:
//
//   - receive: cc-connect pulls the Chat app's Pub/Sub subscription with a
//     streaming pull (no public IP needed). The subscription is fixed, so there
//     is no Workspace Events subscription expiry or per-restart resource leak.
//   - send:    the Chat app replies via the Chat REST API
//     (spaces.messages.create) authenticated as the app's service account
//     (chat.bot scope), so replies appear as the bot.
//
// Both directions are native Go: receive uses cloud.google.com/go/pubsub and
// send uses net/http, authenticated by the same service-account key.
package googlechat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"

	"cloud.google.com/go/pubsub/v2"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
)

// chatBotScope authorizes posting messages as the Chat app (app auth).
const chatBotScope = "https://www.googleapis.com/auth/chat.bot"

// sessionKeyPrefix and threadSep are used by both buildSessionKey and
// ReconstructReplyCtx so the encode/decode pair stays in sync.
const (
	sessionKeyPrefix = "googlechat:"
	threadSep        = ":t:"
)

// chatAPIBase is the Chat REST API base; a var so tests can build URLs against it.
var chatAPIBase = "https://chat.googleapis.com/v1/"

// chatUploadBase is the Chat media-upload endpoint base; a var so tests can override it.
var chatUploadBase = "https://chat.googleapis.com/upload/v1/"

func init() {
	core.RegisterPlatform("googlechat", New)
}

// replyContext carries the platform-specific data needed to reply: the space
// to post into and, when known, the thread to reply within.
type replyContext struct {
	space  string // e.g. "spaces/AAAA"
	thread string // e.g. "spaces/AAAA/threads/XXXX" (empty = top-level)
}

type Platform struct {
	subscription    string // full resource name: projects/<p>/subscriptions/<s>
	projectID       string // parsed from subscription, for the Pub/Sub client
	credentialsFile string // service-account key, used for both receive and send
	tokenSource     oauth2.TokenSource
	allowFrom       string
	sessionScope    string // "space" (default) | "thread" | "user"

	botClient *http.Client // service-account authed client for sending
	psClient  *pubsub.Client

	handler core.MessageHandler
	cancel  context.CancelFunc
}

// New builds a Google Chat platform from config options.
func New(opts map[string]any) (core.Platform, error) {
	subscription, _ := opts["subscription"].(string)
	subscription = strings.TrimSpace(subscription)
	if subscription == "" {
		return nil, fmt.Errorf("googlechat: subscription is required (the Pub/Sub subscription your Chat app publishes to)")
	}
	projectID, err := projectFromSubscription(subscription)
	if err != nil {
		return nil, err
	}

	credentialsFile, _ := opts["credentials_file"].(string)
	credentialsFile = strings.TrimSpace(credentialsFile)
	if credentialsFile == "" {
		return nil, fmt.Errorf("googlechat: credentials_file is required (the Chat app's service-account key, used to pull events and send replies)")
	}
	keyBytes, err := os.ReadFile(credentialsFile)
	if err != nil {
		return nil, fmt.Errorf("googlechat: read credentials_file: %w", err)
	}
	conf, err := google.JWTConfigFromJSON(keyBytes,
		chatBotScope, "https://www.googleapis.com/auth/pubsub")
	if err != nil {
		return nil, fmt.Errorf("googlechat: parse service account credentials: %w", err)
	}
	botClient := conf.Client(context.Background())

	allowFrom, _ := opts["allow_from"].(string)

	core.CheckAllowFrom("googlechat", allowFrom)

	return &Platform{
		subscription:    subscription,
		projectID:       projectID,
		credentialsFile: credentialsFile,
		tokenSource:     conf.TokenSource(context.Background()),
		allowFrom:       allowFrom,
		sessionScope:    normalizeSessionScope(opts["session_scope"]),
		botClient:       botClient,
	}, nil
}

// projectFromSubscription extracts the project ID from a Pub/Sub subscription
// resource name so the Pub/Sub client can be created for the right project.
func projectFromSubscription(sub string) (string, error) {
	parts := strings.Split(sub, "/")
	if len(parts) == 4 && parts[0] == "projects" && parts[2] == "subscriptions" {
		return parts[1], nil
	}
	return "", fmt.Errorf("googlechat: subscription must be of the form projects/<project>/subscriptions/<name>, got %q", sub)
}

// normalizeSessionScope resolves session_scope to "space" | "thread" | "user",
// defaulting to "space".
func normalizeSessionScope(raw any) string {
	s, _ := raw.(string)
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "thread":
		return "thread"
	case "user":
		return "user"
	case "space", "":
		return "space"
	default:
		slog.Warn("googlechat: unknown session_scope, using \"space\"", "value", s)
		return "space"
	}
}

func (p *Platform) Name() string { return "googlechat" }

func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler

	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	client, err := pubsub.NewClient(ctx, p.projectID, option.WithTokenSource(p.tokenSource))
	if err != nil {
		cancel()
		return fmt.Errorf("googlechat: create pubsub client: %w", err)
	}
	p.psClient = client

	go p.receiveLoop(ctx)
	slog.Info("googlechat: started", "subscription", p.subscription, "scope", p.sessionScope)
	return nil
}

// receiveLoop runs a streaming pull on the subscription, restarting with a small
// backoff if Receive returns an error while the context is still alive.
func (p *Platform) receiveLoop(ctx context.Context) {
	const backoff = 5 * time.Second
	sub := p.psClient.Subscriber(p.subscription)
	for {
		if ctx.Err() != nil {
			return
		}
		err := sub.Receive(ctx, func(_ context.Context, m *pubsub.Message) {
			p.handleMessage(m)
		})
		if err != nil && ctx.Err() == nil {
			slog.Error("googlechat: receive exited, restarting", "error", err, "backoff", backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

// ackable is the subset of *pubsub.Message used by dispatchMessage, allowing
// the dispatch logic to be tested without a real Pub/Sub client.
type ackable interface {
	Ack()
	Nack()
}

// handleMessage is the Pub/Sub receive callback; it delegates to dispatchMessage.
func (p *Platform) handleMessage(m *pubsub.Message) {
	p.dispatchMessage(m, m.Data)
}

// dispatchMessage parses data and routes it to the handler.
// Non-message events (e.g. ADDED_TO_SPACE) are acked immediately so they are
// not redelivered. For valid messages, ack happens after the handler returns;
// if the handler panics the message is nacked so Pub/Sub can redeliver it.
func (p *Platform) dispatchMessage(m ackable, data []byte) {
	msg, ok := p.parseEvent(data)
	if !ok {
		m.Ack()
		return
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Error("googlechat: handler panic", "recover", r)
			m.Nack()
			return
		}
		m.Ack()
	}()
	p.handler(p, msg)
}

// chatAppEvent is the Google Chat app event payload that the Chat app publishes
// to its Pub/Sub topic. It arrives as the Pub/Sub message data directly.
type chatAppEvent struct {
	Type    string         `json:"type"` // MESSAGE, ADDED_TO_SPACE, REMOVED_FROM_SPACE, ...
	Message chatAppMessage `json:"message"`
}

// chatAppMessage is the subset of the Chat Message resource we use.
type chatAppMessage struct {
	Name         string `json:"name"`
	Text         string `json:"text"`
	ArgumentText string `json:"argumentText"`
	CreateTime   string `json:"createTime"`
	Sender       struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
		Type        string `json:"type"`
	} `json:"sender"`
	Space struct {
		Name string `json:"name"`
	} `json:"space"`
	Thread struct {
		Name string `json:"name"`
	} `json:"thread"`
}

// parseEvent converts one Pub/Sub message payload into a core.Message. The bool
// is false when the event should be ignored (not a new message, non-human
// sender, unauthorized, stale, or empty text).
func (p *Platform) parseEvent(data []byte) (*core.Message, bool) {
	var ev chatAppEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		slog.Debug("googlechat: parse event failed", "error", err)
		return nil, false
	}
	if ev.Type != "MESSAGE" {
		return nil, false
	}
	m := ev.Message
	// Only react to human messages; skipping app/bot senders prevents the
	// adapter from replying to its own posts.
	if !strings.EqualFold(m.Sender.Type, "HUMAN") {
		return nil, false
	}
	if !core.AllowList(p.allowFrom, m.Sender.Name) {
		slog.Debug("googlechat: message from unauthorized sender", "sender", m.Sender.Name)
		return nil, false
	}
	// Drop messages predating startup so a restart does not replay backlog.
	if t, err := time.Parse(time.RFC3339, m.CreateTime); err == nil && core.IsOldMessage(t) {
		slog.Debug("googlechat: ignoring old message after restart", "create_time", m.CreateTime)
		return nil, false
	}

	content, ok := extractContent(m)
	if !ok {
		return nil, false
	}

	space := m.Space.Name
	thread := m.Thread.Name
	return &core.Message{
		SessionKey: p.buildSessionKey(space, m.Sender.Name, thread),
		Platform:   "googlechat",
		MessageID:  m.Name,
		UserID:     m.Sender.Name,
		UserName:   m.Sender.DisplayName,
		ChatName:   space,
		Content:    content,
		ReplyCtx:   replyContext{space: space, thread: thread},
	}, true
}

// extractContent returns the prompt text for a message. argumentText is used
// (Google strips the @mention markup from it), falling back to text.
func extractContent(m chatAppMessage) (string, bool) {
	content := strings.TrimSpace(m.ArgumentText)
	if content == "" {
		content = strings.TrimSpace(m.Text)
	}
	return content, content != ""
}

// buildSessionKey derives the engine session key per session_scope:
//   - "space":  one session per space          -> googlechat:<space>
//   - "thread": one session per thread          -> googlechat:<space>:t:<thread>
//   - "user":   one session per (space, sender)  -> googlechat:<space>:<user>
//
// "thread" falls back to the space key when the message has no thread.
func (p *Platform) buildSessionKey(space, user, thread string) string {
	switch p.sessionScope {
	case "thread":
		if thread != "" {
			return sessionKeyPrefix + space + threadSep + thread
		}
		return sessionKeyPrefix + space
	case "user":
		return sessionKeyPrefix + space + ":" + user
	default:
		return sessionKeyPrefix + space
	}
}

// ReconstructReplyCtx rebuilds a reply context from a session key so proactive
// sends (cron, send-to-session, restart notices) can reach the right space and
// thread. Implements core.ReplyContextReconstructor.
func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	// googlechat:<space>  |  googlechat:<space>:t:<thread>  |  googlechat:<space>:<user>
	rest, ok := strings.CutPrefix(sessionKey, sessionKeyPrefix)
	if !ok {
		return nil, fmt.Errorf("googlechat: invalid session key %q", sessionKey)
	}
	if idx := strings.Index(rest, threadSep); idx != -1 {
		return replyContext{space: rest[:idx], thread: rest[idx+len(threadSep):]}, nil
	}
	// User-scoped keys append ":<user>" where user is "users/<id>"; strip a
	// trailing "users/..." segment to recover the bare space.
	if idx := strings.LastIndex(rest, ":users/"); idx != -1 {
		return replyContext{space: rest[:idx]}, nil
	}
	return replyContext{space: rest}, nil
}

// httpErrorBody reads up to 2048 bytes from resp.Body, closes it, and returns
// an error combining prefix, status code, and the response snippet.
func httpErrorBody(resp *http.Response, prefix string) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if err := resp.Body.Close(); err != nil {
		return fmt.Errorf("%s: status %d: %s (close body: %v)", prefix, resp.StatusCode, strings.TrimSpace(string(b)), err)
	}
	return fmt.Errorf("%s: status %d: %s", prefix, resp.StatusCode, strings.TrimSpace(string(b)))
}

// coalesce returns s if non-empty, otherwise def.
func coalesce(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

// messageURL returns the Chat messages endpoint for rc's space, appending the
// messageReplyOption query when a thread is known.
func messageURL(rc replyContext) string {
	u := chatAPIBase + rc.space + "/messages"
	if rc.thread != "" {
		u += "?messageReplyOption=REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD"
	}
	return u
}

// applyThread adds the thread field to body when rc has a thread.
func applyThread(body map[string]any, rc replyContext) {
	if rc.thread != "" {
		body["thread"] = map[string]any{"name": rc.thread}
	}
}

// buildSendRequest builds the Chat REST API URL and JSON body to post content
// into rc's space. When a thread is known the reply is threaded (falling back
// to a new thread if that thread no longer accepts replies).
func buildSendRequest(rc replyContext, content string) (string, []byte, error) {
	body := map[string]any{"text": content}
	applyThread(body, rc)
	b, err := json.Marshal(body)
	if err != nil {
		return "", nil, fmt.Errorf("googlechat: marshal body: %w", err)
	}
	return messageURL(rc), b, nil
}

// doRequest executes req using botClient and returns the response on success.
// On non-2xx it reads the error body, closes it, and returns an error.
// The caller is responsible for draining and closing resp.Body on success.
func (p *Platform) doRequest(req *http.Request) (*http.Response, error) {
	resp, err := p.botClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, httpErrorBody(resp, fmt.Sprintf("googlechat: %s %s", req.Method, req.URL.Path))
	}
	return resp, nil
}

func (p *Platform) post(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("googlechat: invalid reply context type %T", rctx)
	}
	if rc.space == "" {
		return fmt.Errorf("googlechat: missing space in reply context")
	}
	url, body, err := buildSendRequest(rc, content)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("googlechat: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.doRequest(req)
	if err != nil {
		return err
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		_ = resp.Body.Close()
		return fmt.Errorf("googlechat: drain response body: %w", err)
	}
	if err := resp.Body.Close(); err != nil {
		return fmt.Errorf("googlechat: close response body: %w", err)
	}
	return nil
}

func (p *Platform) Reply(ctx context.Context, rctx any, content string) error {
	return p.post(ctx, rctx, content)
}

func (p *Platform) Send(ctx context.Context, rctx any, content string) error {
	return p.post(ctx, rctx, content)
}

// uploadAttachment uploads raw bytes to the Chat media endpoint using a
// multipart/related request and returns the attachmentDataRef resource name.
func (p *Platform) uploadAttachment(ctx context.Context, space, filename, mimeType string, data []byte) (string, error) {
	buf := bytes.NewBuffer(make([]byte, 0, 256+len(data)))
	mw := multipart.NewWriter(buf)

	metaPart, err := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {"application/json; charset=UTF-8"}})
	if err != nil {
		return "", fmt.Errorf("googlechat: upload: create metadata part: %w", err)
	}
	if err := json.NewEncoder(metaPart).Encode(map[string]string{"filename": filename}); err != nil {
		return "", fmt.Errorf("googlechat: upload: encode metadata: %w", err)
	}

	mediaPart, err := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {mimeType}})
	if err != nil {
		return "", fmt.Errorf("googlechat: upload: create media part: %w", err)
	}
	if _, err := mediaPart.Write(data); err != nil {
		return "", fmt.Errorf("googlechat: upload: write media: %w", err)
	}
	if err := mw.Close(); err != nil {
		return "", fmt.Errorf("googlechat: upload: finalize multipart body: %w", err)
	}

	uploadURL := chatUploadBase + space + "/attachments:upload?uploadType=multipart"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, buf)
	if err != nil {
		return "", fmt.Errorf("googlechat: upload: build request: %w", err)
	}
	req.Header.Set("Content-Type", "multipart/related; boundary="+mw.Boundary())

	resp, err := p.doRequest(req)
	if err != nil {
		return "", err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Warn("googlechat: close upload response body", "error", err)
		}
	}()

	var result struct {
		AttachmentDataRef struct {
			ResourceName string `json:"resourceName"`
		} `json:"attachmentDataRef"`
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body) }()
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("googlechat: upload: decode response: %w", err)
	}
	if result.AttachmentDataRef.ResourceName == "" {
		return "", fmt.Errorf("googlechat: upload: empty resourceName in response")
	}
	return result.AttachmentDataRef.ResourceName, nil
}

// buildAttachmentRequest builds the Chat REST API URL and JSON body to post a
// message that references an already-uploaded attachment (by resource name).
// Threading behaviour mirrors buildSendRequest.
func buildAttachmentRequest(rc replyContext, resourceName string) (string, []byte, error) {
	body := map[string]any{
		"attachment": []map[string]any{
			{"attachmentDataRef": map[string]any{"resourceName": resourceName}},
		},
	}
	applyThread(body, rc)
	b, err := json.Marshal(body)
	if err != nil {
		return "", nil, fmt.Errorf("googlechat: marshal attachment body: %w", err)
	}
	return messageURL(rc), b, nil
}

// postAttachment uploads data then creates a Chat message carrying the
// attachmentDataRef. Shared by SendImage and SendFile.
func (p *Platform) postAttachment(ctx context.Context, rc replyContext, filename, mimeType string, data []byte) error {
	if rc.space == "" {
		return fmt.Errorf("googlechat: missing space in reply context")
	}
	resourceName, err := p.uploadAttachment(ctx, rc.space, filename, mimeType, data)
	if err != nil {
		return err
	}
	url, body, err := buildAttachmentRequest(rc, resourceName)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("googlechat: attachment message: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.doRequest(req)
	if err != nil {
		return err
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		_ = resp.Body.Close()
		return fmt.Errorf("googlechat: drain attachment create response body: %w", err)
	}
	if err := resp.Body.Close(); err != nil {
		return fmt.Errorf("googlechat: close attachment create response body: %w", err)
	}
	return nil
}

// SendImage uploads an image and posts it as a Chat message attachment.
// Implements core.ImageSender.
func (p *Platform) SendImage(ctx context.Context, rctx any, img core.ImageAttachment) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("googlechat: SendImage: invalid reply context type %T", rctx)
	}
	return p.postAttachment(ctx, rc,
		coalesce(img.FileName, "image.png"),
		coalesce(img.MimeType, "image/png"),
		img.Data)
}

// SendFile uploads a file and posts it as a Chat message attachment.
// Implements core.FileSender.
func (p *Platform) SendFile(ctx context.Context, rctx any, file core.FileAttachment) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("googlechat: SendFile: invalid reply context type %T", rctx)
	}
	return p.postAttachment(ctx, rc,
		coalesce(file.FileName, "attachment"),
		coalesce(file.MimeType, "application/octet-stream"),
		file.Data)
}

var _ core.ImageSender = (*Platform)(nil)
var _ core.FileSender = (*Platform)(nil)
var _ core.ReplyContextReconstructor = (*Platform)(nil)

func (p *Platform) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	if p.psClient != nil {
		return p.psClient.Close()
	}
	return nil
}

// FormattingInstructions returns Google Chat text-formatting guidance for the agent.
func (p *Platform) FormattingInstructions() string {
	return `You are responding in Google Chat. Use Google Chat's text formatting, NOT standard Markdown:
- Bold: *bold* (single asterisks)
- Italic: _italic_
- Strikethrough: ~text~
- Inline code: ` + "`text`" + `
- Code block: ` + "```text```" + `
- Block quote: >text
- Lists: use - or * prefix normally
- Do NOT use ## headings — Google Chat does not render them. Use *bold* on its own line instead.
- Do NOT use [text](url) Markdown links.
  - To auto-link a URL: paste the raw URL directly — Google Chat will linkify it.
  - To link with display text: <https://example.com|display text>`
}

// compile-time assertion that *Platform implements core.FormattingInstructionProvider.
var _ core.FormattingInstructionProvider = (*Platform)(nil)
