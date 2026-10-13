package codex

import (
	"context"
	"fmt"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

var _ core.NativeSessionNamer = (*appServerSession)(nil)

func (s *appServerSession) SupportsSessionNaming() bool { return true }

func (s *appServerSession) SetSessionName(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	id := s.CurrentSessionID()
	if id == "" {
		return fmt.Errorf("codex: cannot name a session before its thread ID is known")
	}
	requestID := s.nextID.Add(1)
	ch := make(chan rpcResponseEnvelope, 1)
	s.pendingMu.Lock()
	if s.pending == nil {
		s.pending = make(map[int64]chan rpcResponseEnvelope)
	}
	s.pending[requestID] = ch
	s.pendingMu.Unlock()
	defer func() {
		s.pendingMu.Lock()
		delete(s.pending, requestID)
		s.pendingMu.Unlock()
	}()

	// A cosmetic update must not use writeJSONWithTimeout: that path aborts
	// the active model transport on a write timeout. The writer is released
	// when the pipe drains or the owning session closes.
	written := make(chan error, 1)
	go func() {
		if err := ctx.Err(); err != nil {
			written <- err
			return
		}
		written <- s.writeJSON(map[string]any{"jsonrpc": "2.0", "id": requestID, "method": "thread/name/set", "params": map[string]string{"threadId": id, "name": name}})
	}()
	select {
	case err := <-written:
		if err != nil {
			return fmt.Errorf("codex: write session name: %w", err)
		}
	case <-ctx.Done():
		return ctx.Err()
	case <-s.contextDone():
		return s.contextErr()
	}
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return fmt.Errorf("codex: set session name: %s", resp.Error.Message)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.contextDone():
		return s.contextErr()
	}
}
