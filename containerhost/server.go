package containerhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/releaseinstall"
)

type ServerConfig struct {
	SocketPath        string
	StatePath         string
	EnvironmentPath   string
	ControlDatabase   string
	InitialTag        string
	ClientUID         int
	ClientGID         int
	ActivationTimeout time.Duration
	ReleaseClient     *releaseinstall.Client
	Runner            Runner
}

type Server struct {
	config ServerConfig

	mu       sync.Mutex
	state    persistedState
	prepared map[string]Preparation
	opMu     sync.Mutex

	listener net.Listener
	http     *http.Server
	ctx      context.Context
	cancel   context.CancelFunc
	wait     sync.WaitGroup
}

func NewServer(config ServerConfig) (*Server, error) {
	if config.ReleaseClient == nil || config.Runner == nil {
		return nil, errors.New("container host: release client and runner are required")
	}
	for _, path := range []string{config.SocketPath, config.StatePath, config.EnvironmentPath, config.ControlDatabase} {
		if !filepath.IsAbs(strings.TrimSpace(path)) {
			return nil, errors.New("container host: absolute socket, state, environment and database paths are required")
		}
	}
	if config.ActivationTimeout <= 0 {
		config.ActivationTimeout = 2 * time.Minute
	}
	state, err := readState(config.StatePath)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{config: config, state: state, prepared: make(map[string]Preparation), ctx: ctx, cancel: cancel}, nil
}

func (s *Server) Start(ctx context.Context) error {
	if s.state.CurrentTag == "" {
		if !validTag(s.config.InitialTag) {
			return errors.New("container host: initial release tag is required")
		}
		preparation, err := s.prepare(ctx, s.config.InitialTag)
		if err != nil {
			return err
		}
		s.state = persistedState{Status: Status{CurrentTag: preparation.Tag, CurrentImage: preparation.Image}}
		if err := writeEnvironment(s.config.EnvironmentPath, preparation.Tag, preparation.Image); err != nil {
			return err
		}
		if err := writeState(s.config.StatePath, s.state); err != nil {
			return err
		}
	}
	if s.state.Pending != nil && !s.state.Pending.Committed {
		pending := s.state.Pending
		s.state.LastRunID = pending.RunID
		s.state.LastOutcome = "failed"
		s.state.LastError = "container host restarted before activation commit"
		s.state.Pending = nil
		if err := writeState(s.config.StatePath, s.state); err != nil {
			return err
		}
	}
	if s.state.Pending != nil && s.state.Pending.Committed && s.state.LastRunID == s.state.Pending.RunID && s.state.LastOutcome == "failed" {
		return fmt.Errorf("container host: pending rollback requires operator recovery: %s", s.state.LastError)
	}
	if err := prepareSocketDirectory(s.config.SocketPath, s.config.ClientGID); err != nil {
		return err
	}
	if err := removeSocket(s.config.SocketPath); err != nil {
		return err
	}
	listener, err := net.Listen("unix", s.config.SocketPath)
	if err != nil {
		return fmt.Errorf("container host: listen: %w", err)
	}
	if err := os.Chmod(s.config.SocketPath, 0o660); err != nil {
		_ = listener.Close()
		return err
	}
	if s.config.ClientGID >= 0 {
		if err := os.Chown(s.config.SocketPath, -1, s.config.ClientGID); err != nil {
			_ = listener.Close()
			return err
		}
	}
	listener = restrictPeer(listener, s.config.ClientUID)
	s.listener = listener
	s.http = &http.Server{Handler: s.routes(), ReadHeaderTimeout: 5 * time.Second}
	s.wait.Add(1)
	go func() {
		defer s.wait.Done()
		if err := s.http.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("container host HTTP server stopped", "error", err)
		}
	}()
	if err := s.config.Runner.ComposeUp(ctx); err != nil {
		_ = s.Close(context.Background())
		return fmt.Errorf("container host: ensure control container: %w", err)
	}
	s.schedulePendingRollback()
	return nil
}

func prepareSocketDirectory(socketPath string, clientGID int) error {
	directory := filepath.Dir(socketPath)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return fmt.Errorf("container host: create socket directory: %w", err)
	}
	if err := os.Chmod(directory, 0o750); err != nil {
		return fmt.Errorf("container host: secure socket directory: %w", err)
	}
	if clientGID >= 0 {
		if err := os.Chown(directory, -1, clientGID); err != nil {
			return fmt.Errorf("container host: set socket directory group: %w", err)
		}
	}
	return nil
}

func (s *Server) Close(ctx context.Context) error {
	s.cancel()
	if s.http != nil {
		_ = s.http.Shutdown(ctx)
	}
	done := make(chan struct{})
	go func() { s.wait.Wait(); close(done) }()
	select {
	case <-done:
		_ = os.Remove(s.config.SocketPath)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/latest", s.handleLatest)
	mux.HandleFunc("GET /v1/status", s.handleStatus)
	mux.HandleFunc("POST /v1/prepare", s.handlePrepare)
	mux.HandleFunc("POST /v1/activate", s.handleActivate)
	mux.HandleFunc("POST /v1/commit", s.handleCommit)
	mux.HandleFunc("POST /v1/cancel", s.handleCancel)
	mux.HandleFunc("POST /v1/confirm", s.handleConfirm)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(contractHeader) != ContractHash {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUpgradeRequired)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "update_required"})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) handleLatest(w http.ResponseWriter, r *http.Request) {
	tag, err := s.config.ReleaseClient.LatestTag(r.Context())
	writeResponse(w, tagResult{Tag: tag}, err)
}

type tagResult struct {
	Tag string `json:"tag"`
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	status := cloneStatus(s.state.Status)
	s.mu.Unlock()
	writeResponse(w, status, nil)
}

func (s *Server) handlePrepare(w http.ResponseWriter, r *http.Request) {
	var request PrepareRequest
	if err := decodeBody(r, &request); err != nil {
		writeResponse(w, nil, err)
		return
	}
	preparation, err := s.prepare(r.Context(), request.Tag)
	writeResponse(w, preparation, err)
}

func (s *Server) prepare(ctx context.Context, tag string) (Preparation, error) {
	if !validTag(tag) {
		return Preparation{}, errors.New("container host: valid release tag is required")
	}
	release, err := s.config.ReleaseClient.Fetch(ctx, tag)
	if err != nil {
		return Preparation{}, err
	}
	image, err := s.config.Runner.PrepareImage(ctx, tag)
	if err != nil {
		return Preparation{}, err
	}
	preparation := Preparation{Tag: tag, Image: image, Manifest: append(json.RawMessage(nil), release.ManifestRaw...)}
	s.mu.Lock()
	s.prepared[tag] = preparation
	s.mu.Unlock()
	return preparation, nil
}

func (s *Server) handleActivate(w http.ResponseWriter, r *http.Request) {
	var request ActivateRequest
	if err := decodeBody(r, &request); err != nil {
		writeResponse(w, nil, err)
		return
	}
	err := s.activate(request)
	writeResponse(w, map[string]string{"run_id": request.RunID}, err)
}

func (s *Server) activate(request ActivateRequest) error {
	if !validRunID(request.RunID) || (request.Kind != "update" && request.Kind != "rollback") ||
		request.BackupName != "control-"+request.RunID+".db" {
		return errors.New("container host: activation request is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Pending != nil {
		return errors.New("container host: another activation is pending")
	}
	preparation, ok := s.prepared[request.TargetTag]
	if !ok || preparation.Image != request.TargetImage {
		return errors.New("container host: target image was not prepared by this process")
	}
	if request.TargetTag == s.state.CurrentTag || (request.Kind == "rollback" && request.TargetTag != s.state.PreviousTag) {
		return errors.New("container host: target release is not valid for the requested operation")
	}
	backup := filepath.Join(filepath.Dir(s.config.ControlDatabase), "backups", request.BackupName)
	if info, err := os.Stat(backup); err != nil || !info.Mode().IsRegular() {
		return errors.New("container host: control database backup is unavailable")
	}
	pending := &PendingOperation{RunID: request.RunID, Kind: request.Kind, TargetTag: request.TargetTag,
		TargetImage: request.TargetImage, PreviousTag: s.state.CurrentTag, PreviousImage: s.state.CurrentImage,
		PriorPreviousTag: s.state.PreviousTag, PriorPreviousImage: s.state.PreviousImage,
		BackupName: request.BackupName, Deadline: time.Now().UTC().Add(s.config.ActivationTimeout)}
	if err := validatePending(*pending); err != nil {
		return err
	}
	s.state.Pending = pending
	if err := writeState(s.config.StatePath, s.state); err != nil {
		s.state.Pending = nil
		return err
	}
	return nil
}

func (s *Server) handleCommit(w http.ResponseWriter, r *http.Request) {
	request, err := decodeRunRequest(r)
	if err == nil {
		err = s.markCommitted(request.RunID)
	}
	writeResponse(w, map[string]string{"run_id": request.RunID}, err)
	if err == nil {
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		s.wait.Add(1)
		go func(runID string) {
			defer s.wait.Done()
			s.runCommitted(runID)
		}(request.RunID)
	}
}

func (s *Server) markCommitted(runID string) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Pending == nil || s.state.Pending.RunID != runID || s.state.Pending.Committed {
		return errors.New("container host: pending activation does not match commit")
	}
	s.state.Pending.Committed = true
	if err := writeEnvironment(s.config.EnvironmentPath, s.state.Pending.TargetTag, s.state.Pending.TargetImage); err != nil {
		s.state.Pending.Committed = false
		return err
	}
	if err := writeState(s.config.StatePath, s.state); err != nil {
		s.state.Pending.Committed = false
		restoreErr := writeEnvironment(s.config.EnvironmentPath, s.state.CurrentTag, s.state.CurrentImage)
		return errors.Join(err, restoreErr)
	}
	return nil
}

func (s *Server) runCommitted(runID string) {
	ctx, cancel := context.WithTimeout(s.ctx, 2*time.Minute)
	err := s.config.Runner.ComposeUp(ctx)
	cancel()
	if err != nil {
		if rollbackErr := s.rollback(runID, fmt.Errorf("activate target container: %w", err)); rollbackErr != nil {
			slog.Error("container host rollback failed", "run_id", runID, "error", rollbackErr)
		}
		return
	}
	s.schedulePendingRollback()
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	request, err := decodeRunRequest(r)
	if err == nil {
		s.opMu.Lock()
		s.mu.Lock()
		if s.state.Pending == nil || s.state.Pending.RunID != request.RunID || s.state.Pending.Committed {
			err = errors.New("container host: activation cannot be cancelled")
		} else {
			previousState := s.state
			s.state.LastRunID = request.RunID
			s.state.LastOutcome = "failed"
			s.state.LastError = "activation cancelled before commit"
			s.state.Pending = nil
			err = writeState(s.config.StatePath, s.state)
			if err != nil {
				s.state = previousState
			}
		}
		s.mu.Unlock()
		s.opMu.Unlock()
	}
	writeResponse(w, map[string]string{"run_id": request.RunID}, err)
}

func (s *Server) handleConfirm(w http.ResponseWriter, r *http.Request) {
	request, err := decodeRunRequest(r)
	if err == nil {
		err = s.confirm(request.RunID)
	}
	writeResponse(w, map[string]string{"run_id": request.RunID}, err)
}

func (s *Server) confirm(runID string) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	pending := s.state.Pending
	if pending == nil || pending.RunID != runID || !pending.Committed {
		return errors.New("container host: pending activation does not match confirmation")
	}
	previousState := s.state
	s.state.CurrentTag, s.state.CurrentImage = pending.TargetTag, pending.TargetImage
	s.state.PreviousTag, s.state.PreviousImage = pending.PreviousTag, pending.PreviousImage
	s.state.LastRunID, s.state.LastOutcome, s.state.LastError = runID, "succeeded", ""
	s.state.Pending = nil
	if err := writeState(s.config.StatePath, s.state); err != nil {
		s.state = previousState
		return err
	}
	_ = os.Remove(filepath.Join(filepath.Dir(s.config.ControlDatabase), "backups", pending.BackupName))
	return nil
}

func (s *Server) schedulePendingRollback() {
	s.mu.Lock()
	pending := s.state.Pending
	s.mu.Unlock()
	if pending == nil || !pending.Committed {
		return
	}
	delay := time.Until(pending.Deadline)
	if delay < 0 {
		delay = 0
	}
	s.wait.Add(1)
	go func(runID string, delay time.Duration) {
		defer s.wait.Done()
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-s.ctx.Done():
			return
		case <-timer.C:
			if err := s.rollback(runID, errors.New("candidate control did not confirm before deadline")); err != nil {
				slog.Error("container host timed rollback failed", "run_id", runID, "error", err)
			}
		}
	}(pending.RunID, delay)
}

func (s *Server) rollback(runID string, cause error) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	pending := s.state.Pending
	if pending == nil || pending.RunID != runID || !pending.Committed {
		s.mu.Unlock()
		return nil
	}
	copyPending := *pending
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := s.config.Runner.ComposeStop(ctx); err != nil {
		return s.recordRollbackFailure(runID, cause, err)
	}
	if err := s.restoreDatabase(copyPending.BackupName); err != nil {
		return s.recordRollbackFailure(runID, cause, err)
	}
	if err := writeEnvironment(s.config.EnvironmentPath, copyPending.PreviousTag, copyPending.PreviousImage); err != nil {
		return s.recordRollbackFailure(runID, cause, err)
	}
	var rollbackErr error
	if err := s.config.Runner.ComposeUp(ctx); err != nil {
		rollbackErr = errors.Join(rollbackErr, err)
	}
	result := errors.Join(cause, rollbackErr)

	s.mu.Lock()
	s.state.CurrentTag, s.state.CurrentImage = copyPending.PreviousTag, copyPending.PreviousImage
	s.state.PreviousTag, s.state.PreviousImage = copyPending.PriorPreviousTag, copyPending.PriorPreviousImage
	s.state.LastRunID, s.state.LastOutcome, s.state.LastError = runID, "failed", result.Error()
	s.state.Pending = nil
	if err := writeState(s.config.StatePath, s.state); err != nil {
		slog.Error("container host cannot persist rollback state", "run_id", runID, "error", err)
		rollbackErr = errors.Join(rollbackErr, err)
	}
	s.mu.Unlock()
	return rollbackErr
}

func (s *Server) recordRollbackFailure(runID string, cause, rollbackErr error) error {
	result := errors.Join(cause, rollbackErr)
	s.mu.Lock()
	s.state.LastRunID, s.state.LastOutcome, s.state.LastError = runID, "failed", result.Error()
	persistErr := writeState(s.config.StatePath, s.state)
	s.mu.Unlock()
	return errors.Join(rollbackErr, persistErr)
}

func (s *Server) restoreDatabase(backupName string) error {
	directory := filepath.Dir(s.config.ControlDatabase)
	backup := filepath.Join(directory, "backups", backupName)
	raw, err := os.ReadFile(backup)
	if err != nil {
		return fmt.Errorf("container host: read database backup: %w", err)
	}
	for _, sidecar := range []string{s.config.ControlDatabase + "-wal", s.config.ControlDatabase + "-shm"} {
		if err := os.Remove(sidecar); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := replaceFile(s.config.ControlDatabase, raw, 0o600); err != nil {
		return fmt.Errorf("container host: restore database: %w", err)
	}
	if err := os.Chown(s.config.ControlDatabase, s.config.ClientUID, s.config.ClientGID); err != nil {
		return fmt.Errorf("container host: restore database ownership: %w", err)
	}
	return nil
}

func decodeRunRequest(r *http.Request) (RunRequest, error) {
	var request RunRequest
	err := decodeBody(r, &request)
	if err == nil && !validRunID(request.RunID) {
		err = errors.New("container host: run id is required")
	}
	return request, err
}

func decodeBody(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, (2<<20)+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("container host: request must contain exactly one JSON value")
	}
	return nil
}

func writeResponse(w http.ResponseWriter, data any, err error) {
	w.Header().Set("Content-Type", "application/json")
	status := http.StatusOK
	value := map[string]any{"ok": true, "data": data}
	if err != nil {
		status = http.StatusConflict
		value = map[string]any{"ok": false, "error": err.Error()}
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func cloneStatus(status Status) Status {
	raw, _ := json.Marshal(status)
	var clone Status
	_ = json.Unmarshal(raw, &clone)
	return clone
}

func removeSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return errors.New("container host: socket path exists and is not a socket")
	}
	return os.Remove(path)
}
