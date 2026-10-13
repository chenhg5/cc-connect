package core

import (
	"context"
	"strings"
	"testing"
)

func TestTeamReview_RestartPreservesResultContextAndWorkerAttempt(t *testing.T) {
	manager, _, mp, _, _, store := coordinatorFixture(t)
	manager.platforms = []Platform{&threadCUJPlatform{coordinationPlatform: mp, role: "manager"}}
	key := "test:group:root:submitted-result"
	if err := store.mutateProject("project", func(p *TeamProject) error {
		p.Round, p.WorkflowEpoch = 3, 7
		p.ManagerContexts = map[string]TeamManagerContext{key: {Round: 3, RequesterID: "original-user", Instruction: "continue without overdesign"}}
		p.ManagerThreads = []string{key}
		p.Tasks = map[string]TeamTask{"T023": {ID: "T023", Round: 3, Epoch: 7, Assignee: "dev", Attempt: 1, Status: "submitted", Result: "existing verified artifacts", ResultMessageID: "submitted-result"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	all, _ := store.All()
	project := all["project"]
	_, msg, err := manager.teamReviewMessage(project, "T023")
	if err != nil {
		t.Fatal(err)
	}
	if msg.SessionKey != key || msg.MessageID != "submitted-result" || msg.UserID != "original-user" || msg.teamEpoch != 7 || !msg.teamInternal || msg.IsPermissionResponse {
		t.Fatalf("lost review routing or authority: %+v", msg)
	}
	if !strings.Contains(msg.Content, "continue without overdesign") || !strings.Contains(msg.Content, "existing verified artifacts") {
		t.Fatal("lost persisted instructions or evidence")
	}
	if _, err := manager.RunTeamAction(context.Background(), TeamActionRequest{SessionKey: key, Action: "review", TaskID: "T023"}); err != nil {
		t.Fatal(err)
	}
	ma := manager.agent.(*coordinationAgent).cujAgent
	awaitQuery(t, func() bool {
		ma.mu.Lock()
		defer ma.mu.Unlock()
		for _, s := range ma.sessions {
			s.mu.Lock()
			found := strings.Contains(strings.Join(s.sentPrompts, "\n"), "existing verified artifacts")
			s.mu.Unlock()
			if found {
				return true
			}
		}
		return false
	})
	all, _ = store.All()
	task := all["project"].Tasks["T023"]
	if task.Status != "submitted" || task.Attempt != 1 || task.ResultMessageID != "submitted-result" {
		t.Fatalf("review re-ran or accepted worker task: %+v", task)
	}
}

func TestTeamReview_RejectsStaleOrUnsubmittedResults(t *testing.T) {
	e := &Engine{}
	for _, task := range []TeamTask{
		{Status: "running", Round: 3, Epoch: 7},
		{Status: "accepted", Round: 3, Epoch: 7},
		{Status: "submitted", Round: 2, Epoch: 7},
		{Status: "submitted", Round: 3, Epoch: 6},
		{Status: "submitted", Round: 3, Epoch: 7},
	} {
		p := TeamProject{Round: 3, WorkflowEpoch: 7, Tasks: map[string]TeamTask{"T023": task}}
		if _, _, err := e.teamReviewMessage(p, "T023"); err == nil {
			t.Fatalf("admitted ineligible task: %+v", task)
		}
	}
}
