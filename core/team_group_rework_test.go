package core

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestGroupReworkRoutesOriginalTaskAndRejectsDuplicateOrWrongSender(t *testing.T) {
	_, dev, _, dp, _, store := coordinatorFixture(t)
	p := &threadCUJPlatform{coordinationPlatform: dp, role: "dev"}
	dev.platforms = []Platform{p}
	store.mutateProject("project", func(p *TeamProject) error {
		p.Tasks = map[string]TeamTask{"T001": {ID: "T001", Assignee: "dev", Status: "submitted", Attempt: 1, RootMessageID: "original", SessionKey: "test:group:root:original", Result: "original result", ResultMessageID: "old-result", RequesterID: "requester", NativeThread: true}}
		return nil
	})
	makeMessage := func(user, body string) *Message {
		return &Message{SessionKey: "test:group", MessageID: "group-rework", UserID: user, Content: body}
	}
	project, _ := dev.teamProject("test:group")
	for _, msg := range []*Message{makeMessage("outsider", "T001 fix"), makeMessage("owner", "T001 and T002 fix"), makeMessage("owner", "T999 fix")} {
		if _, err := dev.routeMemberGroupRework(p, msg, project); err == nil {
			t.Fatal("ambiguous or unauthorized rework accepted")
		}
	}
	msg := makeMessage("requester", "T001 返工：补测清理失败")
	if duplicate, err := dev.routeMemberGroupRework(p, msg, project); err != nil || duplicate {
		t.Fatalf("route failed: %v %v", duplicate, err)
	}
	if msg.SessionKey != "test:group:root:group-rework" || msg.ReplyCtx != msg.SessionKey || !strings.Contains(msg.Content, "original result") || !strings.Contains(msg.Content, "old-result") {
		t.Fatalf("lost new destination or prior result: %+v", msg)
	}
	project, _ = dev.teamProject(msg.SessionKey)
	if duplicate, err := dev.routeMemberGroupRework(p, makeMessage("requester", "T001 fix"), project); err != nil || !duplicate {
		t.Fatalf("duplicate started another attempt: %v %v", duplicate, err)
	}
	if _, err := dev.registerTaskFollowup(&Message{SessionKey: "test:group:root:original", MessageID: "old-reply", Content: "fix", UserID: "owner"}); err == nil {
		t.Fatal("old thread executed another attempt")
	}
	if _, err := dev.deliverWorkflowResult(context.Background(), p, "test:group:root:original", "late", "late result", nil); err == nil || len(p.handoffs()) != 0 {
		t.Fatal("old unscoped result escaped to group")
	}
	// Leave only running/queued state for fixture teardown; stale output cannot
	// overwrite it or wake the manager as an unrelated report.
	dev.PublishTeamReport("test:group:root:original", "late", "completed", "late result")
	all, _ := store.All()
	task := all["project"].Tasks["T001"]
	if task.Attempt != 2 || task.Status != "queued" || task.History[0].RootMessageID != "original" || task.History[0].ResultMessageID != "old-result" {
		t.Fatalf("history or current attempt corrupted: %+v", task)
	}
}

func TestGroupReworkSendFailureNeverFallsBackToOldThread(t *testing.T) {
	store := NewTeamProjectStore(t.TempDir() + "/projects.json")
	p := &threadCUJPlatform{coordinationPlatform: &coordinationPlatform{queryTestPlatform: queryTestPlatform{stubCardPlatform: stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}}, role: "manager", taskError: errors.New("send failed")}
	dp := &threadCUJPlatform{coordinationPlatform: &coordinationPlatform{queryTestPlatform: queryTestPlatform{stubCardPlatform: stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}}, role: "dev"}
	m := NewEngine("manager", &stubAgent{}, []Platform{p}, t.TempDir()+"/manager.json", LangEnglish)
	d := NewEngine("dev", &stubAgent{}, []Platform{dp}, t.TempDir()+"/dev.json", LangEnglish)
	defer m.Stop()
	defer d.Stop()
	m.SetTeamProjects(store, nil)
	d.SetTeamProjects(store, nil)
	project := TeamProject{ID: "p", Platform: "test", ChatID: "group", State: "ready", Manager: "manager", Creator: "owner", Bots: []string{"manager", "dev"}, Directories: []string{t.TempDir()}, Tasks: map[string]TeamTask{"T001": {ID: "T001", Assignee: "dev", Status: "submitted", Attempt: 1, NativeThread: true, RootMessageID: "old", SessionKey: "test:group:root:old", Result: "first result"}}}
	if err := store.Put(project); err != nil {
		t.Fatal(err)
	}
	if _, err := m.dispatchWorkflowTask(context.Background(), project, TeamActionRequest{SessionKey: "test:group", TaskID: "T001", To: "dev", Message: "correct edge case"}); err == nil {
		t.Fatal("failed send accepted")
	}
	all, _ := store.All()
	task := all["p"].Tasks["T001"]
	if task.Status != "failed" || task.RootMessageID != "" || task.SessionKey != "" || task.History[0].RootMessageID != "old" || len(dp.snapshots()) != 0 {
		t.Fatalf("failed handoff reused old thread or executed: %+v", task)
	}
}
