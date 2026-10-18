package claudecode

import (
	"bufio"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// fixtureLines loads testdata/subagents_fg_bg.jsonl: claude 2.1.302
// stream-json (2026-10-18), one foreground and one background Agent call,
// each running two Bash commands, then the background agent's follow-up turn.
func fixtureLines(t *testing.T) []string {
	t.Helper()
	f, err := os.Open("testdata/subagents_fg_bg.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines
}

// newReplaySession has room for every event the fixture emits, so the replay
// never blocks although nobody reads the events channel.
func newReplaySession(t *testing.T) *claudeSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cs := &claudeSession{events: make(chan core.Event, 512), ctx: ctx}
	cs.alive.Store(true)
	return cs
}

func rowByID(t *testing.T, cs *claudeSession, id string) core.SubTask {
	t.Helper()
	_, tasks, _ := cs.SubTasks()
	for _, r := range tasks {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no row %s in %+v", id, tasks)
	return core.SubTask{}
}

const (
	fixtureFG = "a4ebbdcb451095854"
	fixtureBG = "ac01df9bf411cdba7"
)

func TestSubTasks_ReplayForegroundAndBackground(t *testing.T) {
	lines := fixtureLines(t)
	cs := newReplaySession(t)
	// Mid-run (after line 27: both agents are on their second Bash command).
	for _, l := range lines[:28] {
		cs.handleReadLoopLine(l)
	}
	gen, tasks, _ := cs.SubTasks()
	if gen != 1 || len(tasks) != 2 {
		t.Fatalf("gen=%d rows=%+v, want 2 rows (inner Bash tasks are not rows)", gen, tasks)
	}
	fg, bg := rowByID(t, cs, fixtureFG), rowByID(t, cs, fixtureBG)
	if fg.Description != "前台跑两个 sleep 命令" || fg.Background || fg.Step != "Running sleep 3; echo b" || fg.Status != core.SubTaskRunning {
		t.Fatalf("foreground row = %+v", fg)
	}
	if bg.Description != "后台跑两个 sleep 命令" || !bg.Background || bg.Step != "Running Sleep 3 seconds then print b" || bg.Status != core.SubTaskRunning {
		t.Fatalf("background row = %+v", bg)
	}

	for _, l := range lines[28:] {
		cs.handleReadLoopLine(l)
	}
	_, tasks, _ = cs.SubTasks()
	if len(tasks) != 2 {
		t.Fatalf("rows = %+v, want 2", tasks)
	}
	for _, id := range []string{fixtureFG, fixtureBG} {
		r := rowByID(t, cs, id)
		if r.Status != core.SubTaskCompleted || r.EndedAt.IsZero() {
			t.Fatalf("row %s = %+v, want completed", id, r)
		}
		if r.Summary != "两条命令已按先 a 后 b 的顺序依次运行,没有并行。" {
			t.Fatalf("row %s summary = %q, want the report's first sentence", id, r.Summary)
		}
	}
}

// Outside the turn: after the main turn's result, nobody reads the events
// channel, but a late task_progress still updates the row and signals the
// engine's table updater.
func TestSubTasks_ProgressAfterTurnEnd(t *testing.T) {
	lines := fixtureLines(t)
	cs := newReplaySession(t)
	for _, l := range lines[:14] { // both agents started; background tool_result returned
		cs.handleReadLoopLine(l)
	}
	changed := cs.SubTasksChanged()
	cs.handleReadLoopLine(`{"type":"result","subtype":"success","stop_reason":"end_turn","session_id":"a80946d4-095e-428c-a725-f58a1f327b8f","result":"x"}`)
	select { // consume the coalesced signal from the starts
	case <-changed:
	default:
	}
	cs.handleReadLoopLine(lines[27]) // background agent: "Running Sleep 3 seconds then print b"
	select {
	case <-changed:
	default:
		t.Fatal("late task_progress did not signal a change")
	}
	if r := rowByID(t, cs, fixtureBG); r.Step != "Running Sleep 3 seconds then print b" || r.Status != core.SubTaskRunning {
		t.Fatalf("background row = %+v", r)
	}
	cs.handleReadLoopLine(lines[40]) // its task_notification
	if r := rowByID(t, cs, fixtureBG); r.Status != core.SubTaskCompleted || r.Summary == "" {
		t.Fatalf("background row after notification = %+v", r)
	}
}

// A new batch after every row finished starts a new generation (new message);
// a process exit marks rows still running as stopped and closes the channel.
func TestSubTasks_NewBatchAndClose(t *testing.T) {
	cs := newReplaySession(t)
	cs.handleReadLoopLine(`{"type":"system","subtype":"task_started","task_id":"x1","task_type":"local_agent","description":"one","is_backgrounded":true}`)
	cs.handleReadLoopLine(`{"type":"system","subtype":"task_notification","task_id":"x1","status":"failed","summary":"boom. more"}`)
	if r := rowByID(t, cs, "x1"); r.Status != core.SubTaskFailed || r.Summary != "boom." {
		t.Fatalf("row = %+v", r)
	}
	cs.handleReadLoopLine(`{"type":"system","subtype":"task_started","task_id":"x2","task_type":"local_agent","description":"two"}`)
	gen, tasks, _ := cs.SubTasks()
	if gen != 2 || len(tasks) != 1 || tasks[0].ID != "x2" {
		t.Fatalf("gen=%d rows=%+v, want a fresh table with x2", gen, tasks)
	}
	ch := cs.SubTasksChanged()
	cs.closeSubTasks()
	if r := rowByID(t, cs, "x2"); r.Status != core.SubTaskStopped {
		t.Fatalf("row after close = %+v", r)
	}
	for range ch { // drains the last signal, then sees the close
	}
}

func TestFirstSentence(t *testing.T) {
	for in, want := range map[string]string{
		"做完了。细节如下":         "做完了。",
		"Done. All green.": "Done.",
		"v1.2 shipped":     "v1.2 shipped",
		"第一行\n第二行":         "第一行",
	} {
		if got := firstSentence(in); got != want {
			t.Errorf("firstSentence(%q) = %q, want %q", in, got, want)
		}
	}
}

// Review fix 3: the sub-agents' own assistant/user messages (parent_tool_use_id
// set) are not the main agent's events: replaying the fixture, the main agent
// keeps only its two Agent calls, their two tool results and its own text, and
// the context stats stay the main agent's.
func TestSubAgentMessagesAreNotMainEvents(t *testing.T) {
	lines := fixtureLines(t)
	cs := newReplaySession(t)
	for _, l := range lines[:10] { // up to the main agent's second Agent call
		cs.handleReadLoopLine(l)
	}
	mainCtx := cs.GetContextUsage()
	for _, l := range lines[10:] {
		cs.handleReadLoopLine(l)
	}
	close(cs.events)
	var tools []string
	var texts []string
	results := 0
	for ev := range cs.events {
		switch ev.Type {
		case core.EventToolUse:
			tools = append(tools, ev.ToolName)
		case core.EventToolResult:
			results++
		case core.EventText:
			if ev.Content != "" {
				texts = append(texts, ev.Content)
			}
		}
	}
	if len(tools) != 2 || tools[0] != "Agent" || tools[1] != "Agent" {
		t.Fatalf("tool uses = %v, want only the main agent's two Agent calls", tools)
	}
	if results != 2 {
		t.Fatalf("tool results = %d, want 2 (the two Agent calls)", results)
	}
	for _, s := range texts {
		if strings.HasPrefix(s, "两条命令已按先 a 后 b 的顺序依次运行,没有并行。\n\n1.") {
			t.Fatalf("background sub-agent's final report leaked as main text: %q", s)
		}
	}
	if len(texts) != 2 {
		t.Fatalf("texts = %q, want the main agent's two replies", texts)
	}
	got := cs.GetContextUsage()
	if mainCtx == nil || got == nil || got.CacheCreationInputTokens == 16617 || got.CacheCreationInputTokens == 1390 {
		t.Fatalf("context usage taken from a sub-agent call: before=%+v after=%+v", mainCtx, got)
	}
}

// Real-run regression (2026-10-18): a background sub-agent that starts its own
// run_in_background Bash and stops to wait for it. Recorded from claude 2.1.302
// in stream-json input mode (testdata/subagent_waits_own_bg.jsonl): the CLI
// reports the agent completed (task_updated + task_notification) while its
// Bash child is still running, then resumes it with a new task_started for the
// same task_id once the child finishes — twice — and only the third
// completion, with no child left, is the real end.
func TestSubTasks_WaitingOnOwnBackgroundWork(t *testing.T) {
	f, err := os.Open("testdata/subagent_waits_own_bg.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	const id = "a3d5a86186c41d4f2"
	cs := newReplaySession(t)
	var started time.Time
	pos := 0
	// at replays through fixture line upto (0-based, inclusive) and checks the row.
	at := func(upto int, want core.SubTaskStatus) core.SubTask {
		t.Helper()
		for ; pos <= upto; pos++ {
			cs.handleReadLoopLine(lines[pos])
		}
		gen, tasks, _ := cs.SubTasks()
		if gen != 1 || len(tasks) != 1 {
			t.Fatalf("line %d: gen=%d rows=%+v, want the one original row (no new table)", upto, gen, tasks)
		}
		r := tasks[0]
		if r.ID != id || r.Status != want {
			t.Fatalf("line %d: row = %+v, want status %s", upto, r, want)
		}
		if started.IsZero() {
			started = r.StartedAt
		} else if !r.StartedAt.Equal(started) {
			t.Fatalf("line %d: started_at moved to %v, want the first start %v", upto, r.StartedAt, started)
		}
		return r
	}
	at(13, core.SubTaskRunning)      // first Bash (background) running
	r := at(20, core.SubTaskWaiting) // "completed" while its Bash child runs
	if r.Summary != "第一条已在后台运行，等待完成通知。" || !r.EndedAt.IsZero() {
		t.Fatalf("waiting row = %+v", r)
	}
	at(37, core.SubTaskRunning) // resumed: task_started, same task_id
	at(39, core.SubTaskRunning)
	if r := at(49, core.SubTaskWaiting); r.Summary != "第一条的输出是 a。" {
		t.Fatalf("second wait row = %+v", r)
	}
	at(60, core.SubTaskRunning)                 // resumed again
	r = at(len(lines)-1, core.SubTaskCompleted) // real end: no child left
	if r.Summary != "两条后台命令都跑完了,退出码都是 0。" || r.EndedAt.IsZero() {
		t.Fatalf("final row = %+v", r)
	}
}
