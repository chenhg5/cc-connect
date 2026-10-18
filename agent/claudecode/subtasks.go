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
	changed   chan struct{}
	closed    bool
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
		if typ, _ := raw["task_type"].(string); typ != "local_agent" {
			return
		}
		if owned, _ := raw["owned_by_subagent"].(bool); owned {
			return
		}
		// A new batch after every earlier row finished starts a new table.
		allDone := true
		for _, r := range t.tasks {
			if r.Running() {
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
		if _, dup := t.index[id]; dup {
			return
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

	var row *core.SubTask
	if i, ok := t.index[id]; ok {
		row = &t.tasks[i]
	} else if i, ok := t.prevIndex[id]; ok {
		row = &t.prev[i]
	} else {
		return // not a sub-agent row (e.g. a Bash task)
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
		if !row.Running() || !finishSubTask(row, st) {
			return
		}
	case "task_notification":
		st, _ := raw["status"].(string)
		changed := row.Running() && finishSubTask(row, st)
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
		if t.tasks[i].Running() {
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
