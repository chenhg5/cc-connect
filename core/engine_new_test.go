package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// Keep the process alive until the test releases Close. Unlike a sleeping
// fixture, this lets the test send a real message during teardown reliably.
type newCommandCloseSession struct {
	*receiptAgentSession
	closeStarted chan struct{}
	releaseClose chan struct{}
	startedOnce  sync.Once
	releaseOnce  sync.Once
	closeErr     error
}

func newCommandSession(id string) *newCommandCloseSession {
	return &newCommandCloseSession{
		receiptAgentSession: &receiptAgentSession{queuingAgentSession: newQueuingSession(id)},
		closeStarted:        make(chan struct{}),
		releaseClose:        make(chan struct{}),
	}
}

func (s *newCommandCloseSession) Close() error {
	s.startedOnce.Do(func() { close(s.closeStarted) })
	<-s.releaseClose
	if s.closeErr != nil {
		return s.closeErr
	}
	return s.receiptAgentSession.Close()
}

func (s *newCommandCloseSession) release() {
	s.releaseOnce.Do(func() { close(s.releaseClose) })
}

func assertNewCommandVisible(t *testing.T, p *stubPlatformEngine, text string) {
	t.Helper()
	for _, message := range p.getSent() {
		if strings.Contains(message, text) {
			return
		}
	}
	t.Fatalf("missing user-visible %q in %v", text, p.getSent())
}

func assertNewCommandPrompts(t *testing.T, s *receiptAgentSession, want ...string) {
	t.Helper()
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if fmt.Sprint(s.sendCalls) != fmt.Sprint(want) {
		t.Fatalf("agent received %v, want %v", s.sendCalls, want)
	}
}

// Run in a synctest bubble so the real 150-second close timeout can be covered
// without changing production timeouts or making the regression suite slow.
func testIssue600ReceiveMessageDuringNew(t *testing.T, workspace bool, closeMode string, pending bool) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		p := &stubPlatformEngine{n: "test"}
		oldProcess := newCommandSession("old-agent")
		newProcess := &receiptAgentSession{queuingAgentSession: newQueuingSession("new-agent")}
		if closeMode == "error" {
			oldProcess.closeErr = errors.New("process could not be killed")
		}
		starts := make(chan string, 4)
		agent := &controllableAgent{startSessionFn: func(_ context.Context, id string) (AgentSession, error) {
			starts <- id
			if len(starts) == 1 {
				return oldProcess, nil
			}
			return newProcess, nil
		}}
		e := NewEngine("issue600", agent, []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
		defer func() {
			oldProcess.release()
			e.cancel()
			synctest.Wait()
			_ = oldProcess.receiptAgentSession.Close()
			_ = newProcess.Close()
		}()

		key := "test:chat:user"
		sessions := e.sessions
		interactiveKey := key
		if workspace {
			baseDir := t.TempDir()
			e.SetMultiWorkspace(baseDir, filepath.Join(t.TempDir(), "bindings.json"))
			previousFactory, registered := agentFactories[agent.Name()]
			defer func() {
				if registered {
					agentFactories[agent.Name()] = previousFactory
				} else {
					delete(agentFactories, agent.Name())
				}
			}()
			RegisterAgent(agent.Name(), func(map[string]any) (Agent, error) { return agent, nil })
			e.workspaceBindings.Bind("project:issue600", workspaceChannelKey(p.Name(), "chat"), "chat", baseDir)
			var err error
			_, sessions, err = e.getOrCreateWorkspaceAgent(baseDir)
			if err != nil {
				t.Fatal(err)
			}
			interactiveKey = normalizeWorkspacePath(baseDir) + ":" + key
		}
		send := func(content string) {
			e.ReceiveMessage(p, &Message{
				Platform: p.Name(), SessionKey: key, ChannelID: "chat", UserID: "user",
				MessageID: content, Content: content, ReplyCtx: "ctx",
			})
		}

		// Start through the same entrypoint used by platform adapters.
		send("old question")
		synctest.Wait()
		oldSession := sessions.GetOrCreateActive(key)
		oldState := e.interactiveStates[interactiveKey]
		assertNewCommandPrompts(t, oldProcess.receiptAgentSession, "old question")
		if pending {
			// Queue before the permission request, otherwise plain text is
			// interpreted as a permission response by the engine.
			send("queued before reset")
			oldProcess.events <- Event{Type: EventPermissionRequest, RequestID: "old-permission", ToolName: "write_file"}
			synctest.Wait()
			if oldState.pending == nil {
				t.Fatal("old turn did not reach its permission prompt")
			}
		} else {
			oldProcess.events <- Event{Type: EventResult, Content: "old answer", Done: true}
			synctest.Wait()
			assertNewCommandVisible(t, p, "old answer")
		}
		oldPending := oldState.pending

		newDone := make(chan struct{})
		go func() {
			send("/new fresh")
			close(newDone)
		}()
		<-oldProcess.closeStarted
		synctest.Wait()
		active := sessions.GetOrCreateActive(key)
		if active == oldSession {
			t.Fatal("issue #600: /new still routes messages to the old active session while Close is blocked")
		}
		// A restart during teardown must also select the newly persisted row.
		restored := NewSessionManager(sessions.StorePath()).GetOrCreateActive(key)
		if restored.ID != active.ID || restored.GetAgentSessionID() != "" {
			t.Fatalf("restart selected %s/%s, want fresh %s", restored.ID, restored.GetAgentSessionID(), active.ID)
		}
		if pending {
			select {
			case <-oldPending.Resolved:
			default:
				t.Fatal("reset left the old permission unresolved")
			}
			if len(oldState.pendingMessages) != 0 {
				t.Fatal("reset retained the old queued message")
			}
			assertNewCommandVisible(t, p, "session reset")
		}

		send("new question")
		synctest.Wait()
		send("new follow-up")
		synctest.Wait()
		assertNewCommandPrompts(t, oldProcess.receiptAgentSession, "old question")
		assertNewCommandPrompts(t, newProcess)
		if !active.Busy() {
			t.Fatal("the concurrent message did not acquire the new session")
		}
		if closeMode == "timeout" {
			time.Sleep(closeTimeout + time.Second)
		} else {
			oldProcess.release()
		}
		<-newDone
		synctest.Wait()
		if closeMode != "success" {
			assertNewCommandVisible(t, p, e.i18n.T(MsgSessionCloseFailed))
		}
		assertNewCommandPrompts(t, newProcess, "new question")
		if len(starts) != 2 {
			t.Fatalf("started %d agent processes, want 2", len(starts))
		}
		for len(starts) > 0 {
			if id := <-starts; id != "" {
				t.Fatalf("resumed old context %q after /new", id)
			}
		}
		newProcess.events <- Event{Type: EventResult, Content: "fresh answer", Done: true}
		synctest.Wait()
		assertNewCommandVisible(t, p, "fresh answer")
		assertNewCommandPrompts(t, newProcess, "new question", "new follow-up")
		newProcess.events <- Event{Type: EventResult, Content: "fresh follow-up answer", Done: true}
		synctest.Wait()
		assertNewCommandVisible(t, p, "fresh follow-up answer")
		assertNewCommandVisible(t, p, "fresh")

		// Once the old process finally exits, its cleanup must not remove the
		// replacement or release a lock belonging to the new turn.
		replacement := e.interactiveStates[interactiveKey]
		oldProcess.release()
		synctest.Wait()
		if replacement == nil || replacement == oldState || e.interactiveStates[interactiveKey] != replacement {
			t.Fatal("old cleanup removed the replacement interactive state")
		}
		p.clearSent()
		send("/history")
		synctest.Wait()
		assertNewCommandVisible(t, p, "new question")
		assertNewCommandVisible(t, p, "fresh answer")
		for _, message := range p.getSent() {
			if strings.Contains(message, "old question") || strings.Contains(message, "queued before reset") {
				t.Fatalf("new history contains old conversation: %s", message)
			}
		}
	})
}

func TestIssue600_ReceiveMessageDuringNew(t *testing.T) {
	for _, workspace := range []bool{false, true} {
		for _, mode := range []string{"success", "error", "timeout"} {
			t.Run(fmt.Sprintf("workspace=%v/close=%s", workspace, mode), func(t *testing.T) {
				testIssue600ReceiveMessageDuringNew(t, workspace, mode, true)
			})
		}
	}
}

func TestCleanupCAS_NilExpectedPreservesNewState(t *testing.T) {
	e := newTestEngine()
	defer e.cancel()
	key := "test:user"
	replacement := &interactiveState{agentSession: newControllableSession("replacement")}
	e.interactiveStates[key] = replacement
	// /new can observe no state, then a concurrent message installs one.
	e.cleanupInteractiveState(key, nil)
	if e.interactiveStates[key] != replacement {
		t.Fatal("cleanup with an explicitly absent old state removed a new state")
	}
}

func TestIssue600_NewPreservesReplacementDuringClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &stubPlatformEngine{n: "test"}
		e := NewEngine("issue600", &stubAgent{}, []Platform{p}, "", LangEnglish)
		defer e.cancel()
		oldProcess := newCommandSession("old-agent")
		defer oldProcess.release()
		key := "test:user"
		e.interactiveStates[key] = &interactiveState{agentSession: oldProcess, platform: p}
		done := make(chan struct{})
		go func() {
			e.ReceiveMessage(p, &Message{SessionKey: key, Content: "/new", ReplyCtx: "ctx"})
			close(done)
		}()
		<-oldProcess.closeStarted
		synctest.Wait()
		// Model a replacement installed while teardown has released interactiveMu.
		// The ReceiveMessage tests cover routing; this forces the second CAS
		// check regardless of scheduling of the close/spawn registry's waiters.
		replacement := &interactiveState{agentSession: newControllableSession("replacement")}
		e.interactiveStates[key] = replacement
		oldProcess.release()
		<-done
		if e.interactiveStates[key] != replacement {
			t.Fatal("/new deleted a replacement installed during the old Close")
		}
	})
}

func TestIssue600_BridgeNewKeepsFreshSession(t *testing.T) {
	bs, url := startTestBridge(t, "")
	bp := bs.NewPlatform("issue600")
	oldProcess := newCommandSession("old-agent")
	newProcess := &receiptAgentSession{queuingAgentSession: newQueuingSession("new-agent")}
	var starts int
	agent := &controllableAgent{startSessionFn: func(_ context.Context, _ string) (AgentSession, error) {
		starts++ // StartSession is serialized by interactiveMu.
		if starts == 1 {
			return oldProcess, nil
		}
		return newProcess, nil
	}}
	e := NewEngine("issue600", agent, []Platform{bp}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	bs.RegisterEngine("issue600", e, bp)
	defer func() {
		oldProcess.release()
		_ = e.Stop()
	}()
	conn := dialWS(t, url, nil)
	register(t, conn, "test-chat", []string{"text"})
	const key = "test-chat:chat:user"
	send := func(content string) {
		mustWriteJSON(t, conn, map[string]any{
			"type": "message", "project": "issue600", "session_key": key,
			"user_id": "user", "msg_id": content, "content": content, "reply_ctx": "ctx",
		})
	}
	readUntil := func(content string) {
		t.Helper()
		for i := 0; i < 20; i++ {
			message := readMsg(t, conn)
			if text, ok := message["content"].(string); ok && strings.Contains(text, content) {
				return
			}
		}
		t.Fatalf("bridge did not deliver %q", content)
	}
	env := &cujEnv{t: t, engine: e, plat: &stubPlatformEngine{}}
	waitForPrompt := func(s *receiptAgentSession) {
		t.Helper()
		env.waitFor("bridge prompt", 3*time.Second, func() bool {
			s.sendMu.Lock()
			defer s.sendMu.Unlock()
			return len(s.sendCalls) == 1
		})
	}
	send("old question")
	waitForPrompt(oldProcess.receiptAgentSession)
	oldProcess.events <- Event{Type: EventResult, Content: "old answer", Done: true}
	readUntil("old answer")
	oldSession := e.sessions.GetOrCreateActive(key)
	env.waitFor("old turn unlocked", 3*time.Second, func() bool { return !oldSession.Busy() })
	send("/new fresh")
	select {
	case <-oldProcess.closeStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("bridge /new did not close the old process")
	}
	if e.sessions.GetOrCreateActive(key) == oldSession {
		t.Fatal("bridge /new kept the old session active during Close")
	}
	// Bridge serializes inbound frames; this message must run in the new
	// context when the adapter resumes reading after the blocked command.
	send("new question")
	oldProcess.release()
	readUntil("fresh")
	waitForPrompt(newProcess)
	assertNewCommandPrompts(t, newProcess, "new question")
	newProcess.events <- Event{Type: EventResult, Content: "fresh answer", Done: true}
	readUntil("fresh answer")
	send("/history")
	readUntil("new question")
}
