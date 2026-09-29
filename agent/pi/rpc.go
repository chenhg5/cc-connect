package pi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Acknowledgements are independent of model execution, which may take much
// longer. This bounds both writing a prompt and waiting for its RPC response.
const rpcPromptTimeout = 30 * time.Second

func (s *piSession) requestRPCPrompt(ctx context.Context, cmd map[string]any) error {
	id := fmt.Sprintf("cc-connect-prompt-%d", s.rpcNextID.Add(1))
	cmd["id"] = id
	result := make(chan error, 1)
	s.rpcPendingMu.Lock()
	if s.rpcErr != nil {
		err := s.rpcErr
		s.rpcPendingMu.Unlock()
		return err
	}
	if s.rpcPending == nil {
		s.rpcPending = make(map[string]chan error)
	}
	s.rpcPending[id] = result
	s.rpcPendingMu.Unlock()
	defer func() {
		s.rpcPendingMu.Lock()
		delete(s.rpcPending, id)
		s.rpcPendingMu.Unlock()
	}()

	// Register before writing: the child may reply before Write returns.
	if err := s.writeRPCCommandContext(ctx, cmd); err != nil {
		return err
	}
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return fmt.Errorf("pi: waiting for prompt acknowledgement: %w", ctx.Err())
	}
}

func (s *piSession) handleRPCPromptResponse(raw map[string]any) {
	if command, _ := raw["command"].(string); command != "prompt" {
		return
	}
	id, _ := raw["id"].(string)
	success, valid := raw["success"].(bool)
	var err error
	if !valid {
		err = fmt.Errorf("pi: prompt response missing boolean success")
	} else if !success {
		message, _ := raw["error"].(string)
		err = fmt.Errorf("pi: prompt rejected: %s", message)
	}
	s.rpcPendingMu.Lock()
	result, waiting := s.rpcPending[id]
	if waiting {
		delete(s.rpcPending, id)
		result <- err
	}
	s.rpcPendingMu.Unlock()
	if waiting || err == nil {
		return
	}
	// Pi can acknowledge a prompt before its asynchronous preflight fails.
	// Send has already returned in that case; surface the later error through
	// the event stream instead of silently losing it. Matched rejections are
	// returned only through Send, so /ps fails without ending the active turn.
	slog.Warn("piSession: asynchronous prompt error", "id", id, "error", err)
	select {
	case s.events <- core.Event{Type: core.EventError, Error: err}:
	case <-s.ctx.Done():
	}
}

func (s *piSession) failRPCRequests(err error) {
	s.rpcPendingMu.Lock()
	defer s.rpcPendingMu.Unlock()
	s.rpcErr = err
	for id, result := range s.rpcPending {
		result <- err
		delete(s.rpcPending, id)
	}
}

func (s *piSession) writeRPCCommandContext(ctx context.Context, cmd map[string]any) error {
	b, err := json.Marshal(cmd)
	if err != nil {
		return fmt.Errorf("piSession: marshal command: %w", err)
	}
	b = append(b, '\n')
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("piSession: write stdin: %w", err)
	}
	written := make(chan error, 1)
	go func() {
		s.rpcStdinMu.Lock()
		defer s.rpcStdinMu.Unlock()
		if err := ctx.Err(); err != nil {
			written <- err
			return
		}
		n, err := s.rpcStdin.Write(b)
		if err == nil && n != len(b) {
			err = io.ErrShortWrite
		}
		written <- err
	}()
	select {
	case err := <-written:
		if err != nil {
			return fmt.Errorf("piSession: write stdin: %w", err)
		}
		return nil
	case <-ctx.Done():
		// A partial/blocked write leaves the stream unusable. Closing the
		// pipe unblocks this and all serialized writers; reap the writer here.
		s.failRPCRequests(fmt.Errorf("pi: rpc write interrupted: %w", ctx.Err()))
		s.killRPC()
		<-written
		return fmt.Errorf("piSession: write stdin: %w", ctx.Err())
	}
}
