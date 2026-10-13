package core

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type coordinationPlatform struct {
	queryTestPlatform
	groupPolicy func(string, string) GroupMessagePolicy
}

func (p *coordinationPlatform) SetGroupMessagePolicy(fn func(string, string) GroupMessagePolicy) {
	p.groupPolicy = fn
}

type coordinationAgent struct{ *cujAgent }

func (*coordinationAgent) Name() string { return "coordination-test" }
func (a *coordinationAgent) WorkspaceAgentOptions() map[string]any {
	return map[string]any{"test_agent": a}
}

func (p *coordinationPlatform) ReconstructReplyCtx(key string) (any, error) { return key, nil }

func coordinatorFixture(t *testing.T) (*Engine, *Engine, *coordinationPlatform, *coordinationPlatform, *cujAgent, *TeamProjectStore) {
	t.Helper()
	RegisterAgent("coordination-test", func(opts map[string]any) (Agent, error) {
		return opts["test_agent"].(*coordinationAgent), nil
	})
	t.Cleanup(func() { delete(agentFactories, "coordination-test") })
	dir := t.TempDir()
	store := NewTeamProjectStore(filepath.Join(dir, "projects.json"))
	if err := store.Put(TeamProject{ID: "project", Team: "team", Platform: "test", ChatID: "group", Manager: "manager", Creator: "owner", Bots: []string{"manager", "dev"}, Directories: []string{dir}, State: "ready"}); err != nil {
		t.Fatal(err)
	}
	mp := &coordinationPlatform{queryTestPlatform: queryTestPlatform{split: true, stubCardPlatform: stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}}
	dp := &coordinationPlatform{queryTestPlatform: queryTestPlatform{split: true, stubCardPlatform: stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}}
	ma, da := &cujAgent{}, &cujAgent{}
	manager := NewEngine("manager", &coordinationAgent{ma}, []Platform{mp}, filepath.Join(dir, "manager.json"), LangEnglish)
	dev := NewEngine("dev", &coordinationAgent{da}, []Platform{dp}, filepath.Join(dir, "dev.json"), LangEnglish)
	for _, e := range []*Engine{manager, dev} {
		e.SetTeamProjects(store, nil)
		e.SetTeamCoordination(true, e == manager, nil)
		e.SetTeamManagerCheck(func(context.Context, TeamProject) error { return nil })
		if _, _, err := e.getOrCreateWorkspaceAgentForSession(dir, "test:group:owner"); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		dev.cancel()
		stop := func(e *Engine) {
			e.Stop()
			awaitQuery(t, func() bool {
				for _, ws := range e.workspacePool.All() {
					if ws.HasActiveTurn() || ws.sessions.GetOrCreateActive("test:group:owner").Busy() {
						return false
					}
				}
				return true
			})
		}
		stop(dev)
		// A report is saved before its asynchronous ReceiveMessage call starts.
		// Drain that delivery before shutting down the manager and unregistering
		// the test agent; merely observing idle workspaces can miss it entirely.
		all, err := store.All()
		if err != nil {
			t.Fatal(err)
		}
		for _, report := range all["project"].Reports {
			if !roundAcceptsWork(all["project"]) {
				break // Terminal projects intentionally suppress report delivery.
			}
			if report.Epoch != all["project"].WorkflowEpoch || report.Status == "stopped" || report.Status == "running" || report.Status == "queued" {
				continue
			}
			awaitQuery(t, func() bool {
				ma.mu.Lock()
				defer ma.mu.Unlock()
				for _, session := range ma.sessions {
					session.mu.Lock()
					prompts := strings.Join(session.sentPrompts, "\n")
					session.mu.Unlock()
					if strings.Contains(prompts, report.ID) {
						return true
					}
				}
				return false
			})
		}
		manager.cancel()
		stop(manager)
	})
	return manager, dev, mp, dp, da, store
}

func TestTeamCoordination_GroupPolicyRequiresActualOwnerOrDirectMention(t *testing.T) {
	manager, dev, mp, dp, _, store := coordinatorFixture(t)
	dev.SetBotOwnership("", "member-owner", nil)
	if mp.groupPolicy == nil || dp.groupPolicy == nil {
		t.Fatal("group policy must be installed for both manager and member bots")
	}
	if err := store.mutateProject("project", func(pr *TeamProject) error {
		pr.RequesterID = "visitor"
		pr.ManagerContexts = map[string]TeamManagerContext{"test:group:root:thread": {RequesterID: "visitor"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, key, sender string
		p                 *coordinationPlatform
		want              GroupMessagePolicy
	}{
		{"owner", "test:group:owner", "owner", mp, GroupMessagePolicy{DefaultReceiver: true}},
		{"visitor using owner session", "test:group:owner", "visitor", mp, GroupMessagePolicy{RequireMention: true}},
		{"owner shared thread", "test:group:root:thread", "owner", mp, GroupMessagePolicy{DefaultReceiver: true}},
		{"requester shared thread", "test:group:root:thread", "visitor", mp, GroupMessagePolicy{RequireMention: true}},
		{"empty sender", "test:group:owner", "", mp, GroupMessagePolicy{RequireMention: true}},
		{"owner addresses member", "test:group:root:thread", "owner", dp, GroupMessagePolicy{RequireMention: true}},
		{"member app scoped owner", "test:group:root:thread", "member-owner", dp, GroupMessagePolicy{}},
		{"visitor addresses member", "test:group:root:thread", "visitor", dp, GroupMessagePolicy{RequireMention: true}},
		{"unrelated group", "test:other:visitor", "visitor", mp, GroupMessagePolicy{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.groupPolicy(tc.key, tc.sender); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
	manager.SetTeamCoordination(false, true, nil)
	if got := mp.groupPolicy("test:group:owner", "owner"); got != (GroupMessagePolicy{}) {
		t.Fatalf("disabled coordination must not enable unmentioned delivery: %+v", got)
	}
	if got := mp.groupPolicy("test:group:visitor", "visitor"); !got.RequireMention {
		t.Fatal("disabled coordination must retain other members' mention requirement")
	}
}

func TestTeamCoordination_ReportDeliveryVisibilityReachesManager(t *testing.T) {
	for _, published := range []bool{false, true} {
		name := "unpublished"
		if published {
			name = "published"
		}
		t.Run(name, func(t *testing.T) {
			manager, dev, _, _, _, store := coordinatorFixture(t)
			resultID := ""
			if published {
				resultID = "result-dev-1"
			}
			if err := store.mutateProject("project", func(pr *TeamProject) error {
				pr.Tasks = map[string]TeamTask{"T001": {
					ID: "T001", Assignee: "dev", Attempt: 1, Status: "running",
					SessionKey: "test:group:owner", ResultMessageID: resultID,
				}}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			const answer = "API inventory: create(ViewOptions), required name; source api/view.go:12."
			dev.PublishTeamReport("test:group:owner", "api-report", "completed", answer)
			agent := manager.agent.(*coordinationAgent).cujAgent
			var prompt string
			awaitQuery(t, func() bool {
				agent.mu.Lock()
				defer agent.mu.Unlock()
				for _, session := range agent.sessions {
					for _, sent := range session.getSentPrompts() {
						if strings.Contains(sent, "api-report") {
							prompt = sent
							return true
						}
					}
				}
				return false
			})
			if !strings.Contains(prompt, answer) {
				t.Fatal("handoff changed or dropped the member's original deliverable")
			}
			visible := strings.Contains(prompt, "already published in the project group")
			missing := strings.Contains(prompt, "No published result message is confirmed")
			if visible != published || missing == published {
				t.Fatalf("manager must know whether the user already received the answer (published=%v)", published)
			}
			if published && !strings.Contains(prompt, "NO_REPLY") {
				t.Fatal("visible complete answer should allow silent acceptance")
			}
		})
	}
}

func TestTeamCoordination_DispatchWaitsForApprovalAndReportsFailure(t *testing.T) {
	manager, dev, mp, dp, agent, store := coordinatorFixture(t)
	agent.setNextSessionEvents([]Event{{Type: EventPermissionRequest, RequestID: "permission", ToolName: "Bash", ToolInput: "run tests"}}, 0)
	req := TeamActionRequest{From: "manager", SessionKey: "test:group:owner", Action: "dispatch", To: "dev", Message: "implement the plan"}
	if _, err := manager.RunTeamAction(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	awaitQuery(t, func() bool {
		cs := dp.snapshots()
		if len(cs) == 0 {
			return false
		}
		_, prompt, _ := cs[0].snapshot()
		return prompt != nil
	})
	all, err := store.All()
	if err != nil {
		t.Fatal(err)
	}
	if all["project"].Reports["dev"].Status != "waiting_approval" {
		t.Fatal("approval wait not visible to manager")
	}
	if _, err := manager.RunTeamAction(context.Background(), req); err == nil {
		t.Fatal("duplicated an active task")
	}
	dev.ReceiveMessage(dp, &Message{Platform: "test", SessionKey: "test:group:owner", UserID: "owner", Content: "deny", ReplyCtx: "ctx"})
	agent.mu.Lock()
	session := agent.sessions[0]
	agent.mu.Unlock()
	session.events <- Event{Type: EventError, Error: errors.New("tests failed")}
	awaitQuery(t, func() bool { all, _ := store.All(); return all["project"].Reports["dev"].Status == "failed" })
	awaitQuery(t, func() bool { return len(mp.snapshots()) >= 2 })
	// The manager's own completion must never dispatch itself or create a report loop.
	all, _ = store.All()
	if _, ok := all["project"].Reports["manager"]; ok {
		t.Fatal("manager feedback loop")
	}
	if _, err := dev.RunTeamAction(context.Background(), req); err == nil {
		t.Fatal("member gained manager actions")
	}
	req.SessionKey = "test:other:owner"
	if _, err := manager.RunTeamAction(context.Background(), req); err == nil {
		t.Fatal("cross-project dispatch allowed")
	}
}

func TestTeamCoordination_InternalReportCannotApprove(t *testing.T) {
	manager, dev, _, dp, agent, store := coordinatorFixture(t)
	agent.setNextSessionEvents([]Event{{Type: EventPermissionRequest, RequestID: "permission", ToolName: "Bash", ToolInput: "run tests"}}, 0)
	if _, err := manager.RunTeamAction(context.Background(), TeamActionRequest{SessionKey: "test:group:owner", Action: "dispatch", To: "dev", Message: "test"}); err != nil {
		t.Fatal(err)
	}
	awaitQuery(t, func() bool {
		cs := dp.snapshots()
		if len(cs) == 0 {
			return false
		}
		_, prompt, _ := cs[0].snapshot()
		return prompt != nil
	})
	all, _ := store.All()
	_, internal, err := dev.teamMessage(all["project"], "allow")
	if err != nil {
		t.Fatal(err)
	}
	dev.ReceiveMessage(dp, internal)
	_, prompt, _ := dp.snapshots()[0].snapshot()
	if prompt == nil {
		t.Fatal("internal message approved a tool")
	}
}

func TestTeamCoordination_PreservesRoleCreationReason(t *testing.T) {
	manager, _, _, _, _, _ := coordinatorFixture(t)
	var reason string
	manager.SetTeamCoordination(true, true, func(_ context.Context, _ Platform, p TeamProject, role, backend, why string) (string, error) {
		if p.ID != "project" || role != "tester" || backend != "codex" {
			t.Fatal("role context was lost")
		}
		reason = why
		return "awaiting_authorization", nil
	})
	_, err := manager.RunTeamAction(context.Background(), TeamActionRequest{SessionKey: "test:group:owner", Action: "ensure-role", Role: "tester", Backend: "codex", Reason: "Run regression tests and deliver findings"})
	if err != nil || reason != "Run regression tests and deliver findings" {
		t.Fatalf("authorization reason not forwarded: %q, %v", reason, err)
	}
}

func TestTeamCoordination_ResultsPersistAcrossRestart(t *testing.T) {
	_, dev, _, _, _, store := coordinatorFixture(t)
	dev.PublishTeamReport("test:group:owner", "run-1", "completed", "Plan document: docs/plan.md; unresolved decision remains")
	restored := NewTeamProjectStore(store.path)
	all, err := restored.All()
	if err != nil {
		t.Fatal(err)
	}
	report := all["project"].Reports["dev"]
	if report.Status != "completed" || !strings.Contains(report.Content, "unresolved") || report.UpdatedAt.Before(time.Now().Add(-time.Minute)) {
		t.Fatalf("report lost: %+v", report)
	}
}

func TestTeamCoordination_RejectedDispatchIsReported(t *testing.T) {
	manager, dev, mp, _, _, store := coordinatorFixture(t)
	dev.SetBannedWords([]string{"blocked-task"})
	_, err := manager.RunTeamAction(context.Background(), TeamActionRequest{SessionKey: "test:group:owner", Action: "dispatch", To: "dev", Message: "blocked-task"})
	if err != nil {
		t.Fatal(err)
	}
	awaitQuery(t, func() bool { all, _ := store.All(); return all["project"].Reports["dev"].Status == "failed" })
	awaitQuery(t, func() bool { return len(mp.snapshots()) == 2 })
}

func TestTeamCoordination_LegacyRelayCannotBypassApproval(t *testing.T) {
	manager, dev, _, _, agent, _ := coordinatorFixture(t)
	rm := NewRelayManager(t.TempDir())
	rm.RegisterEngine("manager", manager)
	rm.RegisterEngine("dev", dev)
	_, err := rm.Send(context.Background(), RelayRequest{From: "manager", To: "dev", SessionKey: "test:group:owner", Message: "test"})
	if err == nil || !strings.Contains(err.Error(), "team dispatch") {
		t.Fatalf("unsafe relay allowed: %v", err)
	}
	agent.mu.Lock()
	defer agent.mu.Unlock()
	if len(agent.sessions) != 0 {
		t.Fatal("legacy relay started a session")
	}
}
