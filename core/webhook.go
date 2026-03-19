package core

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// WebhookServer exposes an HTTP endpoint for external systems
// (git hooks, CI/CD, file watchers, etc.) to trigger agent or shell actions.
type WebhookServer struct {
	port    int
	token   string
	path    string
	server  *http.Server
	engines map[string]*Engine
	mu      sync.RWMutex
}

// WebhookRequest is the JSON body for POST /hook.
type WebhookRequest struct {
	Event      string `json:"event,omitempty"`       // event name for logging (e.g. "git:commit")
	Project    string `json:"project,omitempty"`      // target project; optional if single project
	SessionKey string `json:"session_key"`            // target session key (required)
	Prompt     string `json:"prompt,omitempty"`       // agent prompt (mutually exclusive with exec)
	Exec       string `json:"exec,omitempty"`         // shell command (mutually exclusive with prompt)
	WorkDir    string `json:"work_dir,omitempty"`     // working dir for exec
	Silent     bool   `json:"silent,omitempty"`       // suppress notification
	Payload    any    `json:"payload,omitempty"`      // arbitrary extra data; appended to prompt context
}

func NewWebhookServer(port int, token, path string) *WebhookServer {
	if port <= 0 {
		port = 9111
	}
	if path == "" {
		path = "/hook"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return &WebhookServer{
		port:    port,
		token:   token,
		path:    path,
		engines: make(map[string]*Engine),
	}
}

func (ws *WebhookServer) RegisterEngine(name string, e *Engine) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.engines[name] = e
}

func (ws *WebhookServer) Start() {
	mux := http.NewServeMux()
	mux.HandleFunc(ws.path, ws.handleHook)

	addr := fmt.Sprintf(":%d", ws.port)
	ws.server = &http.Server{Addr: addr, Handler: mux}

	go func() {
		slog.Info("webhook: server started", "addr", addr, "path", ws.path)
		if err := ws.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("webhook: server error", "error", err)
		}
	}()
}

func (ws *WebhookServer) Stop() {
	if ws.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = ws.server.Shutdown(ctx)
	}
}

func (ws *WebhookServer) handleHook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	if !ws.authenticate(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req WebhookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.SessionKey == "" {
		http.Error(w, "session_key is required", http.StatusBadRequest)
		return
	}
	if req.Prompt == "" && req.Exec == "" {
		http.Error(w, "either prompt or exec is required", http.StatusBadRequest)
		return
	}
	if req.Prompt != "" && req.Exec != "" {
		http.Error(w, "prompt and exec are mutually exclusive", http.StatusBadRequest)
		return
	}

	engine, err := ws.resolveEngine(req.Project)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	eventName := req.Event
	if eventName == "" {
		eventName = "webhook"
	}

	slog.Info("webhook: received",
		"event", eventName,
		"project", req.Project,
		"session_key", req.SessionKey,
		"has_prompt", req.Prompt != "",
		"has_exec", req.Exec != "",
	)

	if req.Exec != "" {
		go ws.executeShell(engine, req, eventName)
	} else {
		prompt := req.Prompt
		if req.Payload != nil {
			if payloadJSON, err := json.Marshal(req.Payload); err == nil {
				prompt += "\n\nContext:\n```json\n" + string(payloadJSON) + "\n```"
			}
		}
		go ws.executePrompt(engine, req.SessionKey, prompt, req.Silent, eventName)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "accepted",
		"event":  eventName,
	})
}

func (ws *WebhookServer) authenticate(r *http.Request) bool {
	if ws.token == "" {
		return true
	}

	// Check Authorization: Bearer <token>
	if auth := r.Header.Get("Authorization"); auth != "" {
		if strings.HasPrefix(auth, "Bearer ") {
			got := auth[7:]
			return subtle.ConstantTimeCompare([]byte(got), []byte(ws.token)) == 1
		}
	}

	// Check X-Webhook-Token header
	if tok := r.Header.Get("X-Webhook-Token"); tok != "" {
		return subtle.ConstantTimeCompare([]byte(tok), []byte(ws.token)) == 1
	}

	// Check query parameter as fallback
	if tok := r.URL.Query().Get("token"); tok != "" {
		return subtle.ConstantTimeCompare([]byte(tok), []byte(ws.token)) == 1
	}

	return false
}

func (ws *WebhookServer) resolveEngine(project string) (*Engine, error) {
	ws.mu.RLock()
	defer ws.mu.RUnlock()

	if project != "" {
		e, ok := ws.engines[project]
		if !ok {
			return nil, fmt.Errorf("project %q not found", project)
		}
		return e, nil
	}

	if len(ws.engines) == 1 {
		for _, e := range ws.engines {
			return e, nil
		}
	}

	return nil, fmt.Errorf("project is required (multiple projects configured)")
}

func (ws *WebhookServer) executePrompt(engine *Engine, sessionKey, prompt string, silent bool, event string) {
	platformName := ""
	if idx := strings.Index(sessionKey, ":"); idx > 0 {
		platformName = sessionKey[:idx]
	}

	var targetPlatform Platform
	for _, p := range engine.platforms {
		if p.Name() == platformName {
			targetPlatform = p
			break
		}
	}
	if targetPlatform == nil {
		slog.Error("webhook: platform not found", "event", event, "platform", platformName)
		return
	}

	rc, ok := targetPlatform.(ReplyContextReconstructor)
	if !ok {
		slog.Error("webhook: platform does not support proactive messaging", "event", event, "platform", platformName)
		return
	}

	replyCtx, err := rc.ReconstructReplyCtx(sessionKey)
	if err != nil {
		slog.Error("webhook: reconstruct reply context failed", "event", event, "error", err)
		return
	}

	if !silent {
		engine.send(targetPlatform, replyCtx, fmt.Sprintf("🪝 %s", event))
	}

	msg := &Message{
		SessionKey: sessionKey,
		Platform:   platformName,
		UserID:     "webhook",
		UserName:   "webhook",
		Content:    prompt,
		ReplyCtx:   replyCtx,
	}

	session := engine.sessions.GetOrCreateActive(sessionKey)
	if !session.TryLock() {
		slog.Warn("webhook: session busy, queued prompt dropped", "event", event, "session_key", sessionKey)
		if !silent {
			engine.send(targetPlatform, replyCtx, fmt.Sprintf("🪝 ⚠️ session busy, skipped: %s", event))
		}
		return
	}

	engine.processInteractiveMessage(targetPlatform, msg, session)
	slog.Info("webhook: prompt executed", "event", event, "session_key", sessionKey)
}

const webhookShellTimeout = 5 * time.Minute

func (ws *WebhookServer) executeShell(engine *Engine, req WebhookRequest, event string) {
	sessionKey := req.SessionKey
	platformName := ""
	if idx := strings.Index(sessionKey, ":"); idx > 0 {
		platformName = sessionKey[:idx]
	}

	var targetPlatform Platform
	for _, p := range engine.platforms {
		if p.Name() == platformName {
			targetPlatform = p
			break
		}
	}
	if targetPlatform == nil {
		slog.Error("webhook: platform not found for shell exec", "event", event, "platform", platformName)
		return
	}

	rc, ok := targetPlatform.(ReplyContextReconstructor)
	if !ok {
		slog.Error("webhook: platform does not support proactive messaging", "event", event, "platform", platformName)
		return
	}

	replyCtx, err := rc.ReconstructReplyCtx(sessionKey)
	if err != nil {
		slog.Error("webhook: reconstruct reply context failed", "event", event, "error", err)
		return
	}

	if !req.Silent {
		engine.send(targetPlatform, replyCtx, fmt.Sprintf("🪝 %s: `%s`", event, truncateStr(req.Exec, 60)))
	}

	workDir := req.WorkDir
	if workDir == "" {
		if wd, ok := engine.agent.(interface{ GetWorkDir() string }); ok {
			workDir = wd.GetWorkDir()
		}
	}
	if workDir == "" {
		workDir, _ = os.Getwd()
	}

	ctx, cancel := context.WithTimeout(context.Background(), webhookShellTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", req.Exec)
	cmd.Dir = workDir
	output, execErr := cmd.CombinedOutput()

	result := strings.TrimSpace(string(output))

	if ctx.Err() == context.DeadlineExceeded {
		engine.send(targetPlatform, replyCtx, fmt.Sprintf("🪝 ⚠️ timeout: `%s`", truncateStr(req.Exec, 60)))
		return
	}

	if execErr != nil {
		msg := fmt.Sprintf("🪝 ❌ `%s`", truncateStr(req.Exec, 60))
		if result != "" {
			msg += "\n\n" + truncateStr(result, 3000)
		}
		msg += "\n\nerror: " + execErr.Error()
		engine.send(targetPlatform, replyCtx, msg)
	} else {
		if result == "" {
			result = "(no output)"
		}
		engine.send(targetPlatform, replyCtx, fmt.Sprintf("🪝 ✅ `%s`\n\n%s", truncateStr(req.Exec, 60), truncateStr(result, 3000)))
	}

	slog.Info("webhook: shell executed", "event", event, "session_key", sessionKey, "success", execErr == nil)
}
