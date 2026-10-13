package core

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// NativeSessionNamer updates the native session list without creating a turn.
// Multi-backend adapters can decline support for an individual session.
type NativeSessionNamer interface {
	SupportsSessionNaming() bool
	SetSessionName(context.Context, string) error
}

// Keep the native title separate from role-decorated workflow card labels.
func (s *TeamProjectStore) SetMemberDisplayName(id, name string) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.displayNames == nil {
		s.displayNames = map[string]string{}
	}
	s.displayNames[id] = strings.TrimSpace(name)
}

func (s *TeamProjectStore) memberDisplayName(id string) string {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if name := s.displayNames[id]; name != "" {
		return name
	}
	return id
}

// Called once on each native start/resume, not on every message. Naming is
// best effort and must not hold up the first prompt or other member sessions.
func (e *Engine) nameTeamSession(route string, session *Session, sessions *SessionManager, native AgentSession) {
	p, err := e.teamProject(route)
	if err != nil || p == nil || strings.TrimSpace(p.Name) == "" {
		return
	}
	id := native.CurrentSessionID()
	if id == "" {
		return
	}
	name := strings.TrimSpace(p.Name) + " · " + e.teamProjects.memberDisplayName(e.name)
	session.SetName(name)
	sessions.SetSessionName(id, name)
	namer, ok := native.(NativeSessionNamer)
	if !ok || !namer.SupportsSessionNaming() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(e.ctx, 5*time.Second)
		defer cancel()
		if err := namer.SetSessionName(ctx, name); err != nil {
			slog.Warn("team native session name sync failed", "member", e.name, "agent_session", id, "error", err)
			return
		}
		slog.Info("team native session named", "member", e.name, "agent_session", id, "name", name)
	}()
}
