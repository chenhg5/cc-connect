// Package googlechat connects cc-connect to Google Chat.
//
// Google Chat has no native socket/long-poll inbound for self-hosted apps
// without a public endpoint. This adapter therefore drives the `gws`
// (google-workspace-cli) binary as a subprocess:
//
//   - receive: `gws events +subscribe` pulls Workspace Events from a Pub/Sub
//     topic and emits one JSON event per line on stdout (no public IP needed).
//   - send:    `gws chat spaces messages create` posts a (optionally threaded)
//     reply back into the space.
//
// Wrapping a binary deviates from cc-connect's "platforms should be native Go"
// norm; the core.Platform interface is unchanged so the engine can later be
// swapped to a native client without touching core/ or agent/.
package googlechat

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const createdEventType = "google.workspace.chat.message.v1.created"

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
	gwsPath         string
	project         string
	target          string
	subscription    string
	allowFrom       string
	trigger         string
	sessionScope    string // "space" (default) | "thread" | "user"
	credentialsFile string

	handler core.MessageHandler
	cancel  context.CancelFunc
}

// New builds a Google Chat platform from config options.
func New(opts map[string]any) (core.Platform, error) {
	gwsPath, _ := opts["gws_path"].(string)
	if strings.TrimSpace(gwsPath) == "" {
		gwsPath = "gws"
	}
	project, _ := opts["project"].(string)
	target, _ := opts["target"].(string)
	if strings.TrimSpace(target) == "" {
		target = "//chat.googleapis.com/users/me"
	}
	subscription, _ := opts["subscription"].(string)
	allowFrom, _ := opts["allow_from"].(string)
	trigger, _ := opts["trigger"].(string)
	credentialsFile, _ := opts["credentials_file"].(string)

	if strings.TrimSpace(project) == "" && strings.TrimSpace(subscription) == "" {
		return nil, fmt.Errorf("googlechat: project is required (or set subscription to reuse an existing one)")
	}

	core.CheckAllowFrom("googlechat", allowFrom)

	return &Platform{
		gwsPath:         gwsPath,
		project:         project,
		target:          target,
		subscription:    subscription,
		allowFrom:       allowFrom,
		trigger:         trigger,
		sessionScope:    normalizeSessionScope(opts["session_scope"]),
		credentialsFile: credentialsFile,
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
	slog.Info("googlechat: started", "target", p.target, "scope", p.sessionScope)
	return nil
}

// subscribeArgs builds the `gws events +subscribe` argument list. When a
// subscription is configured it is reused (no Pub/Sub setup); otherwise a new
// topic/subscription is created for target+project.
func (p *Platform) subscribeArgs() []string {
	if strings.TrimSpace(p.subscription) != "" {
		return []string{"events", "+subscribe", "--subscription", p.subscription}
	}
	return []string{
		"events", "+subscribe",
		"--target", p.target,
		"--event-types", createdEventType,
		"--project", p.project,
	}
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
// to the handler and stderr to the log.
func (p *Platform) runSubscribe(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, p.gwsPath, p.subscribeArgs()...)
	if strings.TrimSpace(p.credentialsFile) != "" {
		cmd.Env = core.MergeEnv(os.Environ(), []string{
			"GOOGLE_WORKSPACE_CLI_CREDENTIALS_FILE=" + p.credentialsFile,
		})
	}

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

// chatEvent is the envelope emitted by `gws events +subscribe`.
type chatEvent struct {
	Type string      `json:"type"`
	Data chatMessage `json:"data"`
}

// chatMessage is the subset of the Google Chat Message resource we use.
type chatMessage struct {
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
// when the event should be ignored (wrong type, non-human sender, empty text).
func (p *Platform) parseEvent(line []byte) (*core.Message, bool) {
	var ev chatEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		slog.Debug("googlechat: parse event failed", "error", err)
		return nil, false
	}
	if ev.Type != createdEventType {
		return nil, false
	}
	m := ev.Data
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
// stripped (user-OAuth mode, no Chat App). Otherwise argumentText is used,
// which Google already strips of the @mention markup, falling back to text.
func (p *Platform) extractContent(m chatMessage) (string, bool) {
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
	// <space> is itself "spaces/<id>", so split off the known prefix first.
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

// buildCreateArgs builds the `gws chat spaces messages create` arguments to
// post content into rc's space. When a thread is known the reply is threaded
// (falling back to a new thread if that thread no longer accepts replies).
func buildCreateArgs(rc replyContext, content string) ([]string, error) {
	params := map[string]any{"parent": rc.space}
	body := map[string]any{"text": content}
	if rc.thread != "" {
		params["messageReplyOption"] = "REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD"
		body["thread"] = map[string]any{"name": rc.thread}
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("googlechat: marshal params: %w", err)
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("googlechat: marshal body: %w", err)
	}
	return []string{
		"chat", "spaces", "messages", "create",
		"--params", string(paramsJSON),
		"--json", string(bodyJSON),
	}, nil
}

func (p *Platform) post(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("googlechat: invalid reply context type %T", rctx)
	}
	if rc.space == "" {
		return fmt.Errorf("googlechat: missing space in reply context")
	}
	args, err := buildCreateArgs(rc, content)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, p.gwsPath, args...)
	if strings.TrimSpace(p.credentialsFile) != "" {
		cmd.Env = core.MergeEnv(os.Environ(), []string{
			"GOOGLE_WORKSPACE_CLI_CREDENTIALS_FILE=" + p.credentialsFile,
		})
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("googlechat: send: %w: %s", err, strings.TrimSpace(string(out)))
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
