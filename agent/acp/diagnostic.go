package acp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const stderrTailLimit = 16 * 1024

// stderrDiagnostics bounds process output and keeps only an allowlisted error
// classification for user reports. Never forward arbitrary stderr as a cause
// of a generic prompt failure: it can contain credentials or unrelated output.
type stderrDiagnostics struct {
	mu      sync.Mutex
	tail    string
	partial string
	key     core.MsgKey
	ready   chan struct{}
}

func (d *stderrDiagnostics) beginTurn() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.partial, d.key = "", ""
	d.ready = make(chan struct{})
}

func (d *stderrDiagnostics) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tail = boundedStderr(d.tail + string(p))
	for _, part := range strings.SplitAfter(string(p), "\n") {
		d.partial = boundedStderr(d.partial + part)
		if d.key == "" && strings.Contains(d.partial, "ERROR ") &&
			(strings.Contains(d.partial, "codex_api::sse::trae_chat:") || strings.Contains(d.partial, "traex_acp::thread:")) &&
			strings.Contains(d.partial, "当前仓库密级较高") && strings.Contains(d.partial, "支持 L4 仓库的模型") {
			d.key = core.MsgModelRepositoryPolicyDenied
			if d.ready != nil {
				close(d.ready)
			}
		}
		if strings.HasSuffix(part, "\n") {
			d.partial = ""
		}
	}
	return len(p), nil
}

func boundedStderr(s string) string {
	if len(s) > stderrTailLimit {
		return s[len(s)-stderrTailLimit:]
	}
	return s
}

func (d *stderrDiagnostics) String() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.tail
}

func (d *stderrDiagnostics) promptError(ctx context.Context, err error) error {
	var rpc *rpcErrPayload
	if !errors.As(err, &rpc) || rpc.Code != -32603 || !strings.EqualFold(strings.TrimSpace(rpc.Message), "Internal error") {
		return err
	}
	d.mu.Lock()
	ready, key := d.ready, d.key
	d.mu.Unlock()
	if key == "" && ready != nil {
		// stdout and stderr use separate OS pipes; the RPC response can arrive
		// just before the diagnostic that the process wrote before that response.
		timer := time.NewTimer(100 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ready:
		case <-timer.C:
		case <-ctx.Done():
		}
		d.mu.Lock()
		key = d.key
		d.mu.Unlock()
	}
	if key != "" {
		return &core.AgentDiagnosticError{Key: key, Cause: err}
	}
	return err
}
