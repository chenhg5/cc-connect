package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// errACPDeleteUnsupported is returned when the agent does not advertise
// sessionCapabilities.delete. Callers can errors.Is it.
var errACPDeleteUnsupported = errors.New("acp: session/delete is not supported by this agent")

func (a *Agent) canonicalWorkDir() string {
	a.mu.RLock()
	wd := a.workDir
	a.mu.RUnlock()
	return canonicalCwd(wd)
}

func (a *Agent) noteCapabilities(init *acpInitializeResult) {
	if init == nil {
		return
	}
	a.reportListSupported(sessionListSupported(init))
	a.reportDeleteSupported(sessionDeleteSupported(init))
}

func sessionListSupported(init *acpInitializeResult) bool {
	if init == nil {
		return false
	}
	return len(init.AgentCapabilities.SessionCapabilities.List) > 0
}

func sessionLoadSupported(init *acpInitializeResult) bool {
	return init != nil && init.AgentCapabilities.LoadSession
}

func sessionDeleteSupported(init *acpInitializeResult) bool {
	if init == nil {
		return false
	}
	raw := bytes.TrimSpace(init.AgentCapabilities.SessionCapabilities.Delete)
	return len(raw) > 0 && !bytes.Equal(raw, []byte("null"))
}

// DeleteSession removes a backend session when the agent advertises
// sessionCapabilities.delete. Unsupported agents return a stable error
// without being asked again after the first negative probe.
func (a *Agent) DeleteSession(ctx context.Context, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("acp: session id is required")
	}
	if a.deleteUnsupported.Load() {
		return errACPDeleteUnsupported
	}
	cwd := a.canonicalWorkDir()
	probeCtx, cancel := context.WithTimeout(ctx, listSessionsProbeTimeout)
	defer cancel()

	tr, stderrBuf, teardown, err := a.probeSpawn(probeCtx, cwd, nil)
	if err != nil {
		return err
	}
	defer teardown()

	init, err := probeInitialize(probeCtx, tr)
	if err != nil {
		return wrapProbeStderr(err, stderrBuf)
	}
	a.noteCapabilities(init)
	if !sessionDeleteSupported(init) {
		return errACPDeleteUnsupported
	}
	if err := deleteSessionOnTransport(probeCtx, tr, sessionID); err != nil {
		return fmt.Errorf("acp: session/delete: %w", err)
	}
	return nil
}

func deleteSessionOnTransport(ctx context.Context, tr *transport, sessionID string) error {
	_, err := tr.call(ctx, "session/delete", map[string]any{"sessionId": sessionID})
	return err
}

// ValidateSessionID reports whether sessionID is in this work dir's
// session/list result.
//
// An empty id is never valid. When session/list is unsupported or the
// probe fails, this returns true. A false return makes the engine drop
// the stored id and start a fresh session; doing that because of a
// transient error, or because the agent cannot list sessions, would
// destroy the user's conversation. Uncertainty keeps the id.
func (a *Agent) ValidateSessionID(ctx context.Context, sessionID string) bool {
	if strings.TrimSpace(sessionID) == "" {
		return false
	}
	if a.listUnsupported.Load() {
		return true
	}
	sessions, err := a.ListSessions(ctx)
	if err != nil || a.listUnsupported.Load() {
		return true
	}
	return containsSessionID(sessions, sessionID)
}

func containsSessionID(sessions []core.AgentSessionInfo, sessionID string) bool {
	for _, s := range sessions {
		if s.ID == sessionID {
			return true
		}
	}
	return false
}

func (a *Agent) rememberTitle(id, title string) {
	id = strings.TrimSpace(id)
	title = strings.TrimSpace(title)
	if id == "" || title == "" {
		return
	}
	a.titlesMu.Lock()
	if a.titles == nil {
		a.titles = make(map[string]string)
	}
	a.titles[id] = title
	a.titlesMu.Unlock()
}

func (a *Agent) rememberTitles(entries []acpSessionListEntry) {
	for _, e := range entries {
		a.rememberTitle(e.SessionID, e.Title)
	}
}

// GetSessionTitle returns a title learned from session/list or from a
// session_info_update notification. Empty when this agent has not seen one.
func (a *Agent) GetSessionTitle(sessionID string) string {
	a.titlesMu.RLock()
	defer a.titlesMu.RUnlock()
	if a.titles == nil {
		return ""
	}
	return a.titles[sessionID]
}

func (s *acpSession) maybeAbsorbSessionInfoUpdate(params json.RawMessage) {
	kind, update, sid := peekSessionUpdate(params)
	if kind != "session_info_update" {
		return
	}
	var body struct {
		Title string `json:"title"`
	}
	if json.Unmarshal(update, &body) != nil {
		return
	}
	title := strings.TrimSpace(body.Title)
	if title == "" || s.callbacks == nil {
		return
	}
	if sid == "" {
		sid = s.currentACPSessionID()
	}
	s.callbacks.reportSessionTitle(sid, title)
}
