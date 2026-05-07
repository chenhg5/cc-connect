package opencode

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const (
	opencodeServerHost           = "127.0.0.1"
	opencodeServerStartupTimeout = 10 * time.Second
	opencodeServerStopTimeout    = 5 * time.Second
)

var opencodeListeningURLRe = regexp.MustCompile(`opencode server listening on (http://[^\s]+)`)

type opencodeServer struct {
	url    string
	cancel context.CancelFunc
	cmd    *exec.Cmd
	done   chan struct{}
	errMu  sync.Mutex
	err    error
}

func (a *Agent) ensureAttachServer(ctx context.Context, cmdPath, workDir string, extraEnv []string, port int) (string, error) {
	a.serverMu.Lock()
	defer a.serverMu.Unlock()

	if a.server != nil {
		if a.server.running() {
			return a.server.url, nil
		}
		a.server = nil
	}

	server, err := startOpencodeServer(ctx, cmdPath, workDir, extraEnv, port)
	if err != nil {
		return "", err
	}
	a.server = server
	return server.url, nil
}

func (a *Agent) stopAttachServer() {
	a.serverMu.Lock()
	defer a.serverMu.Unlock()
	if a.server == nil {
		return
	}
	a.server.stop()
	a.server = nil
}

func startOpencodeServer(ctx context.Context, cmdPath, workDir string, extraEnv []string, port int) (*opencodeServer, error) {
	serverCtx, cancel := context.WithCancel(context.Background())
	args := []string{"serve", "--hostname", opencodeServerHost, "--port", strconv.Itoa(port)}
	cmd := exec.CommandContext(serverCtx, cmdPath, args...)
	cmd.Dir = workDir
	if len(extraEnv) > 0 {
		cmd.Env = core.MergeEnv(os.Environ(), extraEnv)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("opencode: server stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("opencode: server stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("opencode: start headless server: %w", err)
	}

	server := &opencodeServer{
		cancel: cancel,
		cmd:    cmd,
		done:   make(chan struct{}),
	}
	ready := make(chan string, 1)
	go func() {
		server.setErr(cmd.Wait())
		close(server.done)
	}()
	go logOpencodeServerOutput(stdout, "stdout", ready)
	go logOpencodeServerOutput(stderr, "stderr", nil)

	url, err := waitForOpencodeServer(ctx, server, ready)
	if err != nil {
		server.stop()
		return nil, err
	}
	server.url = url

	slog.Info("opencode: headless server started", "url", url, "work_dir", workDir)
	return server, nil
}

func (s *opencodeServer) setErr(err error) {
	s.errMu.Lock()
	s.err = err
	s.errMu.Unlock()
}

func (s *opencodeServer) waitErr() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.err
}

func (s *opencodeServer) running() bool {
	select {
	case <-s.done:
		if err := s.waitErr(); err != nil {
			slog.Warn("opencode: headless server exited", "url", s.url, "err", err)
		} else {
			slog.Info("opencode: headless server exited", "url", s.url)
		}
		return false
	default:
		return true
	}
}

func (s *opencodeServer) stop() {
	s.cancel()
	select {
	case <-s.done:
		if err := s.waitErr(); err != nil {
			slog.Debug("opencode: headless server stopped", "url", s.url, "err", err)
		}
	case <-time.After(opencodeServerStopTimeout):
		if s.cmd != nil && s.cmd.Process != nil {
			if err := s.cmd.Process.Kill(); err != nil {
				slog.Warn("opencode: kill headless server failed", "url", s.url, "err", err)
			}
		}
		select {
		case <-s.done:
		case <-time.After(time.Second):
			slog.Warn("opencode: headless server did not exit after kill", "url", s.url)
		}
	}
}

func waitForOpencodeServer(ctx context.Context, server *opencodeServer, ready <-chan string) (string, error) {
	deadline := time.NewTimer(opencodeServerStartupTimeout)
	defer deadline.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("opencode: wait for headless server: %w", ctx.Err())
		case <-server.done:
			if err := server.waitErr(); err != nil {
				return "", fmt.Errorf("opencode: headless server exited during startup: %w", err)
			}
			return "", fmt.Errorf("opencode: headless server exited during startup")
		case <-deadline.C:
			return "", fmt.Errorf("opencode: headless server did not listen within %s", opencodeServerStartupTimeout)
		case url := <-ready:
			if url != "" {
				return url, nil
			}
		}
	}
}

func logOpencodeServerOutput(r io.Reader, stream string, ready chan<- string) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			if ready != nil {
				if m := opencodeListeningURLRe.FindStringSubmatch(line); len(m) == 2 {
					select {
					case ready <- m[1]:
					default:
					}
				}
			}
			slog.Debug("opencode: headless server output", "stream", stream, "line", truncate(line, 500))
		}
	}
	if err := scanner.Err(); err != nil {
		slog.Debug("opencode: read headless server output", "stream", stream, "err", err)
	}
}
