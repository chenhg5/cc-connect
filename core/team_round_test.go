package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRoundHistoryReworkAndOldInputsRemainIsolated(t *testing.T) {
	m, d, mp, _, _, s := coordinatorFixture(t)
	p, _ := m.teamProject("test:group")
	task, err := s.assignTask(p.ID, TeamActionRequest{To: "dev", Message: "old instruction"})
	if err != nil {
		t.Fatal(err)
	}
	s.updateTask(p.ID, task.ID, func(t *TeamTask) error {
		t.Status = "submitted"
		t.Result = "old result"
		t.SessionKey = "test:group:root:old-task"
		return nil
	})
	s.mutateProject(p.ID, func(p *TeamProject) error {
		p.ManagerContexts = map[string]TeamManagerContext{"test:group:root:old-manager": {Instruction: "old"}}
		return nil
	})
	p, _ = m.teamProject("test:group")
	ending, err := s.finishProject(p.ID, "owner", projectFinishFingerprint(*p))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.completeProjectRound(p.ID, projectRound(ending)); err != nil {
		t.Fatal(err)
	}
	p, _ = m.teamProject("test:group")
	for _, msg := range []*Message{
		{SessionKey: "test:group", Content: "late report", teamInternal: true},
		{SessionKey: "test:group", Content: "late user input", UserID: "owner", UserMessageTimeMs: 1},
	} {
		if !m.prepareProjectRound(mp, msg, p) {
			t.Fatal("stale input opened round")
		}
	}
	if m.prepareProjectRound(mp, &Message{SessionKey: "test:group", Content: "/project dashboard", UserID: "owner"}, p) {
		t.Fatal("read-only command rejected")
	}
	// A platform with explicit group keys must not accept an old thread as a new round.
	native := &threadCUJPlatform{coordinationPlatform: mp, role: "manager"}
	if !m.prepareProjectRound(native, &Message{SessionKey: "test:group:root:old-manager", Content: "old thread reply", UserID: "owner"}, p) {
		t.Fatal("old thread started a new round")
	}
	if m.prepareProjectRound(native, &Message{SessionKey: "test:group", Content: "New requirements", UserID: "owner"}, p) {
		t.Fatal("fresh group instruction rejected")
	}
	if p.Round != 2 || p.State != "ready" {
		t.Fatal("round not advanced")
	}
	if !m.prepareProjectRound(native, &Message{SessionKey: "test:group", Content: "late human input", UserID: "owner", UserMessageTimeMs: 1}, p) {
		t.Fatal("late old input entered active new round")
	}
	if _, err = m.RunTeamAction(context.Background(), TeamActionRequest{SessionKey: "test:group:root:old-manager", Action: "pause"}); err == nil {
		t.Fatal("old manager paused new round")
	}
	if _, err = d.registerTaskFollowup(&Message{SessionKey: "test:group:root:old-task", MessageID: "late", Content: "late", UserID: "owner"}); err == nil {
		t.Fatal("old member thread admitted new input")
	}
	if _, err = s.assignTask(p.ID, TeamActionRequest{TaskID: task.ID, To: "dev", Message: "explicit new rework"}); err != nil {
		t.Fatal(err)
	}
	all, err := NewTeamProjectStore(s.path).All()
	if err != nil {
		t.Fatal(err)
	}
	now := all[p.ID]
	if now.Tasks[task.ID].Round != 2 || now.Tasks[task.ID].Attempt != 2 || now.Rounds[0].Tasks[task.ID].Result != "old result" || now.Rounds[0].Tasks[task.ID].Instruction != "old instruction" {
		t.Fatal("rework rewrote round history")
	}
	// Old reports cannot replace the new attempt, including across restart.
	s.saveReport(p.ID, TeamMemberReport{TaskID: task.ID, Attempt: 1, Epoch: 0, Bot: "dev", Status: "completed", Content: "late"})
	all, _ = s.All()
	if all[p.ID].Tasks[task.ID].Result != "" {
		t.Fatal("late report overwrote current attempt")
	}
}

type roundCloseSession struct {
	*cujAgentSession
	release chan struct{}
	fail    atomic.Bool
	calls   atomic.Int32
}

func (s *roundCloseSession) Close() error {
	s.calls.Add(1)
	if s.release != nil {
		<-s.release
	}
	if s.fail.Load() {
		return errors.New("still running")
	}
	return s.cujAgentSession.Close()
}
func TestEndRoundWaitsForExitAndRetriesCloseFailure(t *testing.T) {
	m, d, mp, _, _, store := coordinatorFixture(t)
	p, _ := m.teamProject("test:group")
	slow := &roundCloseSession{cujAgentSession: newCUJAgentSession(), release: make(chan struct{})}
	slow.fail.Store(true)
	key := d.modelSessionKey("test:group:root:slow")
	d.interactiveMu.Lock()
	d.interactiveStates[key] = &interactiveState{agentSession: slow, sourceSessionKey: "test:group:root:slow"}
	d.interactiveMu.Unlock()
	msg := &Message{UserID: "owner", SessionKey: "test:group", ReplyCtx: "test:group"}
	if err := m.handleProjectFinish(mp, msg, *p, []string{"finish", "confirm", projectFinishFingerprint(*p)}); err != nil {
		t.Fatal(err)
	}
	awaitQuery(t, func() bool { return slow.calls.Load() == 1 })
	all, _ := store.All()
	ending := all[p.ID]
	if ending.RoundState != "ending" || !m.prepareProjectRound(mp, &Message{UserID: "owner", SessionKey: "test:group", Content: "too early"}, &ending) {
		t.Fatal("new work passed before process exit")
	}
	// A persisted ending state remains fenced on restart.
	reloaded, _ := NewTeamProjectStore(store.path).All()
	if roundAcceptsWork(reloaded[p.ID]) {
		t.Fatal("restart opened unfinished ending")
	}
	close(slow.release)
	awaitQuery(t, func() bool { store.runtimeMu.Lock(); defer store.runtimeMu.Unlock(); return !store.roundStopping[p.ID] })
	all, _ = store.All()
	if all[p.ID].RoundState != "ending" {
		t.Fatal("failed stop reported success")
	}
	mp.stubCardPlatform.mu.Lock()
	text := mp.sentCards[len(mp.sentCards)-1].RenderText()
	mp.stubCardPlatform.mu.Unlock()
	if !strings.Contains(text, m.i18n.T(MsgRoundRetry)) || !strings.Contains(text, "dev") {
		t.Fatal("stop failure has no visible member/retry", text)
	}
	slow.fail.Store(false)
	p, _ = m.teamProject("test:group")
	if err := m.handleProjectFinish(mp, msg, *p, []string{"finish", "confirm", projectFinishFingerprint(*p)}); err != nil {
		t.Fatal(err)
	}
	awaitQuery(t, func() bool { all, _ := store.All(); return all[p.ID].RoundState == "idle" })
	if slow.Alive() || slow.calls.Load() != 2 {
		t.Fatal("retry did not confirm process exit")
	}
}

type dashboardErrorPlatform struct {
	*coordinationPlatform
	fail bool
}

func (p *dashboardErrorPlatform) SendCard(ctx context.Context, rc any, c *Card) error {
	if p.fail {
		return errors.New("temporarily unavailable")
	}
	return p.coordinationPlatform.SendCard(ctx, rc, c)
}
func TestDashboardErrorsAreVisibleAndCanRetry(t *testing.T) {
	m, _, mp, _, _, store := coordinatorFixture(t)
	m.SetAdminFrom("owner")
	p := &dashboardErrorPlatform{coordinationPlatform: mp, fail: true}
	msg := &Message{Platform: "test", SessionKey: "test:group", UserID: "owner", Content: "/project dashboard", ReplyCtx: "here"}
	m.ReceiveMessage(p, msg)
	if !strings.Contains(strings.Join(mp.getSent(), "\n"), "/project dashboard") {
		t.Fatal("card failure has no visible retry")
	}
	p.fail = false
	m.ReceiveMessage(p, msg)
	mp.stubCardPlatform.mu.Lock()
	n := len(mp.sentCards)
	mp.stubCardPlatform.mu.Unlock()
	if n != 1 {
		t.Fatal("retry did not produce card")
	}
	original, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(store.path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	m.ReceiveMessage(p, msg)
	if err = os.WriteFile(store.path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(mp.getSent(), "\n"), "Could not refresh") {
		t.Fatal("read failure hidden", mp.getSent())
	}
}
func TestDashboardPaginationAndSnapshotTaskLinks(t *testing.T) {
	m, _, _, _, _, _ := coordinatorFixture(t)
	p, _ := m.teamProject("test:group")
	p.Tasks = map[string]TeamTask{}
	for i := 1; i <= 14; i++ {
		id := fmt.Sprintf("T%03d", i)
		p.Tasks[id] = TeamTask{ID: id, Title: "Task", Status: "waiting_input"}
	}
	for page := 0; page < 3; page++ {
		c := m.dashboardCard(*p, page)
		rows := 0
		for _, el := range c.Elements {
			if _, ok := el.(CardListItem); ok {
				rows++
			}
		}
		if rows > dashboardPageSize*2 {
			t.Fatal("unbounded task cards")
		}
	}
	p.Rounds = []TeamRound{{Number: 1, Tasks: p.Tasks, EndedAt: time.Now().Format(time.RFC3339)}}
	c := m.roundHistoryCard(*p, []string{"history", "round", "1"})
	for _, el := range c.Elements {
		if row, ok := el.(CardListItem); ok && !strings.HasSuffix(row.BtnValue, "round 1") {
			t.Fatal("history resolves to mutable current task")
		}
	}
}

func TestDashboardFullTaskDetailsAreNeverTruncated(t *testing.T) {
	m, _, mp, _, _, _ := coordinatorFixture(t)
	p, _ := m.teamProject("test:group")
	body := strings.Repeat("command arg ", 1600) + "TAIL-OF-COMMAND"
	msg := &Message{ReplyCtx: "here"}
	if err := m.sendProjectTaskDetail(mp, msg, *p, TeamTask{ID: "T001", Instruction: body, Result: "ORIGINAL-RESULT", Evidence: "VERIFIED"}); err != nil {
		t.Fatal(err)
	}
	visible := strings.Join(mp.getSent(), "\n")
	if !strings.Contains(visible, "TAIL-OF-COMMAND") || !strings.Contains(visible, "ORIGINAL-RESULT") || !strings.Contains(visible, "VERIFIED") {
		t.Fatal("large detail was truncated")
	}
}
func TestProjectFinishCannotIgnorePreviousUnsafeClose(t *testing.T) {
	m, d, _, _, _, _ := coordinatorFixture(t)
	p, _ := m.teamProject("test:group")
	key := d.modelSessionKey("test:group")
	d.markUnsafeResume(key)
	targets := m.captureProjectCloses(*p)
	if len(targets) != 1 || m.waitProjectCloses(context.Background(), *p, targets) == nil {
		t.Fatal("unsafe previous close was ignored")
	}
}
