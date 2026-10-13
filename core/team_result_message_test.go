package core

import (
	"context"
	"errors"
	"testing"
)

func TestResultHandoffDeliveryFailureRetryAndOriginalRequester(t *testing.T) {
	manager, dev, mp, dp, _, store := coordinatorFixture(t)
	m := &threadCUJPlatform{coordinationPlatform: mp, role: "manager"}
	d := &threadCUJPlatform{coordinationPlatform: dp, role: "dev", resultError: errors.New("transport failed")}
	manager.platforms, dev.platforms = []Platform{m}, []Platform{d}
	store.mutateProject("project", func(p *TeamProject) error {
		p.RequesterID = "newer-user"
		p.ManagerContexts = map[string]TeamManagerContext{"test:group:root:initial": {RequesterID: "original-user", Instruction: "original goal"}}
		p.Tasks = map[string]TeamTask{"T001": {ID: "T001", Assignee: "dev", Attempt: 1, Status: "running", SessionKey: "test:group:root:task", RootMessageID: "task", RequesterID: "original-user", ManagerSessionKey: "test:group:root:initial"}}
		return nil
	})
	all, _ := store.All()
	task := all["project"].Tasks["T001"]
	if _, err := dev.deliverWorkflowResult(context.Background(), d, task.SessionKey, "turn", "Original result", &task); err == nil {
		t.Fatal("transport failure counted as handoff")
	}
	all, _ = store.All()
	if all["project"].Tasks["T001"].ResultMessageID != "" || len(all["project"].ManagerThreads) != 0 {
		t.Fatal("failed send created a recipient thread")
	}
	d.resultError = nil
	for i := 0; i < 2; i++ {
		if sent, err := dev.deliverWorkflowResult(context.Background(), d, task.SessionKey, "turn", "Original result", &task); err != nil || !sent {
			t.Fatalf("retry: %v %v", sent, err)
		}
	}
	if got := d.handoffs(); len(got) != 1 || got[0].to != "manager" || got[0].content != "Original result" {
		t.Fatalf("duplicated or rewritten result: %+v", got)
	}
	key := "test:group:root:result-dev-1"
	project, err := manager.teamProject(key)
	if err != nil || project.RequesterID != "original-user" || project.ManagerContexts[key].Instruction != "original goal" {
		t.Fatalf("lost requester/instruction: %+v %v", project, err)
	}
	if _, err := manager.deliverWorkflowResult(context.Background(), m, key, "review-turn", "Reviewed result", nil); err != nil {
		t.Fatal(err)
	}
	if got := m.handoffs(); len(got) != 1 || got[0].to != "" || got[0].content != "Reviewed result" {
		t.Fatalf("ordinary manager result must not @ the requester: %+v", got)
	}
	all, _ = store.All()
	if all["project"].ManagerContexts["test:group:root:result-manager-1"].RequesterID != "original-user" {
		t.Fatal("removing the mention lost the original requester for future decisions")
	}
	task.Attempt++
	if _, err := dev.deliverWorkflowResult(context.Background(), d, task.SessionKey, "stale-turn", "Late result", &task); err == nil {
		t.Fatal("stale attempt emitted a handoff")
	}
}
