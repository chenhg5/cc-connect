package core

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"
)

// maomao fork: the channel's sub-agent table ("子进程表").
//
// Claude Code reports each sub-agent it starts (Agent tool, foreground or
// run_in_background) through system/task_* events. The agent adapter keeps one
// row per sub-agent; the engine posts the table as its own message, separate
// from the progress card, and edits it whenever a row changes — also after the
// main turn ended, because a background sub-agent keeps reporting then.

// SubTaskStatus is the state of one sub-agent row.
type SubTaskStatus string

const (
	SubTaskRunning   SubTaskStatus = "running"
	SubTaskCompleted SubTaskStatus = "completed"
	SubTaskFailed    SubTaskStatus = "failed"
	SubTaskStopped   SubTaskStatus = "stopped"
)

// SubTask is one row of the sub-agent table.
type SubTask struct {
	ID          string        `json:"id"`
	Description string        `json:"description"`
	Background  bool          `json:"background,omitempty"`
	Step        string        `json:"step,omitempty"`    // what it is doing now (task_progress)
	Status      SubTaskStatus `json:"status"`            // running / completed / failed / stopped
	Summary     string        `json:"summary,omitempty"` // first sentence of the final report
	StartedAt   time.Time     `json:"started_at"`
	EndedAt     time.Time     `json:"ended_at,omitempty"`
}

// Running reports whether the sub-agent has not finished yet.
func (t SubTask) Running() bool { return t.Status == SubTaskRunning }

// SubTaskReporter is an optional interface for agent sessions that run
// sub-agents (maomao fork). SubTasks returns the current table and its
// generation: the generation changes when a new batch starts after every row of
// the previous one had finished, and the engine then posts a fresh message
// instead of editing the old one far up the channel. SubTasksChanged is
// signalled (coalesced) on every change and closed when the session ends.
type SubTaskReporter interface {
	SubTasks() (generation uint64, tasks []SubTask)
	SubTasksChanged() <-chan struct{}
}

// SubTaskTablePayloadSupport is implemented by platforms that render the
// sub-agent table payload (Discord). Other platforms get no table.
type SubTaskTablePayloadSupport interface {
	SupportsSubTaskTablePayload() bool
}

// SubTaskTablePayloadPrefix marks a structured sub-agent table message.
const SubTaskTablePayloadPrefix = "__cc_connect_subtask_table_v1__:"

// SubTaskTablePayload is the transport form of the table.
type SubTaskTablePayload struct {
	Lang  string    `json:"lang,omitempty"`
	Tasks []SubTask `json:"tasks"`
}

// BuildSubTaskTablePayload encodes the table; "" when there is nothing to show.
func BuildSubTaskTablePayload(lang Language, tasks []SubTask) string {
	if len(tasks) == 0 {
		return ""
	}
	b, err := json.Marshal(SubTaskTablePayload{Lang: string(lang), Tasks: tasks})
	if err != nil {
		return ""
	}
	return SubTaskTablePayloadPrefix + string(b)
}

// ParseSubTaskTablePayload decodes a payload built by BuildSubTaskTablePayload.
func ParseSubTaskTablePayload(content string) (*SubTaskTablePayload, bool) {
	if !strings.HasPrefix(content, SubTaskTablePayloadPrefix) {
		return nil, false
	}
	var p SubTaskTablePayload
	if err := json.Unmarshal([]byte(strings.TrimPrefix(content, SubTaskTablePayloadPrefix)), &p); err != nil {
		return nil, false
	}
	return &p, true
}

// foregroundSubTask returns the most recently started foreground sub-agent that
// is still running: the one the main turn is waiting for.
func foregroundSubTask(r SubTaskReporter) (SubTask, bool) {
	_, tasks := r.SubTasks()
	for i := len(tasks) - 1; i >= 0; i-- {
		if t := tasks[i]; !t.Background && t.Running() {
			return t, true
		}
	}
	return SubTask{}, false
}

// subAgentWaitText is the progress-card line shown while the main turn waits
// for a foreground sub-agent: "等子 agent：<描述> — <它当前那一步>".
func subAgentWaitText(zh bool, t SubTask) string {
	head := "Waiting for sub-agent: "
	if zh {
		head = "等子 agent："
	}
	s := head + strings.TrimSpace(t.Description)
	if step := strings.TrimSpace(t.Step); step != "" {
		s += " — " + step
	}
	return s
}

// ensureSubTaskTable starts the table updater for the state's current agent
// session once. It runs until the session's change channel closes, so updates
// from background sub-agents after the main turn still reach the channel.
func (e *Engine) ensureSubTaskTable(state *interactiveState) {
	state.mu.Lock()
	as := state.agentSession
	p := state.platform
	if as == nil || state.subTableFor == as {
		state.mu.Unlock()
		return
	}
	r, ok := as.(SubTaskReporter)
	if !ok {
		state.mu.Unlock()
		return
	}
	if s, ok := p.(SubTaskTablePayloadSupport); !ok || !s.SupportsSubTaskTablePayload() {
		state.mu.Unlock()
		return
	}
	starter, ok1 := p.(PreviewStarter)
	updater, ok2 := p.(MessageUpdater)
	if !ok1 || !ok2 {
		state.mu.Unlock()
		return
	}
	state.subTableFor = as
	state.mu.Unlock()

	// Same edit interval as the progress card (ProgressUpdateThrottler).
	var interval time.Duration
	if th, ok := p.(ProgressUpdateThrottler); ok {
		interval = th.ProgressUpdateInterval()
	}
	go e.runSubTaskTable(state, r, starter, updater, interval)
}

func (e *Engine) runSubTaskTable(state *interactiveState, r SubTaskReporter, starter PreviewStarter, updater MessageUpdater, interval time.Duration) {
	changed := r.SubTasksChanged()
	var (
		handle   any
		gen      uint64
		lastSent string
		lastAt   time.Time
		timer    *time.Timer
		timerC   <-chan time.Time
	)
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	call := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(e.ctx, compactProgressAPITimeout)
	}
	flush := func() {
		g, tasks := r.SubTasks()
		if len(tasks) == 0 {
			return
		}
		if g != gen {
			gen, handle, lastSent = g, nil, ""
		}
		content := BuildSubTaskTablePayload(e.i18n.CurrentLang(), tasks)
		if content == "" || content == lastSent {
			return
		}
		state.mu.Lock()
		p, replyCtx := state.platform, state.replyCtx
		state.mu.Unlock()
		ctx, cancel := call()
		defer cancel()
		if handle == nil {
			h, err := starter.SendPreviewStart(ctx, markAgentReply(p, replyCtx), content)
			if err != nil || h == nil {
				slog.Warn("subtask table: send failed", "error", err)
				return
			}
			handle = h
		} else if err := updater.UpdateMessage(ctx, handle, content); err != nil {
			slog.Warn("subtask table: edit failed", "error", err)
			return
		}
		lastSent, lastAt = content, time.Now()
	}
	for {
		closed := false
		select {
		case <-e.ctx.Done():
			return
		case _, ok := <-changed:
			if !ok {
				closed = true
				changed = nil
			}
		case <-timerC:
			timerC = nil
		}
		if closed {
			flush() // last state (rows the dead process left are "stopped")
			return
		}
		if wait := interval - time.Since(lastAt); handle != nil && wait > 0 {
			// Throttled: flush once the interval is up, so the last change is
			// never dropped.
			if timerC == nil {
				if timer != nil {
					timer.Stop()
				}
				timer = time.NewTimer(wait)
				timerC = timer.C
			}
			continue
		}
		flush()
	}
}
