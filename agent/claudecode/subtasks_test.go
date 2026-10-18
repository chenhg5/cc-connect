package claudecode

import (
	"bufio"
	"context"
	"os"
	"testing"

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
	_, tasks := cs.SubTasks()
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
	gen, tasks := cs.SubTasks()
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
	_, tasks = cs.SubTasks()
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
	gen, tasks := cs.SubTasks()
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
