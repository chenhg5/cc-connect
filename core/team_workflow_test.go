package core

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkflow_PauseBlocksDispatchAndLateCompletion(t *testing.T) {
	s := NewTeamProjectStore(filepath.Join(t.TempDir(), "projects.json"))
	if err := s.Put(TeamProject{ID: "p", State: "ready", Directories: []string{t.TempDir()}, Bots: []string{"manager", "dev"}}); err != nil {
		t.Fatal(err)
	}
	task, err := s.assignTask("p", TeamActionRequest{To: "dev", Message: "Implement", Title: "Fix login", Reason: "Accepted plan", Deliverable: "Patch and tests"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.setWorkflowPaused("p", true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.assignTask("p", TeamActionRequest{To: "dev", Message: "New task"}); err == nil {
		t.Fatal("paused project dispatched")
	}
	if err = s.updateTask("p", task.ID, func(tk *TeamTask) error {
		return applyTaskReport(tk, TeamMemberReport{ID: "old", Status: "completed", Content: "late result"})
	}); err != nil {
		t.Fatal(err)
	}
	all, _ := NewTeamProjectStore(s.path).All()
	if !all["p"].Paused || all["p"].Tasks[task.ID].Status != "stopped" {
		t.Fatal("pause or stopped task overwritten")
	}
}
func TestWorkflow_SubmittedRequiresAcceptanceAndDependencies(t *testing.T) {
	s := NewTeamProjectStore(filepath.Join(t.TempDir(), "projects.json"))
	s.Put(TeamProject{ID: "p", State: "ready", Directories: []string{t.TempDir()}, Bots: []string{"manager", "dev", "test"}})
	task, err := s.assignTask("p", TeamActionRequest{To: "dev", Title: "Fix", Reason: "Bug", Deliverable: "Tests", Message: "Fix"})
	if err != nil {
		t.Fatal(err)
	}
	s.updateTask("p", task.ID, func(tk *TeamTask) error {
		return applyTaskReport(tk, TeamMemberReport{ID: "run", Status: "completed", Content: "patch.go; tests passed"})
	})
	req := TeamActionRequest{To: "test", Title: "Verify", Reason: "Review", Deliverable: "Report", Message: "Verify", DependsOn: []string{task.ID}}
	if _, err = s.assignTask("p", req); err == nil {
		t.Fatal("unaccepted dependency dispatched")
	}
	if err = s.acceptTask("p", task.ID, ""); err == nil {
		t.Fatal("accepted without evidence")
	}
	if err = s.acceptTask("p", task.ID, "Inspected patch.go and test results"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.assignTask("p", req); err != nil {
		t.Fatal(err)
	}
	all, _ := s.All()
	if all["p"].Tasks[task.ID].Title != "Fix" {
		t.Fatal("task metadata lost")
	}
}
func TestWorkflow_CompactCardHidesRawTraceKeepsApprovalAndAnswer(t *testing.T) {
	var answer string
	raw := &queryTestCard{split: true, onAnswer: func(s string) { answer = s }}
	c := newWorkflowCard(raw, NewI18n(LangEnglish), "compact", "T001 · Fix login")
	c.observe(Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "SECRET_RAW_COMMAND"})
	c.Update(context.Background(), "RAW_TRACE_AND_THINKING")
	c.SetPrompt(context.Background(), NewCard().Markdown("APPROVE_THIS_COMMAND").Build())
	text, prompt, _ := raw.snapshot()
	if strings.Contains(text, "RAW") || !strings.Contains(text, "T001") || prompt == nil {
		t.Fatalf("bad progress: %s", text)
	}
	c.FinalizeWithAnswer(context.Background(), "RAW_TRACE", "Patch delivered")
	if answer != "Patch delivered" {
		t.Fatal("answer lost")
	}
}

func TestWorkflow_QuietManagerReportDoesNotAllocateCards(t *testing.T) {
	creates, sends := 0, 0
	c := &quietWorkflowCard{create: func() (StreamingCard, error) {
		creates++
		return &queryTestCard{split: true, onAnswer: func(string) {}}, nil
	}, send: func(string) error { sends++; return nil }}
	c.Update(context.Background(), "checking")
	c.FinalizeWithAnswer(context.Background(), "checking", "")
	if creates != 0 || sends != 0 {
		t.Fatal("silent bookkeeping created messages")
	}
	c = &quietWorkflowCard{create: func() (StreamingCard, error) {
		creates++
		return &queryTestCard{split: true, onAnswer: func(string) {}}, nil
	}, send: func(string) error { sends++; return nil }}
	if err := c.SetPrompt(context.Background(), NewCard().Markdown("permission").Build()); err != nil {
		t.Fatal(err)
	}
	if creates != 1 {
		t.Fatal("approval prompt missing")
	}
}
func TestWorkflow_StaleQueueCannotRestartAfterResume(t *testing.T) {
	e := NewEngine("manager", &stubAgent{}, nil, t.TempDir()+"/sessions.json", LangEnglish)
	defer e.Stop()
	s := NewTeamProjectStore(t.TempDir() + "/projects.json")
	e.SetTeamProjects(s, nil)
	s.Put(TeamProject{ID: "p", State: "ready", Platform: "test", ChatID: "group", Manager: "manager", Bots: []string{"manager"}, Directories: []string{t.TempDir()}})
	s.setWorkflowPaused("p", true)
	s.setWorkflowPaused("p", false)
	if !e.workflowMessageStopped("test:group:owner", 0, &interactiveState{}) {
		t.Fatal("stale queued work restarted after pause/resume")
	}
}

func TestWorkflow_UnrelatedReportCannotCompleteAssignedTask(t *testing.T) {
	s := NewTeamProjectStore(t.TempDir() + "/projects.json")
	s.Put(TeamProject{ID: "p", State: "ready", Directories: []string{t.TempDir()}, Bots: []string{"manager", "dev"}})
	task, err := s.assignTask("p", TeamActionRequest{To: "dev", Message: "Do task"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.saveReport("p", TeamMemberReport{Bot: "dev", ID: "unrelated-conversation", Status: "completed", Content: "unrelated answer"}); err != nil {
		t.Fatal(err)
	}
	all, _ := s.All()
	if all["p"].Tasks[task.ID].Status != "queued" {
		t.Fatal("unrelated conversation completed task")
	}
}

func TestWorkflow_ManagerCanPauseButCannotAutoResume(t *testing.T) {
	manager, _, _, _, _, store := coordinatorFixture(t)
	req := TeamActionRequest{SessionKey: "test:group:owner", Action: "pause"}
	if _, err := manager.RunTeamAction(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	req.Action = "resume"
	if _, err := manager.RunTeamAction(context.Background(), req); err == nil {
		t.Fatal("manager automatically resumed project")
	}
	req.Action = "status"
	if _, err := manager.RunTeamAction(context.Background(), req); err != nil {
		t.Fatal("paused project status unavailable")
	}
	all, _ := store.All()
	if !all["project"].Paused {
		t.Fatal("pause not persisted")
	}
}
