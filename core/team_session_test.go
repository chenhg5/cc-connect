package core

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func setTeamNextTurnEvents(a *cujAgent, events []Event, delay int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := len(a.sessions) - 1; i >= 0; i-- {
		s := a.sessions[i]
		if s.Alive() {
			s.mu.Lock()
			s.pendingEvents = append([]Event(nil), events...)
			s.pendingDelayMs = delay
			s.mu.Unlock()
			return
		}
	}
	a.nextSessionEvents = append([]Event(nil), events...)
	a.nextSessionDelayMs = delay
}

func TestSharedConversationMigratesPersistsAndResets(t *testing.T) {
	path := t.TempDir() + "/sessions.json"
	sm := NewSessionManager(path)
	old := sm.GetOrCreateActive("test:group:root:one")
	old.SetAgentSessionID("native-old", "test")
	recent := sm.GetOrCreateActive("test:group:root:two")
	recent.SetAgentSessionID("native-recent", "test")
	recent.mu.Lock()
	recent.UpdatedAt = time.Now().Add(time.Minute)
	recent.mu.Unlock()
	other := sm.GetOrCreateActive("test:another:root:x")
	sm.ShareConversation("test:group", "test:group:member:project:dev:0")
	if got := sm.GetOrCreateActive("test:group:root:three"); got != recent {
		t.Fatal("upgrade lost most recent member context")
	}
	if len(sm.sessions) != 3 || sm.GetOrCreateActive("test:another:root:x") != other {
		t.Fatal("upgrade deleted histories or changed another group")
	}
	sm = NewSessionManager(path)
	got := sm.GetOrCreateActive("test:group:root:four")
	if got.GetAgentSessionID() != "native-recent" {
		t.Fatal("restart lost native resume ID")
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if sm.GetOrCreateActive("test:group:root:five") != got {
				t.Error("concurrent session creation")
			}
		}()
	}
	wg.Wait()
	gen, ok := got.TryLock()
	if !ok {
		t.Fatal("lock")
	}
	if _, ok = sm.GetOrCreateActive("test:group:root:six").TryLock(); ok {
		t.Fatal("different thread bypassed member lock")
	}
	got.Unlock(gen)
	reset := sm.NewSession("test:group:root:four", "fresh")
	if reset == got || sm.GetOrCreateActive("test:group:root:five") != reset {
		t.Fatal("reset was confined to one display thread")
	}
	if _, err := sm.SwitchSession("test:group:root:five", got.ID); err != nil {
		t.Fatal(err)
	}
	if sm.GetOrCreateActive("test:group:root:six") != got {
		t.Fatal("switch did not apply to member conversation")
	}
}

func TestTeamSessionSeparatesProjectsMembersAndRouting(t *testing.T) {
	m, d, _, _, _, store := coordinatorFixture(t)
	p, _ := m.teamProject("test:group:owner")
	one := m.modelSessionKey("test:group:root:one")
	if one != m.modelSessionKey("test:group:root:two") || one == d.modelSessionKey("test:group:root:one") {
		t.Fatal("member scope wrong")
	}
	other := *p
	other.ID = "project-other"
	other.ChatID = "another"
	if err := store.Put(other); err != nil {
		t.Fatal(err)
	}
	if one == m.modelSessionKey("test:another:root:one") {
		t.Fatal("shared directory leaked across projects")
	}
	// A queued newer instruction must not become an executing turn's origin.
	m.interactiveMu.Lock()
	m.interactiveStates["current"] = &interactiveState{sourceSessionKey: "test:group:root:first", pendingMessages: []queuedMessage{{msgSessionKey: "test:group:root:second"}}}
	m.interactiveMu.Unlock()
	if got := m.teamActionSource(one); got != "test:group:root:first" {
		t.Fatalf("wrong dispatch origin: %s", got)
	}
	if m.modelSessionKey("test:unrelated:owner") != "test:unrelated:owner" {
		t.Fatal("ordinary sessions changed")
	}
}

func TestPlannedTasksStayPrivateAndRequireExplicitReadyDispatch(t *testing.T) {
	m, _, mp, dp, _, s := coordinatorFixture(t)
	a, err := s.assignTask("project", TeamActionRequest{To: "dev", Message: "A"})
	if err != nil {
		t.Fatal(err)
	}
	req := TeamActionRequest{SessionKey: "test:group:owner", Action: "plan", To: "dev", Title: "B", Message: "B inputs", Deliverable: "B artifact", DependsOn: []string{a.ID}}
	if _, err = m.RunTeamAction(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	all, _ := s.All()
	b := all["project"].Tasks["T002"]
	if b.Status != "planned" || b.RootMessageID != "" || len(mp.getSent()) != 0 || len(dp.snapshots()) != 0 {
		t.Fatal("planning assigned visible work")
	}
	if _, err = m.RunTeamAction(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	all, _ = s.All()
	if len(all["project"].Tasks) != 2 {
		t.Fatal("duplicate plan")
	}
	if _, err = s.assignTask("project", TeamActionRequest{To: "dev", TaskID: b.ID}); err == nil {
		t.Fatal("busy member released B")
	}
	s.saveReport("project", TeamMemberReport{TaskID: a.ID, Attempt: a.Attempt, ID: a.ID, Bot: "dev", Status: "completed", Content: "A output"})
	if _, err = s.assignTask("project", TeamActionRequest{To: "dev", TaskID: b.ID}); err == nil {
		t.Fatal("unaccepted prerequisite released B")
	}
	if err = s.acceptTask("project", a.ID, "inspected A output"); err != nil {
		t.Fatal(err)
	}
	// Persistence alone never executes future work, even after acceptance/restart.
	reloaded := NewTeamProjectStore(s.path)
	all, _ = reloaded.All()
	if all["project"].Tasks[b.ID].Status != "planned" {
		t.Fatal("acceptance auto-dispatched")
	}
	task, err := s.assignTask("project", TeamActionRequest{To: "dev", TaskID: b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if task.Attempt != 1 || task.Instruction != "B inputs" || task.DependencyAttempts[a.ID] != 1 {
		t.Fatalf("bad planned release: %+v", task)
	}
	// Reset reports to queued to avoid fixture expecting synthetic notifications.
}

func TestPlanEditCycleCancellationAndPause(t *testing.T) {
	s := NewTeamProjectStore(t.TempDir() + "/p.json")
	s.Put(TeamProject{ID: "project", State: "ready", Manager: "manager", Bots: []string{"manager", "dev"}, Directories: []string{t.TempDir()}})
	req := TeamActionRequest{To: "dev", Title: "A", Message: "own work", Deliverable: "artifact"}
	a, err := s.planTask("project", req)
	if err != nil {
		t.Fatal(err)
	}
	req.Title = "B"
	req.DependsOn = []string{a.ID}
	b, err := s.planTask("project", req)
	if err != nil {
		t.Fatal(err)
	}
	req.TaskID = a.ID
	req.DependsOn = []string{b.ID}
	if _, err = s.planTask("project", req); err == nil {
		t.Fatal("cycle accepted")
	}
	if err = s.cancelPlannedTask("project", b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.assignTask("project", TeamActionRequest{TaskID: b.ID, To: "dev"}); err == nil {
		t.Fatal("cancelled plan executed")
	}
	s.setWorkflowPaused("project", true)
	if _, err = s.assignTask("project", TeamActionRequest{TaskID: a.ID, To: "dev"}); err == nil {
		t.Fatal("paused plan executed")
	}
	all, _ := s.All()
	if all["project"].Tasks[a.ID].Status != "planned" {
		t.Fatal("pause lost future work")
	}
	s.setWorkflowPaused("project", false)
	all, _ = s.All()
	if all["project"].Tasks[a.ID].Status != "planned" {
		t.Fatal("resume auto-started future work")
	}
}

func TestIdenticalPlansFromSeparateRequestsStaySeparate(t *testing.T) {
	s := NewTeamProjectStore(t.TempDir() + "/p.json")
	if err := s.Put(TeamProject{ID: "project", State: "ready", Manager: "manager", Bots: []string{"manager", "dev"}, Directories: []string{t.TempDir()}}); err != nil {
		t.Fatal(err)
	}
	req := TeamActionRequest{SessionKey: "test:group:root:first", To: "dev", Title: "Review", Message: "Review changes", Deliverable: "Findings"}
	a, err := s.planTask("project", req)
	if err != nil {
		t.Fatal(err)
	}
	req.SessionKey = "test:group:root:second"
	b, err := s.planTask("project", req)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID || b.ManagerSessionKey != req.SessionKey {
		t.Fatal("separate request was folded into an older task")
	}
}

func TestSharedSessionDoesNotAnswerAnotherThreadsQuestion(t *testing.T) {
	m, _, mp, _, _, _ := coordinatorFixture(t)
	pending := &pendingPermission{RequesterID: "owner", ChatID: "group", SourceSessionKey: "test:group:root:current", RequestID: "q", Questions: []UserQuestion{{Question: "Which?"}}, Resolved: make(chan struct{})}
	state := &interactiveState{pending: pending}
	key := m.interactiveKeyForSessionKey("test:group:root:current")
	m.interactiveMu.Lock()
	m.interactiveStates[key] = state
	m.interactiveMu.Unlock()
	if m.handlePendingPermission(mp, &Message{SessionKey: "test:group:root:old", UserID: "owner", Content: "A"}, "A", key) {
		t.Fatal("old thread consumed as answer")
	}
	if len(pending.Answers) > 0 {
		t.Fatal("cross-thread answer applied")
	}
	if !m.handlePendingPermission(mp, &Message{SessionKey: "test:group:root:old", UserID: "owner", Content: "A", IsPermissionResponse: true}, "A", key) {
		t.Fatal("stale callback became new input")
	}
	// Detach to keep cleanup from treating this synthetic decision as live.
	state.mu.Lock()
	state.pending = nil
	state.mu.Unlock()
}

func TestTeamModelContextSurvivesIdleResetPolicy(t *testing.T) {
	m, _, mp, _, _, _ := coordinatorFixture(t)
	m.resetOnIdle = time.Nanosecond
	_, sessions, _, _, err := m.commandContextWithWorkspace(mp, &Message{SessionKey: "test:group:root:one"})
	if err != nil {
		t.Fatal(err)
	}
	session := sessions.GetOrCreateActive("test:group:root:one")
	session.SetAgentSessionID("saved", "test")
	gen, ok := session.TryLock()
	if !ok {
		t.Fatal("lock")
	}
	defer session.Unlock(gen)
	rotated, _ := m.maybeAutoResetSessionOnIdle(mp, &Message{SessionKey: "test:group:root:two"}, sessions, "key", session, gen)
	if rotated != nil {
		t.Fatal("idle gap reset project member context")
	}
	if !strings.Contains(m.modelSessionKey("test:group:root:one"), "member:project:manager") {
		t.Fatal("scope")
	}
}
