// Package googlechat connects cc-connect to Google Chat.
//
// Google Chat has no native socket/long-poll inbound for self-hosted apps
// without a public endpoint. This adapter uses a registered Google Chat app
// whose Cloud Pub/Sub connection publishes Chat events to a topic:
//
//   - receive: `gws events +subscribe --subscription <sub>` pulls the Chat
//     app's Pub/Sub subscription and emits one JSON event per line on stdout
//     (no public IP needed). The subscription is fixed, so there is no
//     Workspace Events subscription expiry or per-restart resource leak.
//   - send:    the Chat app replies via the Chat REST API
//     (spaces.messages.create) authenticated as the app's service account
//     (chat.bot scope), so replies appear as the bot.
//
// Receiving still shells out to `gws` (a binary dependency, a deliberate
// deviation from cc-connect's native-Go norm), but sending is native Go since
// gws does not support service-account auth. The core.Platform interface is
// unchanged so the engine can later be swapped to a fully native client.
package googlechat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"

	"golang.org/x/oauth2/google"
)

// chatBotScope authorizes posting messages as the Chat app (app auth).
const chatBotScope = "https://www.googleapis.com/auth/chat.bot"

// chatAPIBase is the Chat REST API base; a var so tests can build URLs against it.
var chatAPIBase = "https://chat.googleapis.com/v1/"

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
	gwsPath      string
	subscription string // Pub/Sub subscription the Chat app publishes to
	allowFrom    string
	trigger      string
	sessionScope string // "space" (default) | "thread" | "user"

	botClient *http.Client // service-account authed client for sending (nil = cannot send)

	handler core.MessageHandler
	cancel  context.CancelFunc
}

// New builds a Google Chat platform from config options.
func New(opts map[string]any) (core.Platform, error) {
	gwsPath, _ := opts["gws_path"].(string)
	if strings.TrimSpace(gwsPath) == "" {
		gwsPath = "gws"
	}
	subscription, _ := opts["subscription"].(string)
	if strings.TrimSpace(subscription) == "" {
		return nil, fmt.Errorf("googlechat: subscription is required (the Pub/Sub subscription your Chat app publishes to)")
	}
	allowFrom, _ := opts["allow_from"].(string)
	trigger, _ := opts["trigger"].(string)
	credentialsFile, _ := opts["credentials_file"].(string)

	var botClient *http.Client
	if strings.TrimSpace(credentialsFile) != "" {
		keyBytes, err := os.ReadFile(credentialsFile)
		if err != nil {
			return nil, fmt.Errorf("googlechat: read credentials_file: %w", err)
		}
		conf, err := google.JWTConfigFromJSON(keyBytes, chatBotScope)
		if err != nil {
			return nil, fmt.Errorf("googlechat: parse service account credentials: %w", err)
		}
		botClient = conf.Client(context.Background())
	} else {
		slog.Warn("googlechat: credentials_file not set — the bot cannot send replies; set it to the Chat app's service account key")
	}

	core.CheckAllowFrom("googlechat", allowFrom)

	return &Platform{
		gwsPath:      gwsPath,
		subscription: subscription,
		allowFrom:    allowFrom,
		trigger:      trigger,
		sessionScope: normalizeSessionScope(opts["session_scope"]),
		botClient:    botClient,
	}, nil
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
		return "space"
	}
}

func (p *Platform) Name() string { return "googlechat" }

func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler

	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	go p.subscribeLoop(ctx)
	slog.Info("googlechat: started", "subscription", p.subscription, "scope", p.sessionScope)
	return nil
}

// subscribeLoop supervises the gws subprocess, restarting it with a small
// backoff if it exits unexpectedly while the context is still alive.
func (p *Platform) subscribeLoop(ctx context.Context) {
	const backoff = 5 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		if err := p.runSubscribe(ctx); err != nil && ctx.Err() == nil {
			slog.Error("googlechat: subscribe exited, restarting", "error", err, "backoff", backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

// runSubscribe runs one gws subprocess to completion, streaming stdout events
// to the handler and stderr to the log. gws authenticates the pull with its own
// OAuth login (the service-account key is only used for sending).
func (p *Platform) runSubscribe(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, p.gwsPath,
		"events", "+subscribe", "--subscription", p.subscription)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("googlechat: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("googlechat: stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("googlechat: start gws: %w", err)
	}

	go logStderr(stderr)
	p.scanEvents(stdout)

	return cmd.Wait()
}

// scanEvents reads NDJSON events (one per line) from the gws subprocess and
// dispatches parsed messages to the handler. The scanner buffer is enlarged
// because a single event carries the full Chat message resource.
func (p *Platform) scanEvents(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		msg, ok := p.parseEvent(line)
		if !ok {
			continue
		}
		p.handler(p, msg)
	}
	if err := scanner.Err(); err != nil {
		slog.Debug("googlechat: scan events ended", "error", err)
	}
}

func logStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		slog.Debug("googlechat: gws", "stderr", scanner.Text())
	}
}

// gwsEnvelope is the wrapper `gws events +subscribe` writes per Pub/Sub message:
// {"type":...,"source":...,"attributes":{...},"data":<decoded payload>}.
// A Chat app's Pub/Sub messages carry no CloudEvents attributes, so the real
// Chat event (with its own type + message) lives under "data".
type gwsEnvelope struct {
	Data chatAppEvent `json:"data"`
}

// chatAppEvent is the Google Chat app event payload.
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

// parseEvent converts one NDJSON line into a core.Message. The bool is false
// when the event should be ignored (not a new message, non-human sender,
// unauthorized, stale, or empty text).
func (p *Platform) parseEvent(line []byte) (*core.Message, bool) {
	var env gwsEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		slog.Debug("googlechat: parse event failed", "error", err)
		return nil, false
	}
	ev := env.Data
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

	content, ok := p.extractContent(m)
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

// extractContent returns the prompt text for a message. With a trigger word
// configured, only messages starting with it are handled and the prefix is
// stripped. Otherwise argumentText is used, which Google already strips of the
// @mention markup, falling back to text.
func (p *Platform) extractContent(m chatAppMessage) (string, bool) {
	if t := strings.TrimSpace(p.trigger); t != "" {
		text := strings.TrimSpace(m.Text)
		if !strings.HasPrefix(text, t) {
			return "", false
		}
		content := strings.TrimSpace(strings.TrimPrefix(text, t))
		return content, content != ""
	}
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
			return fmt.Sprintf("googlechat:%s:t:%s", space, thread)
		}
		return "googlechat:" + space
	case "user":
		return fmt.Sprintf("googlechat:%s:%s", space, user)
	default:
		return "googlechat:" + space
	}
}

// ReconstructReplyCtx rebuilds a reply context from a session key so proactive
// sends (cron, send-to-session, restart notices) can reach the right space and
// thread. Implements core.ReplyContextReconstructor.
func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	// googlechat:<space>  |  googlechat:<space>:t:<thread>  |  googlechat:<space>:<user>
	rest, ok := strings.CutPrefix(sessionKey, "googlechat:")
	if !ok {
		return nil, fmt.Errorf("googlechat: invalid session key %q", sessionKey)
	}
	if idx := strings.Index(rest, ":t:"); idx != -1 {
		return replyContext{space: rest[:idx], thread: rest[idx+3:]}, nil
	}
	// User-scoped keys append ":<user>" where user is "users/<id>"; strip a
	// trailing "users/..." segment to recover the bare space.
	if idx := strings.LastIndex(rest, ":users/"); idx != -1 {
		return replyContext{space: rest[:idx]}, nil
	}
	return replyContext{space: rest}, nil
}

// buildSendRequest builds the Chat REST API URL and JSON body to post content
// into rc's space. When a thread is known the reply is threaded (falling back
// to a new thread if that thread no longer accepts replies).
func buildSendRequest(rc replyContext, content string) (string, []byte, error) {
	body := map[string]any{"text": content}
	url := chatAPIBase + rc.space + "/messages"
	if rc.thread != "" {
		body["thread"] = map[string]any{"name": rc.thread}
		url += "?messageReplyOption=REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD"
	}
	b, err := json.Marshal(body)
	if err != nil {
		return "", nil, fmt.Errorf("googlechat: marshal body: %w", err)
	}
	return url, b, nil
}

func (p *Platform) post(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("googlechat: invalid reply context type %T", rctx)
	}
	if rc.space == "" {
		return fmt.Errorf("googlechat: missing space in reply context")
	}
	if p.botClient == nil {
		return fmt.Errorf("googlechat: cannot send: credentials_file (service account) is not configured")
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
	resp, err := p.botClient.Do(req)
	if err != nil {
		return fmt.Errorf("googlechat: send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("googlechat: send: status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func (p *Platform) Reply(ctx context.Context, rctx any, content string) error {
	return p.post(ctx, rctx, content)
}

func (p *Platform) Send(ctx context.Context, rctx any, content string) error {
	return p.post(ctx, rctx, content)
}

func (p *Platform) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	return nil
}
