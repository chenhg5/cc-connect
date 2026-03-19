package core

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// APIServer exposes a local Unix socket API for external tools (e.g. cron jobs)
// to send messages to active sessions.
type APIServer struct {
	socketPath string
	listener   net.Listener
	server     *http.Server
	mux        *http.ServeMux
	engines    map[string]*Engine // project name → engine
	cron       *CronScheduler
	relay      *RelayManager
	mu         sync.RWMutex
}

// SendRequest is the JSON body for POST /send.
type SendRequest struct {
	Project    string            `json:"project"`
	SessionKey string            `json:"session_key"`
	Message    string            `json:"message"`
	Images     []ImageAttachment `json:"images,omitempty"`
	Files      []FileAttachment  `json:"files,omitempty"`
}

// NewAPIServer creates an API server on a Unix socket.
func NewAPIServer(dataDir string) (*APIServer, error) {
	sockDir := filepath.Join(dataDir, "run")
	if err := os.MkdirAll(sockDir, 0o755); err != nil {
		return nil, fmt.Errorf("create run dir: %w", err)
	}
	sockPath := filepath.Join(sockDir, "api.sock")

	// Remove stale socket
	os.Remove(sockPath)

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("listen unix socket: %w", err)
	}
	if err := os.Chmod(sockPath, 0o600); err != nil {
		slog.Warn("api: chmod socket failed", "path", sockPath, "error", err)
	}

	s := &APIServer{
		socketPath: sockPath,
		listener:   listener,
		mux:        http.NewServeMux(),
		engines:    make(map[string]*Engine),
	}
	s.mux.HandleFunc("/send", s.handleSend)
	s.mux.HandleFunc("/sessions", s.handleSessions)
	s.mux.HandleFunc("/cron/add", s.handleCronAdd)
	s.mux.HandleFunc("/cron/list", s.handleCronList)
	s.mux.HandleFunc("/cron/del", s.handleCronDel)
	s.mux.HandleFunc("/relay/send", s.handleRelaySend)
	s.mux.HandleFunc("/relay/bind", s.handleRelayBind)
	s.mux.HandleFunc("/relay/binding", s.handleRelayBinding)

	return s, nil
}

func (s *APIServer) SocketPath() string {
	return s.socketPath
}

func (s *APIServer) RegisterEngine(name string, e *Engine) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.engines[name] = e
	if s.relay != nil {
		s.relay.RegisterEngine(name, e)
	}
}

func (s *APIServer) SetRelayManager(rm *RelayManager) {
	s.relay = rm
}

func (s *APIServer) RelayManager() *RelayManager {
	return s.relay
}

func (s *APIServer) SetCronScheduler(cs *CronScheduler) {
	s.cron = cs
}

func (s *APIServer) Start() {
	s.server = &http.Server{Handler: s.mux}
	go func() {
		if err := s.server.Serve(s.listener); err != nil && err != http.ErrServerClosed {
			slog.Error("api server error", "error", err)
		}
	}()
	slog.Info("api server started", "socket", s.socketPath)
}

func (s *APIServer) Stop() {
	s.server.Close()
	os.Remove(s.socketPath)
}

func (s *APIServer) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	const maxSendBody = 52 << 20 // 52 MB (slightly above max attachment to account for base64 overhead)
	var req SendRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxSendBody)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Message == "" && len(req.Images) == 0 && len(req.Files) == 0 {
		http.Error(w, "message or attachment is required", http.StatusBadRequest)
		return
	}

	s.mu.RLock()
	engine, ok := s.engines[req.Project]
	s.mu.RUnlock()

	if !ok {
		// If only one engine, use it by default
		s.mu.RLock()
		if len(s.engines) == 1 {
			for _, e := range s.engines {
				engine = e
				ok = true
			}
		}
		s.mu.RUnlock()
	}

	if !ok {
		http.Error(w, fmt.Sprintf("project %q not found", req.Project), http.StatusNotFound)
		return
	}

	if err := engine.SendToSessionWithAttachments(req.SessionKey, req.Message, req.Images, req.Files); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok"}); err != nil {
		slog.Error("api: failed to encode response", "error", err)
	}
}

func (s *APIServer) handleSessions(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	type sessionInfo struct {
		Project    string `json:"project"`
		SessionKey string `json:"session_key"`
		Platform   string `json:"platform"`
	}

	var result []sessionInfo
	for name, e := range s.engines {
		e.interactiveMu.Lock()
		for key, state := range e.interactiveStates {
			if state.platform != nil {
				result = append(result, sessionInfo{
					Project:    name,
					SessionKey: key,
					Platform:   state.platform.Name(),
				})
			}
		}
		e.interactiveMu.Unlock()
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		slog.Error("api: failed to encode response", "error", err)
	}
}

// ── Cron API ───────────────────────────────────────────────────

// CronAddRequest is the JSON body for POST /cron/add.
type CronAddRequest struct {
	Project     string `json:"project"`
	SessionKey  string `json:"session_key"`
	CronExpr    string `json:"cron_expr"`
	Prompt      string `json:"prompt"`
	Exec        string `json:"exec"`
	WorkDir     string `json:"work_dir"`
	Description string `json:"description"`
	Silent      *bool  `json:"silent,omitempty"`
}

func (s *APIServer) handleCronAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.cron == nil {
		http.Error(w, "cron scheduler not available", http.StatusServiceUnavailable)
		return
	}

	var req CronAddRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.CronExpr == "" {
		http.Error(w, "cron_expr is required", http.StatusBadRequest)
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

	// Resolve project: use provided, or pick single engine
	project := req.Project
	if project == "" {
		s.mu.RLock()
		if len(s.engines) == 1 {
			for name := range s.engines {
				project = name
			}
		}
		s.mu.RUnlock()
	}
	if project == "" {
		http.Error(w, "project is required (multiple projects configured)", http.StatusBadRequest)
		return
	}

	// Resolve session_key: use provided, or auto-detect from active sessions
	sessionKey := req.SessionKey
	if sessionKey == "" {
		s.mu.RLock()
		engine := s.engines[project]
		s.mu.RUnlock()
		if engine != nil {
			keys := engine.ActiveSessionKeys()
			if len(keys) == 1 {
				sessionKey = keys[0]
				slog.Debug("auto-detected session_key for cron job", "session_key", sessionKey)
			}
		}
	}
	if sessionKey == "" {
		http.Error(w, "session_key is required: set CC_SESSION_KEY env, pass --session-key, or ensure exactly one active session exists", http.StatusBadRequest)
		return
	}

	job := &CronJob{
		ID:          GenerateCronID(),
		Project:     project,
		SessionKey:  sessionKey,
		CronExpr:    req.CronExpr,
		Prompt:      req.Prompt,
		Exec:        req.Exec,
		WorkDir:     req.WorkDir,
		Description: req.Description,
		Enabled:     true,
		Silent:      req.Silent,
	}
	job.CreatedAt = time.Now()

	if err := s.cron.AddJob(job); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(job); err != nil {
		slog.Error("api: failed to encode response", "error", err)
	}
}

func (s *APIServer) handleCronList(w http.ResponseWriter, r *http.Request) {
	if s.cron == nil {
		http.Error(w, "cron scheduler not available", http.StatusServiceUnavailable)
		return
	}

	project := r.URL.Query().Get("project")
	var jobs []*CronJob
	if project != "" {
		jobs = s.cron.Store().ListByProject(project)
	} else {
		jobs = s.cron.Store().List()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(jobs)
}

func (s *APIServer) handleCronDel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.cron == nil {
		http.Error(w, "cron scheduler not available", http.StatusServiceUnavailable)
		return
	}

	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.ID == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}

	if s.cron.RemoveJob(req.ID) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	} else {
		http.Error(w, fmt.Sprintf("job %q not found", req.ID), http.StatusNotFound)
	}
}

// ── Relay API ──────────────────────────────────────────────────

func (s *APIServer) handleRelaySend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.relay == nil {
		http.Error(w, "relay not available", http.StatusServiceUnavailable)
		return
	}

	var req RelayRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.To == "" || req.Message == "" || req.SessionKey == "" {
		http.Error(w, "to, session_key, and message are required", http.StatusBadRequest)
		return
	}

	resp, err := s.relay.Send(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *APIServer) handleRelayBind(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.relay == nil {
		http.Error(w, "relay not available", http.StatusServiceUnavailable)
		return
	}

	var req struct {
		Platform string            `json:"platform"`
		ChatID   string            `json:"chat_id"`
		Bots     map[string]string `json:"bots"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.ChatID == "" || len(req.Bots) < 2 {
		http.Error(w, "chat_id and at least 2 bots are required", http.StatusBadRequest)
		return
	}

	s.relay.Bind(req.Platform, req.ChatID, req.Bots)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *APIServer) handleRelayBinding(w http.ResponseWriter, r *http.Request) {
	if s.relay == nil {
		http.Error(w, "relay not available", http.StatusServiceUnavailable)
		return
	}
	chatID := r.URL.Query().Get("chat_id")
	if chatID == "" {
		http.Error(w, "chat_id is required", http.StatusBadRequest)
		return
	}
	binding := s.relay.GetBinding(chatID)
	if binding == nil {
		http.Error(w, "no binding found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(binding)
}
