package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestProgress_QueueHeartbeatDoesNotPretendWorkAdvanced(t *testing.T) {
	start := time.Now().Add(-6 * time.Minute)
	p := newExecutionTracker(start)
	p.observe(Event{Type: EventProgress, Progress: &RuntimeProgress{Phase: "waiting_model", QueuePosition: 482}}, start)
	p.observe(Event{Type: EventProgress, Progress: &RuntimeProgress{Phase: "waiting_model", QueuePosition: 452}}, time.Now())
	v := p.snapshot()
	text := v.text(NewI18n(LangEnglish), time.Now())
	if !v.LastAdvance.Equal(start) || !v.PhaseSince.Equal(start) || !strings.Contains(text, "452") || !strings.Contains(text, "5 minutes") {
		t.Fatal(text, v)
	}
	p.observe(Event{Type: EventThinking, Content: "PRIVATE_REASONING"}, time.Now())
	if v = p.snapshot(); v.Phase == "waiting_model" || strings.Contains(v.text(NewI18n(LangEnglish), time.Now()), "PRIVATE") {
		t.Fatal(v)
	}
}
func TestProgress_ConcurrentToolsPartialResultsPlanAndApproval(t *testing.T) {
	now := time.Now()
	p := newExecutionTracker(now)
	p.observe(Event{Type: EventPlan, Plan: []ProgressStep{{"Inspect code", "completed"}, {"Fix login", "in_progress"}, {"Regression tests", "pending"}}}, now)
	p.observe(Event{Type: EventToolUse, ToolID: "one", ToolName: "Bash", ToolInput: "go test ./... SECRET"}, now)
	p.observe(Event{Type: EventToolUse, ToolID: "two", ToolName: "Bash", ToolInput: "go build ./..."}, now)
	p.observe(Event{Type: EventToolResult, ToolID: "one", ToolStatus: "in_progress", Content: "output"}, now)
	if len(p.snapshot().Recent) != 0 {
		t.Fatal("partial output counted as complete")
	}
	p.observe(Event{Type: EventToolResult, ToolID: "one", ToolStatus: "completed"}, now)
	if p.snapshot().Phase != "tool" {
		t.Fatal("parallel tool lost")
	}
	p.observe(Event{Type: EventPermissionRequest, ToolName: "Bash"}, now)
	if text := p.snapshot().text(NewI18n(LangEnglish), now.Add(time.Hour)); strings.Contains(text, "2 minutes") {
		t.Fatal(text)
	}
	p.resume(now)
	if p.snapshot().Phase != "tool" {
		t.Fatal("approval lost active tool")
	}
	p.observe(Event{Type: EventResult, Done: false}, now)
	if p.snapshot().Phase == "submitted" {
		t.Fatal("nonterminal result completed task")
	}
	text := p.snapshot().text(NewI18n(LangEnglish), now)
	for _, want := range []string{"1/3", "Fix login", "Regression tests", "Running test command"} {
		if !strings.Contains(text, want) {
			t.Fatal(want, text)
		}
	}
	if strings.Contains(text, "SECRET") {
		t.Fatal(text)
	}
	p.observe(Event{Type: EventResult, Done: true}, now)
	if p.snapshot().Phase != "submitted" {
		t.Fatal("missing completion")
	}
}
func TestProgress_SilenceWarningOnOriginalCardAndStop(t *testing.T) {
	raw := &queryTestCard{}
	c := newWorkflowCard(raw, NewI18n(LangEnglish), "compact", "T007")
	c.progress = newExecutionTracker(time.Now().Add(-3 * time.Minute))
	if err := c.Update(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	text, _, _ := raw.snapshot()
	if !strings.Contains(text, "2 minutes") || !strings.Contains(text, "/stop") {
		t.Fatal(text)
	}
	c.Finish(context.Background(), "", CardStatusDone)
	text, _, _ = raw.snapshot()
	if !strings.Contains(text, "Stopped") {
		t.Fatal(text)
	}
}
func TestProgress_DurableSnapshotCannotReviveStoppedOrNewAttempt(t *testing.T) {
	store := NewTeamProjectStore(t.TempDir() + "/projects.json")
	project := TeamProject{ID: "p", Directories: []string{t.TempDir()}, State: "ready", WorkflowEpoch: 1, Tasks: map[string]TeamTask{"T001": {ID: "T001", Status: "running", Attempt: 2, Epoch: 1}}}
	if err := store.Put(project); err != nil {
		t.Fatal(err)
	}
	e := NewEngine("dev", &stubAgent{}, nil, t.TempDir()+"/sessions.json", LangEnglish)
	defer e.Stop()
	e.teamProjects = store
	old := project.Tasks["T001"]
	old.Attempt = 1
	e.saveExecutionProgress(&project, &old, newExecutionTracker(time.Now()).snapshot())
	all, _ := store.All()
	if all["p"].Tasks["T001"].Progress != nil {
		t.Fatal("stale attempt wrote progress")
	}
	current := project.Tasks["T001"]
	e.saveExecutionProgress(&project, &current, newExecutionTracker(time.Now()).snapshot())
	all, _ = store.All()
	if all["p"].Tasks["T001"].Progress == nil {
		t.Fatal("progress missing")
	}
	store.setWorkflowPaused("p", true)
	e.saveExecutionProgress(&project, &current, ExecutionProgress{Phase: "waiting_model"})
	all, _ = store.All()
	if all["p"].Tasks["T001"].Progress.Phase == "waiting_model" {
		t.Fatal("late event revived progress")
	}
}

type progressSummaryTestCard struct {
	queryTestCard
	summary string
}

func (c *progressSummaryTestCard) SetProgressSummary(s string) { c.summary = s }
func TestProgress_FoldSummaryShowsCurrentWorkAndDetailsKeepWholePlan(t *testing.T) {
	raw := &progressSummaryTestCard{}
	c := newWorkflowCard(raw, NewI18n(LangEnglish), "compact", "T007 · Login")
	c.owner = "Owner: Jordan\nExpected deliverable: Patch and tests"
	c.observe(Event{Type: EventPlan, Plan: []ProgressStep{{"Inspect", "completed"}, {"Implement", "in_progress"}, {"Tests", "pending"}, {"Review", "pending"}}})
	c.observe(Event{Type: EventThinking, Content: "PRIVATE_REASONING"})
	if err := c.Update(context.Background(), "PRIVATE_RAW_TRACE"); err != nil {
		t.Fatal(err)
	}
	detail, _, _ := raw.snapshot()
	for _, s := range []string{"Inspect", "Implement", "Tests", "Review", "Patch and tests"} {
		if !strings.Contains(detail, s) {
			t.Fatal("detail missing", s)
		}
	}
	for _, s := range []string{"Jordan", "Implement", "1/4"} {
		if !strings.Contains(raw.summary, s) {
			t.Fatal("summary missing", s)
		}
	}
	if strings.Contains(raw.summary, "Patch and tests") || strings.Contains(raw.summary+detail, "PRIVATE") {
		t.Fatal("summary leaked detail or internal content")
	}
	c.progress = newExecutionTracker(time.Now().Add(-6 * time.Minute))
	c.progress.observe(Event{Type: EventProgress, Progress: &RuntimeProgress{Phase: "waiting_model", QueuePosition: 409}}, time.Now().Add(-6*time.Minute))
	c.Update(context.Background(), "")
	if !strings.Contains(raw.summary, "5 minutes") || !strings.Contains(raw.summary, "409") {
		t.Fatal("wait warning hidden")
	}
	c.Finish(context.Background(), "", CardStatusDone)
	if !strings.Contains(raw.summary, "Stopped") || strings.Contains(raw.summary, "5 minutes") {
		t.Fatal("terminal summary stale")
	}
}
