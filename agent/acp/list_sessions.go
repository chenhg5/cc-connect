package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// listSessionsProbeTimeout bounds how long we wait for a one-shot
// `session/list` round-trip before giving up. Keep this short — the
// whole point of the probe is that it's quick; if the ACP agent is
// slow we'd rather return nothing than block `/ls` in IM.
var listSessionsProbeTimeout = 15 * time.Second

// acpModeInfo mirrors the ACP `modes.availableModes[]` shape sent by
// servers like `devin acp`, Cursor Agent, Copilot CLI, etc.
type acpModeInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// acpModesBlock mirrors the `modes` object returned inside `session/new`
// and `session/load` responses.
type acpModesBlock struct {
	CurrentModeID  string        `json:"currentModeId"`
	AvailableModes []acpModeInfo `json:"availableModes"`
}

// acpInitializeResult is the subset of `initialize` fields this package
// cares about. Additional vendor metadata is ignored.
type acpInitializeResult struct {
	ProtocolVersion   int `json:"protocolVersion"`
	AgentCapabilities struct {
		LoadSession         bool `json:"loadSession"`
		SessionCapabilities struct {
			// ACP advertises capabilities as objects (possibly empty);
			// treat "field present" as "supported" regardless of contents.
			List   json.RawMessage `json:"list,omitempty"`
			Resume json.RawMessage `json:"resume,omitempty"`
		} `json:"sessionCapabilities"`
	} `json:"agentCapabilities"`
}

// acpSessionListResult mirrors a `session/list` response.
type acpSessionListResult struct {
	Sessions []acpSessionListEntry `json:"sessions"`
}

type acpSessionListEntry struct {
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	Title     string `json:"title,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// probeSpawn launches `<cmd> <args...>`, sets up a JSON-RPC transport
// and starts its readLoop. The caller owns the returned `teardown`
// func and must invoke it to reap the child process.
//
// teardown returns the child's stderr. It may be called more than once
// (the work is done exactly once) and it may be called *before* the
// caller is done with the transport, which is what makes the stderr
// readable: the text is captured after cmd.Wait() has returned, i.e.
// after os/exec's stderr copy goroutine has finished writing to it.
func (a *Agent) probeSpawn(ctx context.Context, cwd string) (*transport, func() string, error) {
	allArgs := append(append([]string{}, a.cliExtraArgs...), a.args...)
	cmd := exec.CommandContext(ctx, a.cmd, allArgs...)
	cmd.Dir = cwd
	cmd.Env = core.MergeEnv(os.Environ(), a.extraEnv)
	// Bound how long cmd.Wait() may wait for os/exec's I/O goroutines.
	// The stderr copy goroutine only ends when the stderr pipe reaches
	// EOF, so a descendant that inherits the pipe and stays alive would
	// block the teardown forever. Same setting as the gemini/kimi/
	// antigravity/opencode adapters.
	cmd.WaitDelay = 1 * time.Second

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("acp: probe stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("acp: probe stdout pipe: %w", err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = io.MultiWriter(&stderrBuf)

	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("acp: probe start %s: %w", a.cmd, err)
	}

	// The server-request handler needs to reference `tr` itself in order
	// to respondError; declare via var so the closure captures the
	// variable (which is assigned to a *transport below) rather than an
	// uninitialised copy.
	var tr *transport
	tr = newTransport(stdout, stdin,
		func(method string, _ json.RawMessage) {
			slog.Debug("acp-probe: notification", "method", method)
		},
		func(_ string, id json.RawMessage, _ json.RawMessage) {
			_ = tr.respondError(id, -32601, "probe: method not implemented")
		},
	)

	readCtx, cancelRead := context.WithCancel(ctx)
	go tr.readLoop(readCtx)

	// teardown is idempotent: the caller may invoke it early to read the
	// stderr safely and still defer it.
	var (
		once       sync.Once
		stderrText string
	)
	teardown := func() string {
		once.Do(func() {
			cancelRead()
			_ = stdin.Close()
			done := make(chan struct{})
			go func() {
				_ = cmd.Wait()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
				<-done
			}
			// exec copies stderr into stderrBuf from its own goroutine until
			// Wait returns, so the buffer is only safe to read after that
			// (see #2111).
			stderrText = stderrBuf.String()
		})
		return stderrText
	}
	return tr, teardown, nil
}

// probeInitialize performs the ACP handshake on an already-spawned
// transport and returns the parsed initialize result.
func probeInitialize(ctx context.Context, tr *transport) (*acpInitializeResult, error) {
	raw, err := tr.call(ctx, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs":       map[string]any{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
		"clientInfo": map[string]any{"name": "cc-connect", "version": "1.0.0"},
	})
	if err != nil {
		return nil, fmt.Errorf("acp: probe initialize: %w", err)
	}
	var res acpInitializeResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("acp: probe parse initialize: %w", err)
	}
	return &res, nil
}

// probeListSessions runs `session/list` on the given transport.
// Returns (nil, nil) if the agent refuses the call with
// method-not-found / invalid-request — callers interpret that as
// "unsupported" rather than "real error".
func probeListSessions(ctx context.Context, tr *transport, cwdFilter string) ([]acpSessionListEntry, error) {
	params := map[string]any{}
	if cwdFilter != "" {
		params["cwd"] = cwdFilter
	}
	raw, err := tr.call(ctx, "session/list", params)
	if err != nil {
		if rpcErr, ok := err.(*rpcErrPayload); ok {
			if rpcErr.Code == -32601 || rpcErr.Code == -32600 {
				return nil, nil
			}
		}
		return nil, err
	}
	var out acpSessionListResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("acp: parse session/list: %w", err)
	}
	return out.Sessions, nil
}

// ListSessions returns past sessions reported by the ACP agent, scoped
// to the agent's work_dir. If the agent does not advertise
// sessionCapabilities.list or the call soft-fails, returns nil.
//
// This runs a one-shot `<command>` process that performs only
// initialize + session/list, so it does NOT allocate a real session on
// the backend (unlike session/new). Cost is roughly a single ACP
// handshake round-trip (~100-500ms for Devin).
func (a *Agent) ListSessions(ctx context.Context) ([]core.AgentSessionInfo, error) {
	if a.listUnsupported.Load() {
		// Already learned this agent doesn't support session/list;
		// fast-path out to avoid respawning just to rediscover that.
		return nil, nil
	}

	a.mu.RLock()
	workDir := a.workDir
	a.mu.RUnlock()
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		absWorkDir = workDir
	}

	probeCtx, cancel := context.WithTimeout(ctx, listSessionsProbeTimeout)
	defer cancel()

	started := time.Now()
	tr, teardown, err := a.probeSpawn(probeCtx, absWorkDir)
	if err != nil {
		return nil, err
	}
	defer teardown()

	init, err := probeInitialize(probeCtx, tr)
	if err != nil {
		// teardown() reaps the child and hands back its stderr; the text is
		// captured after cmd.Wait() returned, so reading it here cannot race
		// with os/exec's stderr copy goroutine (see #2111).
		if msg := strings.TrimSpace(teardown()); msg != "" {
			return nil, fmt.Errorf("%w (stderr: %s)", err, truncateForLog(msg, 200))
		}
		return nil, err
	}
	if len(init.AgentCapabilities.SessionCapabilities.List) == 0 {
		slog.Info("acp: session/list unsupported by agent, caching for fast-path", "command", a.cmd)
		a.listUnsupported.Store(true)
		return nil, nil
	}

	entries, err := probeListSessions(probeCtx, tr, absWorkDir)
	if err != nil {
		return nil, err
	}
	if entries == nil {
		a.listUnsupported.Store(true)
		return nil, nil
	}
	out := convertSessionList(entries, absWorkDir)
	slog.Info("acp: session/list completed",
		"total_entries", len(entries),
		"matching_cwd", len(out),
		"cwd", absWorkDir,
		"elapsed", time.Since(started),
	)
	return out, nil
}

// convertSessionList maps ACP session/list entries to core.AgentSessionInfo.
// If `cwdFilter` is non-empty, entries whose cwd does not match are dropped;
// ACP servers SHOULD filter themselves when the request includes cwd, but
// we defend against servers that ignore the hint (see probe_caps.py output
// against devin acp: filter is respected there, but we still double-check).
func convertSessionList(entries []acpSessionListEntry, cwdFilter string) []core.AgentSessionInfo {
	out := make([]core.AgentSessionInfo, 0, len(entries))
	for _, e := range entries {
		if cwdFilter != "" && e.Cwd != "" && !strings.EqualFold(filepath.Clean(e.Cwd), filepath.Clean(cwdFilter)) {
			continue
		}
		info := core.AgentSessionInfo{
			ID:      e.SessionID,
			Summary: strings.TrimSpace(e.Title),
		}
		if t, err := time.Parse(time.RFC3339, e.UpdatedAt); err == nil {
			info.ModifiedAt = t
		} else if t, err := time.Parse(time.RFC3339Nano, e.UpdatedAt); err == nil {
			info.ModifiedAt = t
		}
		out = append(out, info)
	}
	return out
}

func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
