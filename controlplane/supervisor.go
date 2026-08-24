package controlplane

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

type SupervisorConfig struct {
	Binary        string
	ConfigPath    string
	ServerSocket  string
	RuntimeSocket string
	LogDirectory  string
}

type ServiceStatus struct {
	Running   bool      `json:"running"`
	PID       int       `json:"pid,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
	ExitError string    `json:"exit_error,omitempty"`
}

type LogLine struct {
	Cursor     uint64    `json:"cursor"`
	OccurredAt time.Time `json:"occurred_at"`
	Stream     string    `json:"stream"`
	Line       string    `json:"line"`
}

type ServerRuntimeActivity struct {
	ActiveTurns         int `json:"active_turns"`
	PendingInteractions int `json:"pending_interactions"`
	RealtimeSessions    int `json:"realtime_sessions"`
}

func (a ServerRuntimeActivity) Busy() bool {
	return a.ActiveTurns > 0 || a.PendingInteractions > 0 || a.RealtimeSessions > 0
}

type Supervisor struct {
	config SupervisorConfig

	mu        sync.Mutex
	command   *exec.Cmd
	cancel    context.CancelFunc
	status    ServiceStatus
	logs      []LogLine
	nextLog   uint64
	listeners map[uint64]chan LogLine
	nextSub   uint64
	closed    bool
	wait      sync.WaitGroup
}

func NewSupervisor(config SupervisorConfig) (*Supervisor, error) {
	if config.Binary == "" || config.ConfigPath == "" || config.ServerSocket == "" || config.RuntimeSocket == "" || config.LogDirectory == "" {
		return nil, errors.New("control supervisor: binary, config, sockets and log directory are required")
	}
	return &Supervisor{config: config, listeners: make(map[uint64]chan LogLine)}, nil
}

func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("control supervisor: closed")
	}
	if s.command != nil {
		return errors.New("control supervisor: server is already running")
	}
	if err := os.MkdirAll(s.config.LogDirectory, 0o750); err != nil {
		return fmt.Errorf("control supervisor: create log directory: %w", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	command := exec.CommandContext(runCtx, s.config.Binary,
		"--config", s.config.ConfigPath,
		"--server-socket", s.config.ServerSocket,
		"--runtime-socket", s.config.RuntimeSocket,
	)
	command.Env = append(os.Environ(), "CC_CONTROLLED_SERVER=1")
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("control supervisor: open stdout: %w", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("control supervisor: open stderr: %w", err)
	}
	if err := command.Start(); err != nil {
		cancel()
		return fmt.Errorf("control supervisor: start server: %w", err)
	}
	s.command = command
	s.cancel = cancel
	s.status = ServiceStatus{Running: true, PID: command.Process.Pid, StartedAt: time.Now()}
	s.wait.Add(3)
	go s.capture(stdout, "stdout")
	go s.capture(stderr, "stderr")
	go s.await(command)
	return nil
}

func (s *Supervisor) capture(reader io.Reader, stream string) {
	defer s.wait.Done()
	file, err := os.OpenFile(filepath.Join(s.config.LogDirectory, "server.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		slog.Error("control supervisor cannot open server log", "error", err)
	}
	if file != nil {
		defer func() { _ = file.Close() }()
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 2<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if file != nil {
			_, _ = fmt.Fprintf(file, "%s [%s] %s\n", time.Now().UTC().Format(time.RFC3339Nano), stream, line)
		}
		s.appendLog(stream, line)
	}
}

func (s *Supervisor) appendLog(stream, line string) {
	s.mu.Lock()
	s.nextLog++
	entry := LogLine{Cursor: s.nextLog, OccurredAt: time.Now(), Stream: stream, Line: line}
	s.logs = append(s.logs, entry)
	if len(s.logs) > 5000 {
		s.logs = append([]LogLine(nil), s.logs[len(s.logs)-5000:]...)
	}
	listeners := make([]chan LogLine, 0, len(s.listeners))
	for _, listener := range s.listeners {
		listeners = append(listeners, listener)
	}
	s.mu.Unlock()
	for _, listener := range listeners {
		select {
		case listener <- entry:
		default:
		}
	}
}

func (s *Supervisor) await(command *exec.Cmd) {
	defer s.wait.Done()
	err := command.Wait()
	s.mu.Lock()
	if s.command == command {
		s.command = nil
		s.cancel = nil
		s.status.Running = false
		s.status.PID = 0
		if err != nil && !errors.Is(err, context.Canceled) {
			s.status.ExitError = err.Error()
		}
	}
	s.mu.Unlock()
}

func (s *Supervisor) Stop(ctx context.Context) error {
	s.mu.Lock()
	command := s.command
	cancel := s.cancel
	s.mu.Unlock()
	if command == nil {
		return nil
	}
	if command.Process != nil {
		_ = command.Process.Signal(syscall.SIGTERM)
	}
	done := make(chan struct{})
	go func() { s.wait.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		if cancel != nil {
			cancel()
		}
		return fmt.Errorf("control supervisor: stop timeout: %w", ctx.Err())
	}
}

func (s *Supervisor) Restart(ctx context.Context) error {
	if err := s.Stop(ctx); err != nil {
		return err
	}
	return s.Start(context.Background())
}

func (s *Supervisor) RuntimeActivity(ctx context.Context) (ServerRuntimeActivity, error) {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", s.config.ServerSocket)
	}}
	client := &http.Client{Transport: transport}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://cc-connect-server/internal/v1/control/runtime-activity", nil)
	if err != nil {
		return ServerRuntimeActivity{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return ServerRuntimeActivity{}, fmt.Errorf("control supervisor: query runtime activity: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	var envelope struct {
		OK    bool                  `json:"ok"`
		Data  ServerRuntimeActivity `json:"data"`
		Error string                `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&envelope); err != nil {
		return ServerRuntimeActivity{}, fmt.Errorf("control supervisor: decode runtime activity: %w", err)
	}
	if response.StatusCode != http.StatusOK || !envelope.OK {
		return ServerRuntimeActivity{}, fmt.Errorf("control supervisor: runtime activity unavailable: %s", envelope.Error)
	}
	return envelope.Data, nil
}

func (s *Supervisor) Status() ServiceStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *Supervisor) Logs(after uint64) []LogLine {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]LogLine, 0, len(s.logs))
	for _, entry := range s.logs {
		if entry.Cursor > after {
			result = append(result, entry)
		}
	}
	return result
}

func (s *Supervisor) SubscribeLogs(after uint64) ([]LogLine, <-chan LogLine, func()) {
	s.mu.Lock()
	initial := make([]LogLine, 0, len(s.logs))
	for _, entry := range s.logs {
		if entry.Cursor > after {
			initial = append(initial, entry)
		}
	}
	s.nextSub++
	id := s.nextSub
	channel := make(chan LogLine, 256)
	s.listeners[id] = channel
	s.mu.Unlock()
	var once sync.Once
	return initial, channel, func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.listeners, id)
			s.mu.Unlock()
		})
	}
}

func (s *Supervisor) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return s.Stop(ctx)
}
