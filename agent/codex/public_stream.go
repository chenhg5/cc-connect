package codex

import (
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// Keep each public item separate from private reasoning and from the final answer.
// Completion is authoritative; streamed prefixes are never replayed as text.
type publicMessage struct {
	phase, text string
	done        bool
}

// The turn/start response and notification may arrive in either order. Never
// clear streamed output (or resurrect a finished turn) when the second arrives.
func (s *appServerSession) beginTurnLocked(id string) {
	if id == "" || id == s.lastTurnID {
		return
	}
	s.currentTurn, s.lastTurnID = id, id
	s.pendingMsgs, s.publicMsgs, s.finalAnswer = nil, nil, ""
}

func (s *appServerSession) publicMessageLocked(id string) *publicMessage {
	if s.publicMsgs == nil {
		s.publicMsgs = make(map[string]*publicMessage)
	}
	m := s.publicMsgs[id]
	if m == nil {
		m = &publicMessage{}
		s.publicMsgs[id] = m
	}
	return m
}

func (s *appServerSession) startPublicMessage(item map[string]any) {
	id, _ := item["id"].(string)
	phase, _ := item["phase"].(string)
	s.stateMu.Lock()
	m := s.publicMessageLocked(id)
	if phase != "" {
		m.phase = phase
	}
	s.stateMu.Unlock()
}

func (s *appServerSession) handlePublicDelta(turnID, id, delta string) {
	if delta == "" {
		return
	}
	s.stateMu.Lock()
	if s.currentTurn == "" || (turnID != "" && turnID != s.currentTurn) {
		s.stateMu.Unlock()
		return
	}
	m := s.publicMessageLocked(id)
	if m.done {
		s.stateMu.Unlock()
		return
	}
	m.text += delta
	e := core.Event{Type: core.EventText, TextID: id, Content: delta}
	if m.phase == "commentary" {
		e.Type, e.Content = core.EventProgress, m.text
	}
	s.stateMu.Unlock()
	s.emit(e)
}

func (s *appServerSession) completePublicMessage(item map[string]any) {
	id, _ := item["id"].(string)
	text, _ := item["text"].(string)
	phase, _ := item["phase"].(string)
	s.stateMu.Lock()
	m := s.publicMessageLocked(id)
	if m.done {
		s.stateMu.Unlock()
		return
	}
	if phase != "" {
		m.phase = phase
	}
	previous := m.text
	m.text, m.done = text, true
	e := core.Event{Type: core.EventText, TextID: id}
	if m.phase == "commentary" {
		e.Type, e.Content = core.EventProgress, text
	} else {
		s.finalAnswer = text
		if strings.HasPrefix(text, previous) {
			e.Content = strings.TrimPrefix(text, previous)
		}
		// A revised final item is delivered verbatim by EventResult, without
		// appending conflicting text to the already displayed partial response.
	}
	s.stateMu.Unlock()
	if e.Content != "" {
		s.emit(e)
	}
}
