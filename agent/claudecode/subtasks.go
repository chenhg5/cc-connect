package claudecode

import (
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// maomao fork: sub-agent table (core.SubTaskReporter).
//
// Recorded from claude 2.1.302 stream-json (testdata/subagents_fg_bg.jsonl,
// one foreground and one background Agent call):
//
//	task_started      task_type=local_agent, description, is_backgrounded
//	task_progress     description = the sub-agent's current step ("Running …")
//	task_updated      patch.status = completed / failed / …
//	task_notification status + summary (the sub-agent's final report)
//
// Bash calls made inside a sub-agent arrive as task_started with
// task_type=local_bash and owned_by_subagent=true; they are not rows. Rows are
// kept here rather than in the engine because a background sub-agent keeps
// reporting after the main turn ended, when nobody may be reading the events
// channel.

type subTaskTable struct {
	mu    sync.Mutex
	gen   uint64
	tasks []core.SubTask
	index map[string]int
	// The previous batch (generation gen-1), kept so the engine can still give
	// its message the final edit, and a late notification can still land.
	prev      []core.SubTask
	prevIndex map[string]int
	// Background tasks a sub-agent started itself and that have not ended yet,
	// by sub-agent task_id (children) and by child task_id (childOf).
	children map[string]map[string]bool
	childOf  map[string]string
	changed  chan struct{}
	closed   bool
}

func (t *subTaskTable) ch() chan struct{} {
	if t.changed == nil {
		t.changed = make(chan struct{}, 1)
	}
	return t.changed
}

// notifyLocked signals a change without blocking (coalesced). Caller holds mu.
func (t *subTaskTable) notifyLocked() {
	if t.closed {
		return
	}
	select {
	case t.ch() <- struct{}{}:
	default:
	}
}

// SubTasks implements core.SubTaskReporter.
func (cs *claudeSession) SubTasks() (uint64, []core.SubTask, []core.SubTask) {
	t := &cs.subTasks
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.gen, append([]core.SubTask(nil), t.tasks...), append([]core.SubTask(nil), t.prev...)
}

// SubTasksChanged implements core.SubTaskReporter.
func (cs *claudeSession) SubTasksChanged() <-chan struct{} {
	t := &cs.subTasks
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ch()
}

// handleSubTaskEvent updates the table from a system/task_* event.
func (cs *claudeSession) handleSubTaskEvent(sub string, raw map[string]any) {
	id, _ := raw["task_id"].(string)
	if id == "" {
		return
	}
	t := &cs.subTasks
	t.mu.Lock()
	defer t.mu.Unlock()

	if sub == "task_started" {
		if owned, _ := raw["owned_by_subagent"].(bool); owned {
			t.trackChildLocked(id, raw)
			return
		}
		if typ, _ := raw["task_type"].(string); typ != "local_agent" {
			return
		}
		// A sub-agent resuming after waiting for its own background work gets
		// a new task_started with the same task_id: keep its row (and its
		// first start time) instead of opening a new table.
		if row := t.rowLocked(id); row != nil {
			if row.Status != core.SubTaskRunning {
				row.Status, row.EndedAt = core.SubTaskRunning, time.Time{}
				t.notifyLocked()
			}
			return
		}
		// A new batch after every earlier row finished starts a new table.
		allDone := true
		for _, r := range t.tasks {
			if r.Live() {
				allDone = false
				break
			}
		}
		if allDone {
			t.gen++
			t.prev, t.prevIndex = t.tasks, t.index
			t.tasks = nil
			t.index = map[string]int{}
		}
		if t.index == nil {
			t.index = map[string]int{}
		}
		desc, _ := raw["description"].(string)
		bg, _ := raw["is_backgrounded"].(bool)
		t.index[id] = len(t.tasks)
		t.tasks = append(t.tasks, core.SubTask{
			ID:          id,
			Description: strings.TrimSpace(desc),
			Background:  bg,
			Status:      core.SubTaskRunning,
			StartedAt:   time.Now(),
		})
		t.notifyLocked()
		return
	}

	if parent, ok := t.childOf[id]; ok {
		t.endChildLocked(sub, id, parent, raw)
		return
	}
	row := t.rowLocked(id)
	if row == nil {
		return // not a sub-agent row (e.g. a main-agent Bash task)
	}
	switch sub {
	case "task_progress":
		if !row.Running() {
			return
		}
		step, _ := raw["description"].(string)
		step = strings.TrimSpace(step)
		if step == "" || step == row.Step {
			return
		}
		row.Step = step
	case "task_updated":
		patch, _ := raw["patch"].(map[string]any)
		st, _ := patch["status"].(string)
		if !row.Live() || !t.finishLocked(row, st) {
			return
		}
	case "task_notification":
		st, _ := raw["status"].(string)
		changed := row.Live() && t.finishLocked(row, st)
		if s, _ := raw["summary"].(string); s != "" {
			if first := firstSentence(s); first != row.Summary {
				row.Summary = first
				changed = true
			}
		}
		if !changed {
			return
		}
	default:
		return
	}
	t.notifyLocked()
}

func (t *subTaskTable) rowLocked(id string) *core.SubTask {
	if i, ok := t.index[id]; ok {
		return &t.tasks[i]
	}
	if i, ok := t.prevIndex[id]; ok {
		return &t.prev[i]
	}
	return nil
}

// trackChildLocked records a background task a sub-agent started itself
// (owned_by_subagent, is_backgrounded, parent_task_id = the sub-agent).
func (t *subTaskTable) trackChildLocked(id string, raw map[string]any) {
	parent, _ := raw["parent_task_id"].(string)
	bg, _ := raw["is_backgrounded"].(bool)
	if parent == "" || !bg {
		return
	}
	if t.children == nil {
		t.children, t.childOf = map[string]map[string]bool{}, map[string]string{}
	}
	if t.children[parent] == nil {
		t.children[parent] = map[string]bool{}
	}
	t.children[parent][id] = true
	t.childOf[id] = parent
}

// endChildLocked forgets a child once the CLI reports it ended.
func (t *subTaskTable) endChildLocked(sub, id, parent string, raw map[string]any) {
	st, _ := raw["status"].(string)
	if sub == "task_updated" {
		patch, _ := raw["patch"].(map[string]any)
		st, _ = patch["status"].(string)
	} else if sub != "task_notification" {
		return
	}
	if !isTerminalStatus(st) {
		return
	}
	delete(t.childOf, id)
	if kids := t.children[parent]; kids != nil {
		delete(kids, id)
		if len(kids) == 0 {
			delete(t.children, parent)
		}
	}
}

// finishLocked applies a terminal CLI status. claude 2.1.302 also reports a
// sub-agent "completed" when it only stopped to wait for background work it
// started itself (testdata/subagent_waits_own_bg.jsonl): while such a child is
// still running the row becomes waiting, and the CLI resumes the same task_id
// with a new task_started once the child ends.
func (t *subTaskTable) finishLocked(row *core.SubTask, st string) bool {
	if !isTerminalStatus(st) {
		return false
	}
	if st == "completed" && len(t.children[row.ID]) > 0 {
		if row.Status == core.SubTaskWaiting {
			return false
		}
		row.Status = core.SubTaskWaiting
		return true
	}
	return finishSubTask(row, st)
}

func isTerminalStatus(st string) bool {
	switch st {
	case "completed", "failed", "error", "stopped", "killed", "cancelled":
		return true
	}
	return false
}

// finishSubTask applies a terminal CLI status; false when st is not terminal.
func finishSubTask(row *core.SubTask, st string) bool {
	switch st {
	case "completed":
		row.Status = core.SubTaskCompleted
	case "failed", "error":
		row.Status = core.SubTaskFailed
	case "stopped", "killed", "cancelled":
		row.Status = core.SubTaskStopped
	default:
		return false
	}
	row.EndedAt = time.Now()
	return true
}

// closeSubTasks marks rows still running as stopped (the process is gone) and
// closes the change channel. Called once from the read loop's exit path.
func (cs *claudeSession) closeSubTasks() {
	t := &cs.subTasks
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	changed := false
	for i := range t.tasks {
		if t.tasks[i].Live() {
			t.tasks[i].Status = core.SubTaskStopped
			t.tasks[i].EndedAt = time.Now()
			changed = true
		}
	}
	if changed {
		t.notifyLocked()
	}
	t.closed = true
	close(t.ch())
}

// firstSentence returns the first sentence (or first line) of a sub-agent's
// final report.
func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	rs := []rune(s)
	for i, r := range rs {
		switch r {
		case '。', '！', '？':
			return string(rs[:i+1])
		case '.', '!', '?':
			if i+1 == len(rs) || rs[i+1] == ' ' {
				return string(rs[:i+1])
			}
		}
	}
	return s
}
