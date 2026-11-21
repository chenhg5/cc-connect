package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type globalTaskJourneyAgent struct {
	namedStubGlobalTaskAgent
}

func (a *globalTaskJourneyAgent) StartSession(context.Context, string) (AgentSession, error) {
	s := newCUJAgentSession()
	s.reply = "reply from " + filepath.Base(a.workDir)
	return s, nil
}

func testGlobalTaskDMIsolationAfterInboundMessage(t *testing.T) {
	root := t.TempDir()
	alpha, beta := filepath.Join(root, "alpha"), filepath.Join(root, "beta")
	for _, dir := range []string{alpha, beta} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	agentName := "test-global-task-dm-isolation"
	newAgent := func(workDir string) *globalTaskJourneyAgent {
		return &globalTaskJourneyAgent{namedStubGlobalTaskAgent{
			name: agentName,
			stubGlobalTaskAgent: stubGlobalTaskAgent{
				stubWorkDirAgent: stubWorkDirAgent{workDir: workDir},
				sessions: []AgentSessionInfo{{
					ID: "beta-session-id", Summary: "Beta refactor", WorkDir: beta,
				}},
			},
		}}
	}
	RegisterAgent(agentName, func(opts map[string]any) (Agent, error) {
		return newAgent(opts["work_dir"].(string)), nil
	})
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", newAgent(alpha), []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	defer e.cancel()
	e.SetAdminFrom("alice")
	e.SetGlobalSessionRouting(true, []string{root})
	send := func(key, content string) {
		p.clearSent()
		e.ReceiveMessage(p, &Message{
			SessionKey: key, Platform: "test", UserID: "alice",
			Content: content, ReplyCtx: key,
		})
	}
	chatA, chatB := "test:d:chat-a:alice", "test:d:chat-b:alice"
	send(chatA, "/tasks beta")
	if got := strings.Join(p.getSent(), "\n"); !strings.Contains(got, "Beta refactor") {
		t.Fatalf("/tasks = %q", got)
	}
	send(chatA, "/goto 1")
	if got := strings.Join(p.getSent(), "\n"); !strings.Contains(got, "Resumed:") {
		t.Fatalf("/goto = %q", got)
	}
	send(chatA, "continue in beta")
	if got := strings.Join(waitForPlatformSend(p, 1, 3*time.Second), "\n"); !strings.Contains(got, "reply from beta") {
		t.Fatalf("chat A reply = %q, want beta", got)
	}
	// This extra inbound turn used to promote /goto's exact binding into the
	// participant fallback and silently route this user's other DM to beta.
	send(chatB, "continue in original project")
	if got := strings.Join(waitForPlatformSend(p, 1, 3*time.Second), "\n"); !strings.Contains(got, "reply from alpha") {
		t.Fatalf("chat B reply = %q, want original alpha workspace", got)
	}
}

func TestSendWorkDirBindingPreservesProjectCommandContext(t *testing.T) {
	baseDir, targetDir := t.TempDir(), t.TempDir()
	p := &stubPlatformEngine{n: "test"}
	baseAgent := &stubWorkDirAgent{workDir: baseDir}
	e := NewEngine("test", baseAgent, []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	defer e.cancel()
	// Seed the existing workspace pool so the test distinguishes the resolved
	// command context without depending on an agent factory.
	e.workspacePool = newWorkspacePool(DefaultWorkspaceIdleTimeout)
	ws := e.workspacePool.GetOrCreate(targetDir)
	ws.agent = &stubWorkDirAgent{workDir: targetDir}
	ws.sessions = NewSessionManager("")
	msg := &Message{SessionKey: "test:d:chat:alice", Platform: "test", UserID: "alice", Content: "/status", ReplyCtx: "ctx"}
	e.ReceiveMessage(p, msg)
	before := strings.Join(p.getSent(), "\n")
	e.bindSendWorkDir(msg.SessionKey, targetDir)
	p.clearSent()
	e.ReceiveMessage(p, msg)
	after := strings.Join(p.getSent(), "\n")
	for _, got := range []string{before, after} {
		if !strings.Contains(got, baseDir) || strings.Contains(got, targetDir) {
			t.Fatalf("/status = %q, want project directory %q, not send --cwd directory %q", got, baseDir, targetDir)
		}
	}
	agent, sessions, _, workspace, err := e.commandContextWithWorkspace(p, msg)
	if err != nil || agent != baseAgent || sessions != e.sessions || workspace != "" {
		t.Fatalf("send --cwd changed command context: agent=%T workspace=%q err=%v", agent, workspace, err)
	}
}

func TestListGlobalTasksKeywordBeforeDeduplication(t *testing.T) {
	root := t.TempDir()
	agent := &stubGlobalTaskAgent{sessions: []AgentSessionInfo{
		{ID: "split-session", Summary: "newer prompt", WorkDir: root, ModifiedAt: time.Unix(200, 0)},
		{ID: "split-session", Summary: "older matching prompt", WorkDir: root, ModifiedAt: time.Unix(100, 0)},
	}}
	root, err := canonicalExistingDir(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := listGlobalTasks(context.Background(), agent, []string{root}, "matching")
	if err != nil || len(got) != 1 || got[0].Summary != "older matching prompt" {
		t.Fatalf("keyword search = %+v, %v; want matching older segment", got, err)
	}
}
