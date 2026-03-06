package core

import (
	"encoding/json"
	"fmt"
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
	Project    string `json:"project"`
	SessionKey string `json:"session_key"`
	Message    string `json:"message"`
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
	os.Chmod(sockPath, 0o660)

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

	var req SendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Message == "" {
		http.Error(w, "message is required", http.StatusBadRequest)
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

	if err := engine.SendToSession(req.SessionKey, req.Message); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
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
	json.NewEncoder(w).Encode(result)
}

// ── Cron API ───────────────────────────────────────────────────

// CronAddRequest is the JSON body for POST /cron/add.
type CronAddRequest struct {
	Project     string `json:"project"`
	SessionKey  string `json:"session_key"`
	CronExpr    string `json:"cron_expr"`
	Prompt      string `json:"prompt"`
	Description string `json:"description"`
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
	if req.CronExpr == "" || req.Prompt == "" {
		http.Error(w, "cron_expr and prompt are required", http.StatusBadRequest)
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

	job := &CronJob{
		ID:          GenerateCronID(),
		Project:     project,
		SessionKey:  req.SessionKey,
		CronExpr:    req.CronExpr,
		Prompt:      req.Prompt,
		Description: req.Description,
		Enabled:     true,
	}
	job.CreatedAt = time.Now()

	if err := s.cron.AddJob(job); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(job)
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
	json.NewEncoder(w).Encode(jobs)
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
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
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
	json.NewEncoder(w).Encode(resp)
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
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
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
	json.NewEncoder(w).Encode(binding)
}
