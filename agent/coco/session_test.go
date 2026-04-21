package coco

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestSession(ctx context.Context, resumeID string) *cocoSession {
	sessionCtx, cancel := context.WithCancel(ctx)
	s := &cocoSession{
		workDir: "/tmp",
		events:  make(chan core.Event, 64),
		ctx:     sessionCtx,
		cancel:  cancel,
	}
	s.alive.Store(true)
	if resumeID != "" && resumeID != core.ContinueSession {
		s.sessionID.Store(resumeID)
	}
	return s
}

func TestNewCocoSession(t *testing.T) {
	ctx := context.Background()
	s := newTestSession(ctx, "resume-abc")
	defer s.Close()

	assert.True(t, s.Alive())
	assert.Equal(t, "resume-abc", s.CurrentSessionID())
	assert.NotNil(t, s.Events())
}

func TestNewCocoSessionNoResumeID(t *testing.T) {
	ctx := context.Background()
	s := newTestSession(ctx, "")
	defer s.Close()

	assert.True(t, s.Alive())
	assert.Equal(t, "", s.CurrentSessionID())
}

func TestClose(t *testing.T) {
	ctx := context.Background()
	s := newTestSession(ctx, "")
	require.True(t, s.Alive())

	err := s.Close()
	assert.NoError(t, err)
	assert.False(t, s.Alive())
}

func TestCloseIdempotent(t *testing.T) {
	ctx := context.Background()
	s := newTestSession(ctx, "")

	assert.NoError(t, s.Close())
	assert.NoError(t, s.Close())
	assert.False(t, s.Alive())
}

func TestSendOnClosedSession(t *testing.T) {
	ctx := context.Background()
	s := newTestSession(ctx, "")
	s.Close()

	err := s.Send("hello", nil, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "session is closed")
}

func TestRespondPermissionOnClosedSession(t *testing.T) {
	ctx := context.Background()
	s := newTestSession(ctx, "")
	s.Close()

	err := s.RespondPermission("req-1", core.PermissionResult{Behavior: "allow"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "session is closed")
}

func TestReadLoopSendsEvents(t *testing.T) {
	ctx := context.Background()
	s := newTestSession(ctx, "sess-1")
	defer s.Close()

	pr, pw := io.Pipe()
	s.wg.Add(1)
	go s.readLoop(pr)

	pw.Write([]byte("hello world"))
	pw.Close()

	events := drainEvents(s.events, 3)
	require.GreaterOrEqual(t, len(events), 1)

	hasText := false
	hasResult := false
	for _, e := range events {
		if e.Type == core.EventText {
			hasText = true
		}
		if e.Type == core.EventResult {
			hasResult = true
			assert.True(t, e.Done)
			assert.Equal(t, "sess-1", e.SessionID)
		}
	}
	assert.True(t, hasText, "expected at least one EventText")
	assert.True(t, hasResult, "expected an EventResult")
}

func TestReadLoopCleansAnsi(t *testing.T) {
	ctx := context.Background()
	s := newTestSession(ctx, "")
	defer s.Close()

	pr, pw := io.Pipe()
	s.wg.Add(1)
	go s.readLoop(pr)

	pw.Write([]byte("\x1b[31mred text\x1b[0m"))
	pw.Close()

	events := drainEvents(s.events, 3)
	var resultContent string
	for _, e := range events {
		if e.Type == core.EventResult {
			resultContent = e.Content
		}
	}
	assert.Equal(t, "red text", resultContent)
}

func drainEvents(ch <-chan core.Event, max int) []core.Event {
	var events []core.Event
	timeout := time.After(500 * time.Millisecond)
	for i := 0; i < max; i++ {
		select {
		case evt := <-ch:
			events = append(events, evt)
		case <-timeout:
			return events
		}
	}
	return events
}

