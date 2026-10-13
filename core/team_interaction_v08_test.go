package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestWorkflow_ReworkArchivesThreadAndInvalidatesDependentInputs(t *testing.T) {
	s := NewTeamProjectStore(t.TempDir() + "/projects.json")
	if err := s.Put(TeamProject{ID: "p", State: "ready", Platform: "test", ChatID: "group", Manager: "manager", Bots: []string{"manager", "dev", "test", "review"}, Directories: []string{t.TempDir()}}); err != nil {
		t.Fatal(err)
	}
	assign := func(to string, deps ...string) TeamTask {
		task, err := s.assignTask("p", TeamActionRequest{To: to, Message: "own work", DependsOn: deps})
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
	finish := func(task TeamTask) {
		if err := s.saveReport("p", TeamMemberReport{Bot: task.Assignee, ID: task.ID + "-result", TaskID: task.ID, Attempt: task.Attempt, Epoch: task.Epoch, Status: "completed", Content: "native result"}); err != nil {
			t.Fatal(err)
		}
	}
	first := assign("dev")
	s.updateTask("p", first.ID, func(tk *TeamTask) error {
		tk.RootMessageID = "root"
		tk.SessionKey = "test:group:root:root"
		return nil
	})
	finish(first)
	if err := s.acceptTask("p", first.ID, "checked v1"); err != nil {
		t.Fatal(err)
	}
	second := assign("test", first.ID)
	finish(second)
	if err := s.acceptTask("p", second.ID, "checked tests"); err != nil {
		t.Fatal(err)
	}
	retry, err := s.assignTask("p", TeamActionRequest{To: "dev", TaskID: first.ID, Message: "correct edge case"})
	if err != nil {
		t.Fatal(err)
	}
	if retry.RootMessageID != "" || retry.SessionKey != "" || retry.History[0].RootMessageID != "root" || retry.History[0].SessionKey != "test:group:root:root" || retry.Attempt != 2 || len(retry.History) != 1 || retry.History[0].Result != "native result" {
		t.Fatalf("lost rework origin/history: %+v", retry)
	}
	finish(first) // late v1 report cannot complete v2
	all, _ := s.All()
	if all["p"].Tasks[first.ID].Status != "queued" {
		t.Fatal("stale result completed rework")
	}
	finish(retry)
	if err = s.acceptTask("p", retry.ID, "checked v2"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.assignTask("p", TeamActionRequest{To: "review", Message: "review", DependsOn: []string{second.ID}}); err == nil {
		t.Fatal("stale downstream acceptance released more work")
	}
}

func TestReworkFollowupDeduplicatesAndWaitsForItsTurn(t *testing.T) {
	p := &coordinationPlatform{queryTestPlatform: queryTestPlatform{stubCardPlatform: stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}}
	e := NewEngine("dev", &stubAgent{}, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
	defer e.Stop()
	s := NewTeamProjectStore(t.TempDir() + "/projects.json")
	e.SetTeamProjects(s, nil)
	s.Put(TeamProject{ID: "p", State: "ready", Platform: "test", ChatID: "group", Manager: "manager", Bots: []string{"manager", "dev"}, Directories: []string{t.TempDir()}, Tasks: map[string]TeamTask{"T001": {ID: "T001", Assignee: "dev", Attempt: 1, Status: "running", SessionKey: "test:group:root:task", RootMessageID: "task"}}})
	msg := &Message{MessageID: "followup", SessionKey: "test:group:root:task", Content: "fix this", UserID: "owner"}
	if dup, err := e.registerTaskFollowup(msg); err != nil || dup {
		t.Fatalf("register: %v %v", dup, err)
	}
	if dup, err := e.registerTaskFollowup(msg); err != nil || !dup {
		t.Fatalf("duplicate: %v %v", dup, err)
	}
	all, _ := s.All()
	task := all["p"].Tasks["T001"]
	if task.Attempt != 1 || len(task.Followups) != 1 {
		t.Fatal("followup started in parallel")
	}
	s.updateTask("p", "T001", func(tk *TeamTask) error { tk.Status = "submitted"; tk.Result = "v1"; return nil })
	if err := s.acceptTask("p", "T001", "checked"); err == nil {
		t.Fatal("accepted while rework was queued")
	}
	if err := e.beginTaskFollowup(msg.SessionKey, msg.MessageID); err != nil {
		t.Fatal(err)
	}
	all, _ = s.All()
	task = all["p"].Tasks["T001"]
	if task.Attempt != 2 || task.Instruction != "fix this" || task.History[0].Result != "v1" || len(task.Followups) != 0 {
		t.Fatalf("bad new attempt: %+v", task)
	}
}

type executionTestSink struct {
	queryTestCard
	blocks []ExecutionBlock
	answer string
}

func (c *executionTestSink) UpdateExecution(_ context.Context, b []ExecutionBlock) error {
	c.blocks = append([]ExecutionBlock(nil), b...)
	return nil
}
func (c *executionTestSink) FinishExecution(_ context.Context, b []ExecutionBlock, a string, _ CardStatus) error {
	c.blocks = append([]ExecutionBlock(nil), b...)
	c.answer = a
	return nil
}
func TestNativeExecutionPreservesCommandsAndConcurrentToolIdentity(t *testing.T) {
	sink := &executionTestSink{}
	c := &nativeWorkflowCard{inner: sink, sink: sink}
	ctx := context.Background()
	command := "python <<'PY'\n" + strings.Repeat("print('完整内容')\n", 200) + "PY"
	events := []Event{{Type: EventText, Content: "Native progress"}, {Type: EventThinking, Content: "PRIVATE"}, {Type: EventToolUse, ToolName: "Bash", ToolID: "a", ToolInput: command}, {Type: EventToolUse, ToolName: "Bash", ToolID: "b", ToolInput: "pwd"}, {Type: EventToolResult, ToolID: "b", ToolName: "Bash", ToolResult: "/tmp", ToolStatus: "completed"}, {Type: EventToolResult, ToolID: "a", ToolName: "Bash", ToolResult: "partial", ToolStatus: "in_progress"}, {Type: EventToolResult, ToolID: "a", ToolName: "Bash", ToolResult: "complete", ToolStatus: "completed"}, {Type: EventText, Content: "Final streamed text"}}
	for _, event := range events {
		if err := c.ObserveExecution(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.FinalizeWithAnswer(ctx, "ignored", "Original final result"); err != nil {
		t.Fatal(err)
	}
	if len(sink.blocks) != 3 || sink.blocks[0].Text != "Native progress" || sink.blocks[1].Command != command || sink.blocks[1].Output != "complete" || sink.blocks[2].Output != "/tmp" || sink.answer != "Original final result" {
		t.Fatalf("native output changed: %+v", sink)
	}
}

type decisionTestSession struct {
	controllableAgentSession
	calls  int
	fail   bool
	result PermissionResult
}

func (s *decisionTestSession) RespondPermission(_ string, r PermissionResult) error {
	s.calls++
	if s.fail {
		return errors.New("transport unavailable")
	}
	s.result = r
	return nil
}
func TestDecisionOwnerScopeRetryAndLiteralAnswer(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
	defer e.Stop()
	a := &decisionTestSession{fail: true}
	pending := &pendingPermission{RequesterID: "owner", ChatID: "group", CardToken: "token", RequestID: "request", Questions: []UserQuestion{{Question: "Question", Options: []UserQuestionOption{{Label: "option one"}}}}, Resolved: make(chan struct{}), ExpiresAt: time.Now().Add(time.Minute)}
	state := &interactiveState{agentSession: a, pending: pending}
	e.interactiveStates["test:group:root:task"] = state
	msg := &Message{SessionKey: "test:group:root:task", ChannelID: "group", UserID: "outsider", IsPermissionResponse: true, PermissionToken: "token", LiteralAnswer: true}
	e.handlePendingPermission(p, msg, " 1 \n", msg.SessionKey)
	msg.UserID = "owner"
	msg.ChannelID = "other"
	e.handlePendingPermission(p, msg, " 1 \n", msg.SessionKey)
	msg.ChannelID = "group"
	msg.PermissionToken = "stale"
	e.handlePendingPermission(p, msg, " 1 \n", msg.SessionKey)
	if a.calls != 0 {
		t.Fatal("invalid decision reached backend")
	}
	msg.PermissionToken = "token"
	e.handlePendingPermission(p, msg, " 1 \n", msg.SessionKey)
	if state.pending != pending {
		t.Fatal("transport failure consumed request")
	}
	a.fail = false
	e.handlePendingPermission(p, msg, " 1 \n", msg.SessionKey)
	if state.pending != nil || a.result.UpdatedInput["answers"].(map[string]any)["Question"] != " 1 \n" {
		t.Fatalf("literal answer changed: %+v", a.result)
	}
	e.handlePendingPermission(p, msg, "duplicate", msg.SessionKey)
	if a.calls != 2 {
		t.Fatal("duplicate response reached backend")
	}
}
func TestDecisionExpiryDeniesAndDisablesControls(t *testing.T) {
	e := NewEngine("test", &stubAgent{}, nil, t.TempDir()+"/sessions.json", LangEnglish)
	defer e.Stop()
	card := &queryTestCard{prompt: NewCard().Buttons(PrimaryBtn("Allow", "perm:allow")).Build()}
	a := &decisionTestSession{}
	pending := &pendingPermission{Card: card, RequestID: "r", Resolved: make(chan struct{})}
	state := &interactiveState{agentSession: a, pending: pending}
	e.expirePendingDecision(state, pending)
	e.expirePendingDecision(state, pending)
	if a.calls != 1 || a.result.Behavior != "deny" || card.prompt != nil || !pending.Expired || state.pending != nil {
		t.Fatal("expiry left live controls or authorized work")
	}
}

func (s *decisionTestSession) Close() error { return nil }

type threadCUJPlatform struct {
	*coordinationPlatform
	role        string
	roots       []string
	reminders   []*queryTestCard
	assignments []cujTaskMessage
	taskError   error
	results     []cujHandoff
	resultError error
}

type cujHandoff struct{ id, to, content string }
type cujTaskMessage struct{ id, root, to, content string }

func (p *threadCUJPlatform) SendResultMessage(ctx context.Context, chat, to, text, key string) (string, error) {
	p.queryTestPlatform.mu.Lock()
	if p.resultError != nil {
		err := p.resultError
		p.queryTestPlatform.mu.Unlock()
		return "", err
	}
	id := fmt.Sprintf("result-%s-%d", p.role, len(p.results)+1)
	p.results = append(p.results, cujHandoff{id, to, text})
	p.queryTestPlatform.mu.Unlock()
	_ = p.Send(ctx, chat, "@"+to+" "+text)
	return id, nil
}

func (p *threadCUJPlatform) handoffs() []cujHandoff {
	p.queryTestPlatform.mu.Lock()
	defer p.queryTestPlatform.mu.Unlock()
	return append([]cujHandoff(nil), p.results...)
}

func (p *threadCUJPlatform) BotMentionID() string         { return p.role }
func (p *threadCUJPlatform) MentionText(id string) string { return "@" + id }
func (p *threadCUJPlatform) TaskConversationKey(chat, root string) string {
	return "test:" + chat + ":root:" + root
}
func (p *threadCUJPlatform) ProjectConversationKey(chat, user string) string { return "test:" + chat }
func (p *threadCUJPlatform) SetTaskThreadResolver(func(string, string) bool) {}
func (p *threadCUJPlatform) UpsertWorkflowCard(context.Context, string, string, string, *Card) (string, error) {
	return "board", nil
}
func (p *threadCUJPlatform) SendTaskMessage(ctx context.Context, chat, root, to, text, key string) (string, error) {
	p.queryTestPlatform.mu.Lock()
	if p.taskError != nil {
		err := p.taskError
		p.queryTestPlatform.mu.Unlock()
		return "", err
	}
	id := fmt.Sprintf("task-message-%d", len(p.assignments)+1)
	p.assignments = append(p.assignments, cujTaskMessage{id, root, to, text})
	p.queryTestPlatform.mu.Unlock()
	_ = p.Send(ctx, chat, "@"+to+" "+text)
	return id, nil
}

func (p *threadCUJPlatform) CreateStreamingCard(ctx context.Context, rc any) (StreamingCard, error) {
	p.queryTestPlatform.mu.Lock()
	p.roots = append(p.roots, rc.(string))
	p.queryTestPlatform.mu.Unlock()
	card, err := p.coordinationPlatform.CreateStreamingCard(ctx, rc)
	if err != nil {
		return nil, err
	}
	return &cujNativeCard{queryTestCard: card.(*queryTestCard)}, nil
}
func (p *threadCUJPlatform) CreateTaskStreamingCard(ctx context.Context, rc any, key string) (StreamingCard, error) {
	return p.CreateStreamingCard(ctx, rc)
}
func (p *threadCUJPlatform) CreateDecisionReminder(context.Context, string, string, string) (InteractiveStreamingCard, error) {
	card := &queryTestCard{}
	p.queryTestPlatform.mu.Lock()
	p.reminders = append(p.reminders, card)
	p.queryTestPlatform.mu.Unlock()
	return card, nil
}

type cujNativeCard struct{ *queryTestCard }

func (c *cujNativeCard) UpdateExecution(ctx context.Context, b []ExecutionBlock) error {
	return c.Update(ctx, cujNativeText(b))
}
func (c *cujNativeCard) FinishExecution(ctx context.Context, b []ExecutionBlock, answer string, status CardStatus) error {
	return c.Finish(ctx, cujNativeText(b)+"\n"+answer, status)
}
func cujNativeText(b []ExecutionBlock) string {
	var out strings.Builder
	for _, v := range b {
		out.WriteString(v.Text + v.Command + v.Output + "\n")
	}
	return out.String()
}
