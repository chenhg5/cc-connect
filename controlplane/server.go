package controlplane

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	appconfig "github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/controlstore"
	"github.com/chenhg5/cc-connect/deploy"
	"github.com/chenhg5/cc-connect/runtimeprotocol"
)

const sessionCookieName = "cc_connect_session"

type Config struct {
	ListenAddress string
	PublicURL     string
	ServerSocket  string
	RuntimeSocket string
	AppDirectory  string
	Assets        fs.FS
}

type Server struct {
	config Config
	store  *controlstore.Store
	broker *Broker

	publicServer     *http.Server
	internalServer   *http.Server
	publicListener   net.Listener
	internalListener net.Listener
	proxy            *httputil.ReverseProxy
	loginLimiter     *loginLimiter
	attachments      *AttachmentStore
	supervisor       *Supervisor
	deployment       *DeploymentManager
	closeOnce        sync.Once
}

func New(config Config, store *controlstore.Store, broker *Broker) (*Server, error) {
	if store == nil || broker == nil {
		return nil, errors.New("control plane: store and broker are required")
	}
	if strings.TrimSpace(config.ListenAddress) == "" {
		config.ListenAddress = "127.0.0.1:9820"
	}
	if strings.TrimSpace(config.ServerSocket) == "" || strings.TrimSpace(config.RuntimeSocket) == "" || strings.TrimSpace(config.AppDirectory) == "" {
		return nil, errors.New("control plane: server and runtime socket paths and app directory are required")
	}
	proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: "cc-connect-server"})
	proxy.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", config.ServerSocket)
	}}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		slog.Warn("control proxy failed", "error", err)
		writeJSON(w, http.StatusBadGateway, false, nil, "业务进程当前不可用")
	}
	attachments, err := NewAttachmentStore(filepath.Join(config.AppDirectory, "attachments"))
	if err != nil {
		return nil, err
	}
	broker.setAttachmentStore(attachments)
	return &Server{
		config: config, store: store, broker: broker, proxy: proxy, loginLimiter: newLoginLimiter(),
		attachments: attachments,
	}, nil
}

func (s *Server) SetSupervisor(supervisor *Supervisor)            { s.supervisor = supervisor }
func (s *Server) SetDeploymentManager(manager *DeploymentManager) { s.deployment = manager }

func (s *Server) Start() error {
	listener, err := net.Listen("tcp", s.config.ListenAddress)
	if err != nil {
		return fmt.Errorf("control plane: listen %s: %w", s.config.ListenAddress, err)
	}
	if err := prepareUnixSocket(s.config.RuntimeSocket); err != nil {
		_ = listener.Close()
		return err
	}
	internal, err := net.Listen("unix", s.config.RuntimeSocket)
	if err != nil {
		_ = listener.Close()
		return fmt.Errorf("control plane: listen runtime socket: %w", err)
	}
	if err := os.Chmod(s.config.RuntimeSocket, 0o660); err != nil {
		_ = listener.Close()
		_ = internal.Close()
		return fmt.Errorf("control plane: protect runtime socket: %w", err)
	}
	s.publicListener = listener
	s.internalListener = internal
	s.publicServer = &http.Server{Handler: s.publicHandler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 75 * time.Second}
	s.internalServer = &http.Server{Handler: s.internalHandler(), ReadHeaderTimeout: 10 * time.Second}
	go serveHTTP("public", s.publicServer, listener)
	go serveHTTP("runtime unix", s.internalServer, internal)
	return nil
}

func serveHTTP(name string, server *http.Server, listener net.Listener) {
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("control server stopped unexpectedly", "listener", name, "error", err)
	}
}

func prepareUnixSocket(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("control plane: create runtime socket directory: %w", err)
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("control plane: inspect runtime socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("control plane: runtime socket path exists and is not a socket: %s", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("control plane: remove stale runtime socket: %w", err)
	}
	return nil
}

func (s *Server) publicHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/setup", s.handleSetup)
	mux.HandleFunc("/api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("/api/v1/auth/logout", s.requireSession(s.handleLogout))
	mux.HandleFunc("/api/v1/auth/session", s.requireSession(s.handleSession))
	mux.HandleFunc("/api/v1/devices", s.requireSession(s.handleDevices))
	mux.HandleFunc("/api/v1/devices/pairing-code", s.requireSession(s.handlePairingCode))
	mux.HandleFunc("/api/v1/devices/", s.requireSession(s.handleDevice))
	mux.HandleFunc("/api/v1/deploy/dashboard", s.requireSession(s.handleDeployDashboard))
	mux.HandleFunc("/api/v1/deploy/preflight-operations", s.requireSession(s.handlePreflightOperations))
	mux.HandleFunc("/api/v1/deploy/runs", s.requireSession(s.handleDeployRuns))
	mux.HandleFunc("/api/v1/deploy/runs/", s.requireSession(s.handleDeployRun))
	mux.HandleFunc("/api/v1/service/status", s.requireSession(s.handleServiceStatus))
	mux.HandleFunc("/api/v1/service/restart", s.requireSession(s.handleServiceRestart))
	mux.HandleFunc("/api/v1/service/logs", s.requireSession(s.handleServiceLogs))
	mux.HandleFunc("/api/v1/service/logs/stream", s.requireSession(s.handleServiceLogStream))
	mux.HandleFunc("/runtime/v1/pair", s.handleRuntimePair)
	mux.HandleFunc("/runtime/v1/install.sh", s.handleRuntimeInstaller)
	mux.HandleFunc("/runtime/v1/connect", s.handleRuntimeConnect)
	mux.HandleFunc("/runtime/v1/attachments/", s.handleRuntimeAttachment)
	mux.HandleFunc("/api/", s.requireSession(func(w http.ResponseWriter, r *http.Request) { s.proxy.ServeHTTP(w, r) }))
	return s.withStatic(mux)
}

func (s *Server) handleRuntimeInstaller(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, false, nil, "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", `attachment; filename="install-runtime.sh"`)
	_, _ = w.Write(deploy.RuntimeInstaller)
}

func (s *Server) internalHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /control/v1/service/restart", func(w http.ResponseWriter, r *http.Request) {
		if s.deployment == nil {
			writeJSON(w, http.StatusServiceUnavailable, false, nil, "部署管理器尚未配置")
			return
		}
		run, err := s.deployment.Restart(r.Context())
		if err != nil {
			writeJSON(w, http.StatusConflict, false, nil, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, true, run, "")
	})
	mux.HandleFunc("GET /runtime/v1/events", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeJSON(w, http.StatusInternalServerError, false, nil, "streaming is unavailable")
			return
		}
		workspaceRef := strings.TrimSpace(r.URL.Query().Get("workspace_ref"))
		threadID := strings.TrimSpace(r.URL.Query().Get("thread_id"))
		events, cancel := s.broker.SubscribeEvents()
		defer cancel()
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		encoder := json.NewEncoder(w)
		for {
			select {
			case <-r.Context().Done():
				return
			case event, open := <-events:
				if !open {
					return
				}
				if workspaceRef != "" && event.Resource.WorkspaceRef != workspaceRef {
					continue
				}
				if threadID != "" && event.Resource.ConversationRef != threadID {
					continue
				}
				if err := encoder.Encode(event); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	})
	mux.HandleFunc("GET /runtime/v1/devices", func(w http.ResponseWriter, r *http.Request) {
		devices, err := s.broker.Devices(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, false, nil, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, true, devices, "")
	})
	mux.HandleFunc("GET /runtime/v1/catalog", func(w http.ResponseWriter, r *http.Request) {
		catalog, err := s.broker.Catalog(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, false, nil, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, true, catalog, "")
	})
	mux.HandleFunc("POST /runtime/v1/rpc", func(w http.ResponseWriter, r *http.Request) {
		var request runtimeprotocol.InternalRequest
		if err := decodeRequest(r, &request); err != nil {
			writeJSON(w, http.StatusBadRequest, false, nil, err.Error())
			return
		}
		payload, err := s.broker.ResolveAndCall(r.Context(), request)
		if err != nil {
			status := http.StatusBadGateway
			if strings.Contains(err.Error(), "unknown workspace") || strings.Contains(err.Error(), "another device") {
				status = http.StatusBadRequest
			}
			writeJSON(w, status, false, nil, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, true, json.RawMessage(payload), "")
	})
	mux.HandleFunc("POST /runtime/v1/attachments", func(w http.ResponseWriter, r *http.Request) {
		var request runtimeprotocol.AttachmentStageRequest
		if err := decodeRequest(r, &request); err != nil {
			writeJSON(w, http.StatusBadRequest, false, nil, err.Error())
			return
		}
		result, err := s.broker.StageAttachments(r.Context(), request)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, false, nil, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, true, result, "")
	})
	return mux
}

func (s *Server) withStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/runtime/") {
			next.ServeHTTP(w, r)
			return
		}
		if s.config.Assets == nil {
			http.NotFound(w, r)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if file, err := s.config.Assets.Open(path); err == nil {
			_ = file.Close()
			http.FileServer(http.FS(s.config.Assets)).ServeHTTP(w, r)
			return
		}
		index, err := fs.ReadFile(s.config.Assets, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	})
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		required, err := s.store.SetupRequired(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, false, nil, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, true, map[string]bool{"required": required}, "")
		return
	}
	if r.Method != http.MethodPost || !s.validOrigin(r) {
		writeJSON(w, http.StatusForbidden, false, nil, "setup request origin is not allowed")
		return
	}
	var request struct {
		SetupToken string `json:"setup_token"`
		Password   string `json:"password"`
	}
	if err := decodeRequest(r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, false, nil, err.Error())
		return
	}
	if err := s.store.SetupAdministrator(r.Context(), request.SetupToken, request.Password); err != nil {
		writeJSON(w, http.StatusUnauthorized, false, nil, err.Error())
		return
	}
	s.createBrowserSession(w, r)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.validOrigin(r) {
		writeJSON(w, http.StatusForbidden, false, nil, "login request origin is not allowed")
		return
	}
	key := remoteIP(r)
	if !s.loginLimiter.allow(key, time.Now()) {
		writeJSON(w, http.StatusTooManyRequests, false, nil, "登录尝试过于频繁，请稍后重试")
		return
	}
	var request struct {
		Password string `json:"password"`
	}
	if err := decodeRequest(r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, false, nil, err.Error())
		return
	}
	if err := s.store.AuthenticateAdministrator(r.Context(), request.Password); err != nil {
		s.loginLimiter.failed(key, time.Now())
		writeJSON(w, http.StatusUnauthorized, false, nil, "管理员密码错误")
		return
	}
	s.loginLimiter.succeeded(key)
	s.createBrowserSession(w, r)
}

func (s *Server) createBrowserSession(w http.ResponseWriter, r *http.Request) {
	session, token, err := s.store.CreateSession(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, false, nil, err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, Expires: session.ExpiresAt})
	writeJSON(w, http.StatusOK, true, session, "")
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, false, nil, "method not allowed")
		return
	}
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		_ = s.store.DeleteSession(r.Context(), cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, true, map[string]bool{"logged_out": true}, "")
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, false, nil, "method not allowed")
		return
	}
	session, _ := r.Context().Value(sessionContextKey{}).(controlstore.Session)
	writeJSON(w, http.StatusOK, true, session, "")
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, false, nil, "method not allowed")
		return
	}
	devices, err := s.broker.Devices(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, false, nil, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, true, devices, "")
}

func (s *Server) handlePairingCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, false, nil, "method not allowed")
		return
	}
	code, err := s.store.CreatePairingCode(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, false, nil, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, true, code, "")
}

func (s *Server) handleDevice(w http.ResponseWriter, r *http.Request) {
	suffix := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/devices/"), "/")
	parts := strings.Split(suffix, "/")
	id := parts[0]
	if id == "" {
		writeJSON(w, http.StatusNotFound, false, nil, "device not found")
		return
	}
	if len(parts) > 1 {
		if len(parts) == 2 && parts[1] == "logs" {
			s.handleDeviceLogs(w, r, id, false)
			return
		}
		if len(parts) == 3 && parts[1] == "logs" && parts[2] == "stream" {
			s.handleDeviceLogs(w, r, id, true)
			return
		}
		writeJSON(w, http.StatusNotFound, false, nil, "device resource not found")
		return
	}
	switch r.Method {
	case http.MethodPatch:
		var request struct {
			Name string `json:"name"`
		}
		if err := decodeRequest(r, &request); err != nil {
			writeJSON(w, http.StatusBadRequest, false, nil, err.Error())
			return
		}
		if err := s.store.RenameDevice(r.Context(), id, request.Name); err != nil {
			writeJSON(w, http.StatusBadRequest, false, nil, err.Error())
			return
		}
		_ = s.store.RecordAudit(r.Context(), "admin", "device_renamed", "device:"+id, "succeeded", nil)
	case http.MethodDelete:
		if err := s.broker.RevokeDevice(r.Context(), id); err != nil {
			writeJSON(w, http.StatusBadRequest, false, nil, err.Error())
			return
		}
	default:
		writeJSON(w, http.StatusMethodNotAllowed, false, nil, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, true, map[string]string{"id": id}, "")
}

func (s *Server) handleDeviceLogs(w http.ResponseWriter, r *http.Request, deviceID string, stream bool) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, false, nil, "method not allowed")
		return
	}
	if _, err := s.store.Device(r.Context(), deviceID); err != nil {
		writeJSON(w, http.StatusNotFound, false, nil, "device not found")
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if !stream {
		events, err := s.store.AuditEvents(r.Context(), "device:"+deviceID, after, 500)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, false, nil, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, true, events, "")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, false, nil, "device log streaming is unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	encoder := json.NewEncoder(w)
	for {
		events, err := s.store.AuditEvents(r.Context(), "device:"+deviceID, after, 500)
		if err != nil {
			return
		}
		for _, event := range events {
			if err := encoder.Encode(event); err != nil {
				return
			}
			after = event.ID
		}
		flusher.Flush()
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-r.Context().Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *Server) handleRuntimePair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Header.Get("Origin") != "" {
		writeJSON(w, http.StatusForbidden, false, nil, "runtime pairing requires a non-browser client")
		return
	}
	var request struct {
		Code      string `json:"code"`
		Name      string `json:"name"`
		PublicKey string `json:"public_key"`
	}
	if err := decodeRequest(r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, false, nil, err.Error())
		return
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(request.PublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		writeJSON(w, http.StatusBadRequest, false, nil, "invalid Ed25519 public key")
		return
	}
	device, err := s.store.PairDevice(r.Context(), request.Code, request.Name, publicKey)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, false, nil, err.Error())
		return
	}
	_ = s.store.RecordAudit(r.Context(), "runtime:"+device.ID, "device_paired", "device:"+device.ID, "succeeded", nil)
	writeJSON(w, http.StatusCreated, true, map[string]any{"device_id": device.ID, "contract_hash": runtimeprotocol.ContractHash}, "")
}

func (s *Server) handleRuntimeConnect(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != "" {
		writeJSON(w, http.StatusForbidden, false, nil, "runtime connection requires a non-browser client")
		return
	}
	if r.Method == http.MethodPost {
		var request struct {
			DeviceID string `json:"device_id"`
		}
		if err := decodeRequest(r, &request); err != nil {
			writeJSON(w, http.StatusBadRequest, false, nil, err.Error())
			return
		}
		challenge, expires, err := s.broker.IssueChallenge(r.Context(), request.DeviceID)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, false, nil, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, true, map[string]any{"challenge": challenge, "expires_at": expires, "contract_hash": runtimeprotocol.ContractHash}, "")
		return
	}
	if r.Method == http.MethodGet {
		s.broker.Connect(w, r)
		return
	}
	writeJSON(w, http.StatusMethodNotAllowed, false, nil, "method not allowed")
}

func (s *Server) handleRuntimeAttachment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, false, nil, "method not allowed")
		return
	}
	if r.Header.Get("Origin") != "" {
		writeJSON(w, http.StatusForbidden, false, nil, "runtime attachment requires a non-browser client")
		return
	}
	ref := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/runtime/v1/attachments/"))
	if ref == "" || strings.Contains(ref, "/") {
		writeJSON(w, http.StatusBadRequest, false, nil, "attachment reference is invalid")
		return
	}
	content, err := s.broker.DownloadAttachment(
		r.Context(), r.Header.Get("X-CC-Device-ID"), ref, r.Header.Get("X-CC-Timestamp"),
		r.Header.Get("X-CC-Nonce"), r.Header.Get("X-CC-Signature"),
	)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, false, nil, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, true, content, "")
}

func (s *Server) handleServiceStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || s.supervisor == nil {
		writeJSON(w, http.StatusServiceUnavailable, false, nil, "业务进程监管尚未配置")
		return
	}
	writeJSON(w, http.StatusOK, true, s.supervisor.Status(), "")
}

func (s *Server) handleServiceRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || s.deployment == nil {
		writeJSON(w, http.StatusServiceUnavailable, false, nil, "业务进程监管尚未配置")
		return
	}
	run, err := s.deployment.Restart(r.Context())
	if err != nil {
		writeJSON(w, http.StatusConflict, false, nil, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, true, run, "")
}

func (s *Server) handleServiceLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || s.supervisor == nil {
		writeJSON(w, http.StatusServiceUnavailable, false, nil, "业务进程监管尚未配置")
		return
	}
	after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	writeJSON(w, http.StatusOK, true, s.supervisor.Logs(after), "")
}

func (s *Server) handleServiceLogStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || s.supervisor == nil {
		writeJSON(w, http.StatusServiceUnavailable, false, nil, "业务进程监管尚未配置")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, false, nil, "日志流不可用")
		return
	}
	after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	initial, lines, cancel := s.supervisor.SubscribeLogs(after)
	defer cancel()
	w.Header().Set("Content-Type", "application/x-ndjson")
	encoder := json.NewEncoder(w)
	for _, line := range initial {
		if err := encoder.Encode(line); err != nil {
			return
		}
	}
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case line := <-lines:
			if err := encoder.Encode(line); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) handleDeployDashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPut {
		s.handleConfigureServer(w, r)
		return
	}
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, false, nil, "method not allowed")
		return
	}
	devices, err := s.broker.Devices(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, false, nil, err.Error())
		return
	}
	runs, err := s.store.ListDeployRuns(r.Context(), 10)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, false, nil, err.Error())
		return
	}
	runtimeUpdates, err := s.store.RuntimeUpdates(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, false, nil, err.Error())
		return
	}
	status := ServiceStatus{}
	if s.supervisor != nil {
		status = s.supervisor.Status()
	}
	publicURL, _, err := s.store.Setting(r.Context(), "public_url")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, false, nil, err.Error())
		return
	}
	currentReleaseTag, _, err := s.store.Setting(r.Context(), "current_release_tag")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, false, nil, err.Error())
		return
	}
	_, configErr := os.Stat(filepath.Join(s.config.AppDirectory, "config.toml"))
	if configErr != nil && !errors.Is(configErr, os.ErrNotExist) {
		writeJSON(w, http.StatusInternalServerError, false, nil, configErr.Error())
		return
	}
	workspaces, err := s.broker.Catalog(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, false, nil, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, true, map[string]any{
		"service": status, "devices": devices, "runs": runs, "runtime_updates": runtimeUpdates,
		"runtime_contract_hash": runtimeprotocol.ContractHash, "control_schema": controlstore.SchemaVersion,
		"current_release_tag": currentReleaseTag, "configured": configErr == nil, "public_url": publicURL, "workspace_count": len(workspaces),
	}, "")
}

func (s *Server) handlePreflightOperations(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var request struct {
			PublicURL string `json:"public_url"`
		}
		if err := decodeRequest(r, &request); err != nil {
			writeJSON(w, http.StatusBadRequest, false, nil, err.Error())
			return
		}
		publicURL, err := validatePublicURL(request.PublicURL)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, false, nil, err.Error())
			return
		}
		if err := s.store.PutSetting(r.Context(), "public_url", publicURL); err != nil {
			writeJSON(w, http.StatusInternalServerError, false, nil, err.Error())
			return
		}
		_ = s.store.RecordAudit(r.Context(), "admin", "configure_public_url", "control", "succeeded", nil)
		writeJSON(w, http.StatusOK, true, map[string]string{"public_url": publicURL}, "")
		return
	}
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, false, nil, "method not allowed")
		return
	}
	status := ServiceStatus{}
	if s.supervisor != nil {
		status = s.supervisor.Status()
	}
	publicURL, publicURLSet, _ := s.store.Setting(r.Context(), "public_url")
	catalog, catalogErr := s.broker.Catalog(r.Context())
	validWorkspace := false
	for _, workspace := range catalog {
		if workspace.Online && workspace.Available {
			validWorkspace = true
			break
		}
	}
	writeJSON(w, http.StatusOK, true, []map[string]any{
		{"id": "service_running", "ok": status.Running, "message": "业务进程必须处于运行状态"},
		{"id": "public_url", "ok": publicURLSet, "message": publicURL},
		{"id": "runtime_workspace", "ok": catalogErr == nil && validWorkspace, "message": "至少一台在线 Runtime 必须提供有效 Codex 项目"},
		{"id": "runtime_contract", "ok": true, "message": runtimeprotocol.ContractHash},
		{"id": "signed_release_required", "ok": true, "message": "更新只接受 GitHub OIDC/Sigstore 签名制品"},
	}, "")
}

type serverConfigurationRequest struct {
	Language    string `json:"language"`
	EnableWeCom bool   `json:"enable_wecom"`
	WeComBotID  string `json:"wecom_bot_id"`
	WeComSecret string `json:"wecom_bot_secret"`
	AllowFrom   string `json:"wecom_allow_from"`
}

func (s *Server) handleConfigureServer(w http.ResponseWriter, r *http.Request) {
	if s.supervisor == nil {
		writeJSON(w, http.StatusServiceUnavailable, false, nil, "业务进程监管尚未配置")
		return
	}
	if s.supervisor.Status().Running {
		writeJSON(w, http.StatusConflict, false, nil, "业务进程运行中，初始化配置不可覆盖")
		return
	}
	var request serverConfigurationRequest
	if err := decodeRequest(r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, false, nil, err.Error())
		return
	}
	if request.EnableWeCom && (strings.TrimSpace(request.WeComBotID) == "" || strings.TrimSpace(request.WeComSecret) == "") {
		writeJSON(w, http.StatusBadRequest, false, nil, "启用企业微信时必须提供 Bot ID 和 Bot Secret")
		return
	}
	catalog, err := s.broker.Catalog(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, false, nil, err.Error())
		return
	}
	validWorkspace := false
	for _, workspace := range catalog {
		if workspace.Online && workspace.Available {
			validWorkspace = true
			break
		}
	}
	if !validWorkspace {
		writeJSON(w, http.StatusConflict, false, nil, "至少需要一台在线 Runtime 和一个有效 Codex 项目")
		return
	}
	configPath := filepath.Join(s.config.AppDirectory, "config.toml")
	previousConfig, previousErr := os.ReadFile(configPath)
	previousExists := previousErr == nil
	if previousErr != nil && !errors.Is(previousErr, os.ErrNotExist) {
		writeJSON(w, http.StatusInternalServerError, false, nil, fmt.Sprintf("读取现有业务配置失败: %v", previousErr))
		return
	}
	if err := writeInitialServerConfig(configPath, s.config.AppDirectory, request); err != nil {
		writeJSON(w, http.StatusInternalServerError, false, nil, err.Error())
		return
	}
	if err := s.supervisor.Start(context.Background()); err != nil {
		var rollbackErr error
		if previousExists {
			rollbackErr = replacePrivateFile(configPath, previousConfig)
		} else {
			rollbackErr = os.Remove(configPath)
			if errors.Is(rollbackErr, os.ErrNotExist) {
				rollbackErr = nil
			}
		}
		_ = s.store.RecordAudit(r.Context(), "admin", "initialize_server", "service", "failed", nil)
		writeJSON(w, http.StatusInternalServerError, false, nil, errors.Join(err, rollbackErr).Error())
		return
	}
	_ = s.store.RecordAudit(r.Context(), "admin", "initialize_server", "service", "succeeded", nil)
	writeJSON(w, http.StatusCreated, true, s.supervisor.Status(), "")
}

func validatePublicURL(raw string) (string, error) {
	value := strings.TrimSuffix(strings.TrimSpace(raw), "/")
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("公开地址必须是无凭据、query 和 fragment 的 HTTPS URL")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("公开地址不能包含路径")
	}
	return value, nil
}

func writeInitialServerConfig(path, appDirectory string, request serverConfigurationRequest) error {
	enabled := true
	transports := []string{"web"}
	if request.EnableWeCom {
		transports = append(transports, "wecom")
	}
	language := strings.TrimSpace(request.Language)
	if language == "" {
		language = "zh"
	}
	value := struct {
		DataDir       string                        `toml:"data_dir"`
		Language      string                        `toml:"language"`
		WorkspaceChat appconfig.WorkspaceChatConfig `toml:"workspace_chat"`
	}{
		DataDir: appDirectory, Language: language,
		WorkspaceChat: appconfig.WorkspaceChatConfig{
			Enabled: &enabled, Transports: transports,
			WeCom: appconfig.WorkspaceChatWeComConfig{BotID: strings.TrimSpace(request.WeComBotID), BotSecret: strings.TrimSpace(request.WeComSecret), AllowFrom: strings.TrimSpace(request.AllowFrom)},
		},
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("control plane: create app directory: %w", err)
	}
	var encoded bytes.Buffer
	if err := toml.NewEncoder(&encoded).Encode(value); err != nil {
		return fmt.Errorf("control plane: encode server config: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("control plane: create config temp file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded.Bytes()); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if _, err := appconfig.Load(temporaryPath); err != nil {
		return fmt.Errorf("control plane: validate generated server config: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("control plane: install server config: %w", err)
	}
	return nil
}

func replacePrivateFile(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".restore-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func (s *Server) handleDeployRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		if s.deployment == nil {
			writeJSON(w, http.StatusServiceUnavailable, false, nil, "部署管理器尚未配置")
			return
		}
		var request struct {
			Kind      string `json:"kind"`
			TargetTag string `json:"target_tag"`
		}
		if err := decodeRequest(r, &request); err != nil {
			writeJSON(w, http.StatusBadRequest, false, nil, err.Error())
			return
		}
		run, err := s.deployment.Start(r.Context(), request.Kind, request.TargetTag)
		if err != nil {
			writeJSON(w, http.StatusConflict, false, nil, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, true, run, "")
		return
	}
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, false, nil, "method not allowed")
		return
	}
	runs, err := s.store.ListDeployRuns(r.Context(), 50)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, false, nil, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, true, runs, "")
}

func (s *Server) handleDeployRun(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/deploy/runs/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" {
		writeJSON(w, http.StatusNotFound, false, nil, "deployment run resource not found")
		return
	}
	switch parts[1] {
	case "log":
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, false, nil, "method not allowed")
			return
		}
		after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
		logs, err := s.store.RunLogs(r.Context(), parts[0], after)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, false, nil, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, true, logs, "")
	case "cancel":
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, false, nil, "method not allowed")
			return
		}
		if s.deployment == nil || !s.deployment.Cancel(parts[0]) {
			writeJSON(w, http.StatusConflict, false, nil, "运行已结束或当前阶段不可取消")
			return
		}
		writeJSON(w, http.StatusAccepted, true, map[string]string{"id": parts[0]}, "")
	case "stream":
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, false, nil, "method not allowed")
			return
		}
		s.streamDeployRun(w, r, parts[0])
	default:
		writeJSON(w, http.StatusNotFound, false, nil, "deployment run resource not found")
	}
}

func (s *Server) streamDeployRun(w http.ResponseWriter, r *http.Request, runID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, false, nil, "部署日志流不可用")
		return
	}
	after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	w.Header().Set("Content-Type", "application/x-ndjson")
	encoder := json.NewEncoder(w)
	for {
		logs, err := s.store.RunLogs(r.Context(), runID, after)
		if err != nil {
			return
		}
		for _, entry := range logs {
			if err := encoder.Encode(entry); err != nil {
				return
			}
			after = entry.Sequence
		}
		flusher.Flush()
		run, err := s.store.DeployRun(r.Context(), runID)
		if err != nil || run.Status != "running" {
			return
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-r.Context().Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

type sessionContextKey struct{}

func (s *Server) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, false, nil, "authentication required")
			return
		}
		unsafe := r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
		websocketUpgrade := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
		if (unsafe || websocketUpgrade) && !s.validOrigin(r) {
			writeJSON(w, http.StatusForbidden, false, nil, "request origin is not allowed")
			return
		}
		session, err := s.store.ValidateSession(r.Context(), cookie.Value, r.Header.Get("X-CSRF-Token"), unsafe)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, false, nil, "authentication required")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), sessionContextKey{}, session)))
	}
}

func (s *Server) validOrigin(r *http.Request) bool {
	origin := strings.TrimSuffix(strings.TrimSpace(r.Header.Get("Origin")), "/")
	if origin == "" {
		return false
	}
	expected := strings.TrimSuffix(strings.TrimSpace(s.config.PublicURL), "/")
	configured, ok, err := s.store.Setting(r.Context(), "public_url")
	if err != nil {
		return false
	}
	if ok {
		expected = strings.TrimSuffix(strings.TrimSpace(configured), "/")
	}
	if expected == "" {
		scheme := "https"
		if strings.HasPrefix(r.Host, "127.0.0.1") || strings.HasPrefix(r.Host, "localhost") || strings.HasPrefix(r.Host, "[::1]") {
			scheme = "http"
		}
		expected = scheme + "://" + r.Host
	}
	return origin == expected
}

func (s *Server) Close(ctx context.Context) error {
	var result error
	s.closeOnce.Do(func() {
		_ = s.broker.Close()
		if s.attachments != nil {
			result = errors.Join(result, s.attachments.Close())
		}
		if s.publicServer != nil {
			result = errors.Join(result, s.publicServer.Shutdown(ctx))
		}
		if s.internalServer != nil {
			result = errors.Join(result, s.internalServer.Shutdown(ctx))
		}
		if s.config.RuntimeSocket != "" {
			if err := os.Remove(s.config.RuntimeSocket); err != nil && !errors.Is(err, os.ErrNotExist) {
				result = errors.Join(result, err)
			}
		}
	})
	return result
}

func decodeRequest(r *http.Request, target any) error {
	const maxRequestBody = 50 << 20
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
	if err != nil {
		return fmt.Errorf("invalid request: read body: %w", err)
	}
	if len(raw) > maxRequestBody {
		return errors.New("invalid request: body exceeds 50 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid request: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("invalid request: multiple JSON values are not allowed")
		}
		return fmt.Errorf("invalid request: trailing data: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, ok bool, data any, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	response := map[string]any{"ok": ok}
	if ok {
		response["data"] = data
	} else {
		response["error"] = message
	}
	_ = json.NewEncoder(w).Encode(response)
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type loginLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
}

func newLoginLimiter() *loginLimiter { return &loginLimiter{failures: make(map[string][]time.Time)} }

func (l *loginLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(key, now)
	return len(l.failures[key]) < 5
}

func (l *loginLimiter) failed(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(key, now)
	l.failures[key] = append(l.failures[key], now)
}

func (l *loginLimiter) succeeded(key string) {
	l.mu.Lock()
	delete(l.failures, key)
	l.mu.Unlock()
}

func (l *loginLimiter) prune(key string, now time.Time) {
	cutoff := now.Add(-5 * time.Minute)
	values := l.failures[key]
	kept := values[:0]
	for _, value := range values {
		if value.After(cutoff) {
			kept = append(kept, value)
		}
	}
	l.failures[key] = kept
}
