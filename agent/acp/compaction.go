package acp

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const compactionMethod = "_cognition.ai/compaction"

const (
	compactionStartTimeout = 3 * time.Second
	compactionDoneTimeout  = 3 * time.Minute
	compactionPoll         = 50 * time.Millisecond
	compactionQuiet        = 750 * time.Millisecond
	compactionQuietCap     = 5 * time.Second
)

func (s *acpSession) isCompactPrompt(prompt string) bool {
	prompt = strings.TrimSpace(prompt)
	if prompt == "/compact" {
		return true
	}
	cmd := strings.TrimSpace(s.compactCommand)
	return cmd != "" && prompt == cmd
}

func (s *acpSession) resetCompaction() {
	s.compactMu.Lock()
	s.compactStatus = ""
	s.compactMu.Unlock()
}

func (s *acpSession) noteCompaction(params json.RawMessage) {
	status := compactionStatusOf(params)
	if status == "" {
		return
	}
	s.compactMu.Lock()
	s.compactStatus = status
	s.compactMu.Unlock()
}

func compactionStatusOf(params json.RawMessage) string {
	var body struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(params, &body) != nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(body.Status)) {
	case "started", "completed", "failed":
		return strings.ToLower(strings.TrimSpace(body.Status))
	default:
		return ""
	}
}

func (s *acpSession) currentCompaction() string {
	s.compactMu.Lock()
	defer s.compactMu.Unlock()
	return s.compactStatus
}

// waitForCompaction holds EventResult until a /compact turn finishes.
// session/prompt returns immediately; the summary text arrives later.
func (s *acpSession) waitForCompaction(prompt string) {
	if !s.isCompactPrompt(prompt) {
		return
	}
	if !s.waitCompaction(compactionStartTimeout, compactionBegun) {
		return
	}
	status := s.waitCompactionStatus(compactionDoneTimeout)
	if status == "completed" || status == "failed" {
		// The summary chunk can arrive after the terminal status.
		s.waitUntilQuiet(compactionQuiet, compactionQuietCap)
	}
	if status == "failed" {
		s.emit(core.Event{Type: core.EventError, Error: errors.New("acp: context compaction failed")})
	}
}

// noteSessionUpdate records a session/update for this session so a
// compaction turn can stay open until the stream is quiet.
func (s *acpSession) noteSessionUpdate(params json.RawMessage) {
	if !sessionUpdateForCurrent(params, s.currentACPSessionID()) {
		return
	}
	s.updateMu.Lock()
	s.lastUpdate = time.Now()
	ch := s.updateNotify
	s.updateMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

func sessionUpdateForCurrent(params json.RawMessage, current string) bool {
	var body struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(params, &body) != nil {
		return true
	}
	id := strings.TrimSpace(body.SessionID)
	current = strings.TrimSpace(current)
	return id == "" || current == "" || id == current
}

func (s *acpSession) lastSessionUpdate() time.Time {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	return s.lastUpdate
}

// waitUntilQuiet returns after quiet with no session/update, or when
// cap elapses. Updates that predate the call do not count as activity.
func (s *acpSession) waitUntilQuiet(quiet, cap time.Duration) {
	start := time.Now()
	deadline := start.Add(cap)
	for {
		if s.ctx.Err() != nil {
			return
		}
		s.drainUpdateNotify()
		now := time.Now()
		if !now.Before(deadline) {
			return
		}
		anchor := start
		if last := s.lastSessionUpdate(); last.After(start) {
			anchor = last
		}
		quietAt := anchor.Add(quiet)
		if quietAt.After(deadline) {
			quietAt = deadline
		}
		if !now.Before(quietAt) {
			return
		}
		timer := time.NewTimer(time.Until(quietAt))
		select {
		case <-s.updateNotify:
			timer.Stop()
		case <-timer.C:
		case <-s.ctx.Done():
			timer.Stop()
			return
		}
	}
}

func (s *acpSession) drainUpdateNotify() {
	if s.updateNotify == nil {
		return
	}
	select {
	case <-s.updateNotify:
	default:
	}
}

func compactionBegun(status string) bool {
	return status == "started" || status == "completed" || status == "failed"
}

func (s *acpSession) waitCompaction(timeout time.Duration, accept func(string) bool) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(compactionPoll)
	defer ticker.Stop()
	for {
		if accept(s.currentCompaction()) {
			return true
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			return accept(s.currentCompaction())
		case <-s.ctx.Done():
			return false
		}
	}
}

func (s *acpSession) waitCompactionStatus(timeout time.Duration) string {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(compactionPoll)
	defer ticker.Stop()
	for {
		status := s.currentCompaction()
		if status == "completed" || status == "failed" {
			return status
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			return s.currentCompaction()
		case <-s.ctx.Done():
			return ""
		}
	}
}
