package core

// The reader owns this activity lease. It covers external turns even before
// their first presentation event, including turns waiting for human approval.
type sharedWorkspaceActivity struct {
	workspace *workspaceState
	turnID    string
}

func (e *Engine) sharedWorkspaceActivity(state *interactiveState, sessions *SessionManager) *sharedWorkspaceActivity {
	activity := &sharedWorkspaceActivity{}
	if e.workspacePool == nil {
		return activity
	}
	// Attach and management commands already resolve a workspace session manager,
	// but do not pass its directory to the persistent reader.
	for path, ws := range e.workspacePool.All() {
		ws.mu.Lock()
		matches := ws.sessions == sessions
		ws.mu.Unlock()
		if matches {
			state.mu.Lock()
			state.workspaceDir = path
			state.mu.Unlock()
			activity.workspace = ws
			break
		}
	}
	return activity
}

func (a *sharedWorkspaceActivity) observe(runtime AgentRuntimeState, event Event) {
	if a.workspace == nil {
		return
	}
	id := runtime.TurnID
	if id == "" && (event.Type == EventTurnStarted || event.Type == EventPermissionRequest) {
		id = event.TurnID
	}
	if (event.Type == EventResult && event.Done || event.Type == EventError) && event.TurnID == id {
		id = ""
	}
	if id == "" {
		// Runtime snapshots may already be idle while completion events are
		// still queued. Keep the lease until completion or an idle status.
		if event.Type == EventRuntimeStatus || event.Type == EventResult && event.Done || event.Type == EventError {
			a.end()
		}
		return
	}
	if a.turnID == "" {
		a.workspace.BeginTurn()
	}
	a.turnID = id
}

func (a *sharedWorkspaceActivity) end() {
	if a.turnID != "" {
		a.workspace.EndTurn()
		a.turnID = ""
	}
}
