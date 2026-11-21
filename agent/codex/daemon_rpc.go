package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type daemonMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

// One socket reader dispatches RPC replies separately from thread events. It
// never starts, stops or owns the managed daemon. Writes are serialized.
type daemonRPC struct {
	conn     *websocket.Conn
	ctx      context.Context
	cancel   context.CancelFunc
	next     atomic.Int64
	writeMu  sync.Mutex
	mu       sync.Mutex
	pending  map[string]chan daemonMessage
	messages chan daemonMessage // readLoop is the sole sender and closer
	done     chan struct{}
	err      error
}

func discoverDaemon(ctx context.Context, binary string, args, env []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	bin, err := resolveCodexExecutable(binary)
	if err != nil {
		return "", err
	}
	command := exec.CommandContext(ctx, bin, append(append([]string(nil), args...), "app-server", "daemon", "version")...)
	if len(env) > 0 {
		command.Env = env
	}
	data, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("codex daemon discovery (version only): %w", err)
	}
	var info struct {
		Status string `json:"status"`
		Socket string `json:"socketPath"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return "", fmt.Errorf("codex daemon discovery JSON: %w", err)
	}
	if info.Status != "running" || info.Socket == "" {
		return "", fmt.Errorf("codex managed daemon is not running or has no socket; start it independently")
	}
	return info.Socket, nil
}

func connectDaemon(ctx context.Context, socket string) (*daemonRPC, error) {
	d := websocket.Dialer{HandshakeTimeout: 10 * time.Second, Proxy: nil, NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	conn, response, err := d.DialContext(ctx, "ws://localhost/rpc", nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("codex daemon connect UDS: %w", err)
	}
	conn.SetReadLimit(32 * 1024 * 1024)
	lifetime, cancel := context.WithCancel(ctx)
	r := &daemonRPC{conn: conn, ctx: lifetime, cancel: cancel, pending: map[string]chan daemonMessage{}, messages: make(chan daemonMessage, 1024), done: make(chan struct{})}
	go r.readLoop()
	stop := context.AfterFunc(lifetime, func() { _ = conn.Close() })
	go func() { <-r.done; stop() }()
	if err := r.request(ctx, "initialize", map[string]any{"clientInfo": map[string]any{"name": "cc-connect-codex-agent", "version": "0.1.0"}, "capabilities": map[string]any{"experimentalApi": true}}, nil); err != nil {
		r.close()
		return nil, err
	}
	if err := r.write(ctx, map[string]any{"method": "initialized"}); err != nil {
		r.close()
		return nil, err
	}
	return r, nil
}

func (r *daemonRPC) readLoop() {
	defer close(r.done)
	defer close(r.messages)
	defer r.cancel()
	for {
		var m daemonMessage
		if err := r.conn.ReadJSON(&m); err != nil {
			r.mu.Lock()
			r.err = fmt.Errorf("codex daemon read: %w", err)
			r.mu.Unlock()
			return
		}
		if m.Method == "" && len(m.ID) > 0 {
			r.mu.Lock()
			ch := r.pending[string(m.ID)]
			r.mu.Unlock()
			if ch != nil {
				select {
				case ch <- m:
				default:
				}
			}
			continue
		}
		select {
		case r.messages <- m:
		case <-r.ctx.Done():
			return
		default:
			r.mu.Lock()
			r.err = fmt.Errorf("codex daemon event queue overflow; reconnect required")
			r.mu.Unlock()
			return
		}
	}
}

func (r *daemonRPC) write(ctx context.Context, v any) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.ctx.Err(); err != nil {
		return fmt.Errorf("codex daemon connection closed: %w", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := r.conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	if err := r.conn.WriteJSON(v); err != nil {
		return fmt.Errorf("codex daemon write: %w", err)
	}
	return nil
}

func (r *daemonRPC) request(ctx context.Context, method string, params, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	id := r.next.Add(1)
	key := strconv.FormatInt(id, 10)
	ch := make(chan daemonMessage, 1)
	r.mu.Lock()
	r.pending[key] = ch
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.pending, key); r.mu.Unlock() }()
	if err := r.write(ctx, map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return fmt.Errorf("%s: RPC %d: %s", method, m.Error.Code, m.Error.Message)
		}
		if out != nil {
			if err := json.Unmarshal(m.Result, out); err != nil {
				return fmt.Errorf("%s decode: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%s (outcome may be unknown): %w", method, ctx.Err())
	case <-r.done:
		return fmt.Errorf("%s (outcome may be unknown): %w", method, r.failure())
	}
}

func (r *daemonRPC) failure() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	return fmt.Errorf("codex daemon connection closed")
}
func (r *daemonRPC) close() { r.cancel(); _ = r.conn.Close() }
