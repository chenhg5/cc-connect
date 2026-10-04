package core

import (
	"strings"
	"testing"
	"time"
)

func TestSharedPending_TextAcceptsOfferedDecisionsBeforeAliases(t *testing.T) {
	for _, tc := range []struct{ reply, decision string }{
		{"allow_session", "allow_session"}, {"allow_similar", "allow_similar"},
		{"network_allow", "network_allow"}, {"network_deny", "network_deny"},
		{"cancel", "cancel"}, {"取消", "cancel"}, {" ALLOW_SESSION ", "allow_session"},
		{"yes", "allow"}, {"no", "deny"},
	} {
		t.Run(tc.reply, func(t *testing.T) {
			env := newSharedTestEnv(t)
			plain := &stubPlatformEngine{n: "test"}
			send := func(text string) {
				env.e.ReceiveMessage(plain, &Message{SessionKey: "test:user", Platform: "test", UserID: "user", Content: text, ReplyCtx: "reply"})
			}
			send("/attach first")
			as := env.a.connection("first")
			as.emit(Event{Type: EventPermissionRequest, RequestID: "approval", ToolName: "Bash", ToolInput: "TEXT APPROVAL", Decisions: []string{"allow", "deny", "allow_session", "allow_similar", "network_allow", "network_deny", "cancel"}})
			waitSharedPlainText(t, plain, "TEXT APPROVAL")
			send(tc.reply)
			waitSharedPlainText(t, plain, env.e.i18n.T(MsgSharedResponseSent))
			as.mu.Lock()
			defer as.mu.Unlock()
			if len(as.responses) != 1 || as.responses[0].Behavior != tc.decision {
				t.Fatalf("reply %q submitted %+v; want %q", tc.reply, as.responses, tc.decision)
			}
		})
	}
}

func TestSharedPending_CancelWithoutOfferedCancelDoesNotDeny(t *testing.T) {
	env := newSharedTestEnv(t)
	env.send("/attach first")
	as := env.a.connection("first")
	as.emit(Event{Type: EventPermissionRequest, RequestID: "approval", ToolName: "Bash", ToolInput: "NO CANCEL", Decisions: []string{"allow", "deny"}})
	env.await("NO CANCEL")
	env.send("cancel")
	env.await(env.e.i18n.Tf(MsgSharedDecisionHint, "allow / deny"))
	as.mu.Lock()
	defer as.mu.Unlock()
	if len(as.responses) != 0 || !as.pending["approval"] {
		t.Fatalf("unsupported cancellation submitted a decision: %+v", as.responses)
	}
}

func waitSharedPlainText(t *testing.T, p *stubPlatformEngine, text string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(strings.Join(p.getSent(), "\n"), text) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("missing text %q in %v", text, p.getSent())
}

func setupSharedWorkspace(t *testing.T, env *sharedTestEnv) *workspaceState {
	t.Helper()
	path := t.TempDir()
	env.e.SetMultiWorkspace(path, t.TempDir()+"/bindings.json")
	env.e.SetWorkspaceIdleTimeout(time.Minute)
	ws := env.e.workspacePool.GetOrCreate(path)
	ws.mu.Lock()
	ws.agent, ws.sessions = env.e.agent, env.e.sessions
	ws.mu.Unlock()
	env.e.bindSendWorkDir("test:user", path)
	return ws
}

func ageSharedWorkspace(ws *workspaceState) {
	ws.mu.Lock()
	ws.lastActivity = time.Now().Add(-2 * time.Minute)
	ws.mu.Unlock()
}

func waitSharedWorkspaceIdle(t *testing.T, ws *workspaceState) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !ws.HasActiveTurn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("shared workspace activity leaked after turn ended")
}

func TestSharedWorkspace_ExternalApprovalWaitSurvivesIdleReaping(t *testing.T) {
	env := newSharedTestEnv(t)
	ws := setupSharedWorkspace(t, env)
	env.send("/attach first")
	env.await("Attached to session first")
	as := env.a.connection("first")
	// Attachment must protect an already active CLI turn before any event.
	ageSharedWorkspace(ws)
	env.e.reapIdleWorkspaces()
	if env.e.workspacePool.Get(ws.workspace) != ws || !as.Alive() {
		t.Fatal("attached active CLI turn was reaped before its first event")
	}
	as.emit(Event{Type: EventPermissionRequest, TurnID: "active-first", RequestID: "approval", ToolName: "Bash", ToolInput: "EXTERNAL WAIT", Decisions: []string{"allow_session", "cancel"}})
	env.await("EXTERNAL WAIT")
	ageSharedWorkspace(ws)
	env.e.reapIdleWorkspaces()
	env.send("allow_session")
	env.await(env.e.i18n.T(MsgSharedResponseSent))
	as.mu.Lock()
	as.turn = ""
	as.mu.Unlock()
	as.emit(Event{Type: EventResult, TurnID: "active-first", Content: "EXTERNAL COMPLETION", Done: true})
	env.await("EXTERNAL COMPLETION")
	waitSharedWorkspaceIdle(t, ws)
	// A newly started CLI turn must acquire activity again while attached.
	as.mu.Lock()
	as.turn = "next-cli-turn"
	as.mu.Unlock()
	as.emit(Event{Type: EventRuntimeStatus, Content: "connected"})
	env.await(env.e.i18n.T(MsgSharedReconnected))
	ageSharedWorkspace(ws)
	env.e.reapIdleWorkspaces()
	if !ws.HasActiveTurn() || !as.Alive() {
		t.Fatal("new external CLI turn was reaped")
	}
	env.send("/detach")
	env.await(env.e.i18n.T(MsgSharedDetached))
	waitSharedWorkspaceIdle(t, ws)
	ageSharedWorkspace(ws)
	if got := env.e.workspacePool.ReapIdle(); len(got) != 1 {
		t.Fatalf("detached workspace not reaped: %v", got)
	}
}
