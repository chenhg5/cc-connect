package grokbot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/chenhg5/cc-connect/core"
)

// session is one cc-connect conversation. Turns are synchronous from Send's
// point of view: the engine reads Events concurrently while Send waits for
// Grok Bot to finish the run.
type session struct {
	agent     *Agent
	sessionID string

	ctx    context.Context
	cancel context.CancelFunc

	events    chan core.Event
	alive     atomic.Bool
	inTurn    atomic.Bool
	closeOnce sync.Once
}

func newSession(ctx context.Context, agent *Agent, sessionID string) *session {
	ctx, cancel := context.WithCancel(ctx)
	id := strings.TrimSpace(sessionID)
	if id == "" || id == "new" || id == core.ContinueSession {
		id = newID()
	}
	s := &session{
		agent:     agent,
		sessionID: id,
		ctx:       ctx,
		cancel:    cancel,
		events:    make(chan core.Event, 32),
	}
	s.alive.Store(true)
	return s
}

func (s *session) Send(prompt string, messageID string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if !s.alive.Load() {
		return fmt.Errorf("grokbot: session is closed")
	}
	if !s.inTurn.CompareAndSwap(false, true) {
		return fmt.Errorf("grokbot: a turn is already in progress")
	}
	defer s.inTurn.Store(false)
	if err := s.ctx.Err(); err != nil {
		return err
	}

	workDir := s.agent.GetWorkDir()
	text, imageRefs := s.agent.stageImages(workDir, messageID, prompt, images)
	text, fileRefs := s.agent.stageFiles(workDir, messageID, text, files)

	id := newID()
	body := s.agent.buildBody(id, s.sessionID, messageID, text, imageRefs, fileRefs)
	ch := s.agent.register(id)
	defer s.agent.unregister(id)

	postCtx, cancel := context.WithTimeout(s.ctx, webhookHTTPTimeout)
	runID, err := s.agent.postWebhook(postCtx, body)
	cancel()
	if err != nil {
		if s.ctx.Err() != nil {
			return s.ctx.Err()
		}
		return err
	}
	slog.Debug("grokbot: waiting for reply",
		"session", s.sessionID, "run_uuid", runID, "images", len(imageRefs), "files", len(fileRefs), "text_len", len(text))

	msg, err := s.agent.waitRegistered(s.ctx, id, ch, s.agent.timeout)
	if err != nil {
		if s.ctx.Err() != nil {
			return s.ctx.Err()
		}
		s.emit(core.Event{Type: core.EventError, Error: err})
		return nil
	}
	if msg.text != "" {
		s.emit(core.Event{Type: core.EventText, Content: msg.text})
	}
	s.emit(core.Event{
		Type:    core.EventResult,
		Content: msg.text,
		Done:    true,
		Images:  msg.images,
		Files:   msg.files,
	})
	return nil
}

func (s *session) RespondPermission(string, core.PermissionResult) error {
	return fmt.Errorf("grokbot: permission prompts are not supported")
}

func (s *session) Events() <-chan core.Event { return s.events }

func (s *session) CurrentSessionID() string { return s.sessionID }

func (s *session) Alive() bool { return s.alive.Load() }

func (s *session) Close() error {
	s.closeOnce.Do(func() {
		s.alive.Store(false)
		s.cancel()
	})
	return nil
}

func (s *session) emit(evt core.Event) {
	if evt.SessionID == "" {
		evt.SessionID = s.sessionID
	}
	select {
	case s.events <- evt:
	case <-s.ctx.Done():
	}
}

var _ core.AgentSession = (*session)(nil)
