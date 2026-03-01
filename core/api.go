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
)

// APIServer exposes a local Unix socket API for external tools (e.g. cron jobs)
// to send messages to active sessions.
type APIServer struct {
	socketPath string
	listener   net.Listener
	mux        *http.ServeMux
	engines    map[string]*Engine // project name → engine
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

	return s, nil
}

func (s *APIServer) SocketPath() string {
	return s.socketPath
}

func (s *APIServer) RegisterEngine(name string, e *Engine) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.engines[name] = e
}

func (s *APIServer) Start() {
	go func() {
		srv := &http.Server{Handler: s.mux}
		if err := srv.Serve(s.listener); err != nil && err != http.ErrServerClosed {
			slog.Error("api server error", "error", err)
		}
	}()
	slog.Info("api server started", "socket", s.socketPath)
}

func (s *APIServer) Stop() {
	s.listener.Close()
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
