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
	SubTaskRunning SubTaskStatus = "running"
	// SubTaskWaiting: the sub-agent stopped to wait for its own background work
	// and will resume when that finishes (not done yet).
	SubTaskWaiting   SubTaskStatus = "waiting"
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

// Running reports whether the sub-agent is working right now.
func (t SubTask) Running() bool { return t.Status == SubTaskRunning }

// Live reports whether the sub-agent has not finished yet: running, or
// waiting for its own background work.
func (t SubTask) Live() bool { return t.Status == SubTaskRunning || t.Status == SubTaskWaiting }

// SubTaskReporter is an optional interface for agent sessions that run
// sub-agents (maomao fork). SubTasks returns the current table and its
// generation: the generation changes when a new batch starts after every row of
// the previous one had finished, and the engine then posts a fresh message
// instead of editing the old one far up the channel. previous is the final
// state of batch generation-1 (nil if none), so the engine can still give the
// old message its last edit when that edit was throttled at the switch.
// SubTasksChanged is signalled (coalesced) on every change and closed when the
// session ends.
type SubTaskReporter interface {
	SubTasks() (generation uint64, tasks []SubTask, previous []SubTask)
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
	_, tasks, _ := r.SubTasks()
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
		// The previous batch's message, kept until its final state is shown.
		prevHandle any
		prevGen    uint64
		prevSent   string
		timer      *time.Timer
		timerC     <-chan time.Time
	)
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	lang := func() Language { return e.i18n.CurrentLang() }
	edit := func(ctx context.Context, h any, content string) bool {
		if err := updater.UpdateMessage(ctx, h, content); err != nil {
			slog.Warn("subtask table: edit failed", "error", err)
			return false
		}
		return true
	}
	flush := func(ctx context.Context) {
		g, tasks, prev := r.SubTasks()
		if g != gen && handle != nil {
			prevHandle, prevGen, prevSent = handle, gen, lastSent
			handle, lastSent = nil, ""
		}
		gen = g
		// Final state of the previous batch: its last edit may have been
		// throttled when the next batch started.
		if prevHandle != nil && prevGen+1 == g && len(prev) > 0 {
			if c := BuildSubTaskTablePayload(lang(), prev); c != "" && c != prevSent && edit(ctx, prevHandle, c) {
				prevSent = c
			}
		}
		if len(tasks) == 0 {
			return
		}
		content := BuildSubTaskTablePayload(lang(), tasks)
		if content == "" || content == lastSent {
			return
		}
		state.mu.Lock()
		p, replyCtx := state.platform, state.replyCtx
		state.mu.Unlock()
		if handle == nil {
			h, err := starter.SendPreviewStart(ctx, markAgentReply(p, replyCtx), content)
			if err != nil || h == nil {
				slog.Warn("subtask table: send failed", "error", err)
				return
			}
			handle = h
		} else if !edit(ctx, handle, content) {
			return
		}
		lastSent, lastAt = content, time.Now()
	}
	flushNow := func() {
		ctx, cancel := context.WithTimeout(e.ctx, compactProgressAPITimeout)
		defer cancel()
		flush(ctx)
	}
	for {
		closed := false
		select {
		case <-e.ctx.Done():
			// Engine stopping (restart): the agent process dies with it, so one
			// last edit marks rows still running as stopped instead of leaving
			// the table at 🔄. e.ctx is already cancelled; bound the call with
			// the progress card's API timeout.
			if handle == nil {
				return
			}
			_, tasks, _ := r.SubTasks()
			now := time.Now()
			for i := range tasks {
				if tasks[i].Live() {
					tasks[i].Status, tasks[i].EndedAt = SubTaskStopped, now
				}
			}
			if c := BuildSubTaskTablePayload(lang(), tasks); c != "" && c != lastSent {
				ctx, cancel := context.WithTimeout(context.Background(), compactProgressAPITimeout)
				edit(ctx, handle, c)
				cancel()
			}
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
			flushNow() // last state (rows the dead process left are "stopped")
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
		flushNow()
	}
}
