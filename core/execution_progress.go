package core

import (
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// ProgressStep is a public execution plan, never internal model reasoning.
type ProgressStep struct {
	Step   string `json:"step"`
	Status string `json:"status"`
}
type RuntimeProgress struct {
	Phase         string
	QueuePosition int
}

// ExecutionProgress is stored with a task so local status and the manager see
// the same facts even while the model request is blocked. Times are separate:
// a queue heartbeat proves connectivity, not that useful work has advanced.
type ExecutionProgress struct {
	Phase         string         `json:"phase"`
	StartedAt     time.Time      `json:"started_at"`
	PhaseSince    time.Time      `json:"phase_since"`
	LastActivity  time.Time      `json:"last_activity"`
	LastAdvance   time.Time      `json:"last_advance"`
	QueuePosition int            `json:"queue_position,omitempty"`
	Current       string         `json:"current,omitempty"`
	Summary       string         `json:"summary,omitempty"`
	Recent        []string       `json:"recent,omitempty"`
	Plan          []ProgressStep `json:"plan,omitempty"`
}
type executionTracker struct {
	mu     sync.Mutex
	value  ExecutionProgress
	active map[string]string
	text   string
}

func newExecutionTracker(now time.Time) *executionTracker {
	return &executionTracker{value: ExecutionProgress{Phase: "waiting_response", StartedAt: now, PhaseSince: now, LastActivity: now, LastAdvance: now}, active: map[string]string{}}
}
func (p *executionTracker) phase(phase string, now time.Time) {
	if p.value.Phase != phase {
		p.value.Phase = phase
		p.value.PhaseSince = now
	}
	if phase != "waiting_model" {
		p.value.QueuePosition = 0
	}
}
func (p *executionTracker) resume(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	phase := "waiting_response"
	if len(p.active) > 0 {
		phase = "tool"
	}
	p.phase(phase, now)
	p.value.LastActivity = now
}
func (p *executionTracker) snapshot() ExecutionProgress {
	p.mu.Lock()
	defer p.mu.Unlock()
	v := p.value
	v.Plan = append([]ProgressStep(nil), v.Plan...)
	v.Recent = append([]string(nil), v.Recent...)
	return v
}
func (p *executionTracker) observe(ev Event, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.value.LastActivity = now
	switch ev.Type {
	case EventProgress:
		if ev.Content != "" && strings.TrimSpace(ev.Content) != "NO_REPLY" {
			p.value.Summary = truncateIf(strings.Join(strings.Fields(ev.Content), " "), 360)
			p.value.LastAdvance = now
			if len(p.active) == 0 {
				p.phase("waiting_response", now)
			}
		}
		if ev.Progress != nil && ev.Progress.Phase == "waiting_model" {
			p.phase("waiting_model", now)
			p.value.QueuePosition = ev.Progress.QueuePosition
		}
		return
	case EventThinking:
		// Activity can be acknowledged without ever copying reasoning content.
		if len(p.active) == 0 {
			p.phase("waiting_response", now)
		}
		return
	case EventPlan:
		if len(p.active) == 0 {
			p.phase("waiting_response", now)
		}
		p.value.Plan = boundedPlan(ev.Plan)
		p.value.LastAdvance = now
		return
	case EventToolUse:
		p.text = ""
		p.phase("tool", now)
		label := safeToolLabel(ev)
		key := ev.ToolID
		if key == "" {
			key = ev.ToolName
		}
		p.active[key] = label
		p.value.Current = label
		if strings.Contains(strings.ToLower(ev.ToolName), "update_plan") || strings.EqualFold(ev.ToolName, "TodoWrite") {
			raw := ev.ToolInputRaw
			if raw == nil {
				_ = json.Unmarshal([]byte(ev.ToolInput), &raw)
			}
			for _, field := range []string{"plan", "todos"} {
				if data, err := json.Marshal(raw[field]); err == nil {
					var steps []ProgressStep
					if json.Unmarshal(data, &steps) == nil && len(steps) > 0 {
						p.value.Plan = boundedPlan(steps)
					}
				}
			}
		}
	case EventToolResult:
		if (ev.ToolUpdate && ev.ToolStatus == "") || ev.ToolStatus == "in_progress" || ev.ToolStatus == "pending" || ev.ToolStatus == "running" {
			return
		}
		key := ev.ToolID
		if key == "" {
			key = ev.ToolName
		}
		label := p.active[key]
		if label == "" {
			label = "tool"
		}
		delete(p.active, key)
		mark := "✓ "
		if ev.ToolStatus == "failed" || (ev.ToolSuccess != nil && !*ev.ToolSuccess) || (ev.ToolExitCode != nil && *ev.ToolExitCode != 0) {
			mark = "✗ "
		}
		p.value.Recent = append(p.value.Recent, mark+label)
		if len(p.value.Recent) > 3 {
			p.value.Recent = p.value.Recent[len(p.value.Recent)-3:]
		}
		p.value.Current = ""
		for _, s := range p.active {
			p.value.Current = s
			break
		}
		if len(p.active) == 0 {
			p.phase("waiting_response", now)
		}
	case EventText:
		if len(p.active) == 0 {
			p.phase("responding", now)
		}
		p.text = truncateIf(p.text+ev.Content, 360)
		if !strings.Contains(p.text, "NO_REPLY") {
			p.value.Summary = strings.Join(strings.Fields(p.text), " ")
		}
	case EventPermissionRequest:
		phase := "waiting_approval"
		if ev.ToolName == "AskUserQuestion" {
			phase = "waiting_input"
		}
		p.phase(phase, now)
	case EventResult:
		if !ev.Done {
			return
		}
		p.phase("submitted", now)
	case EventError:
		p.phase("failed", now)
	default:
		return
	}
	p.value.LastAdvance = now
}
func boundedPlan(steps []ProgressStep) []ProgressStep {
	var out []ProgressStep
	for _, s := range steps {
		if len(out) >= 20 {
			break
		}
		if s.Status != "pending" && s.Status != "in_progress" && s.Status != "completed" {
			continue
		}
		if strings.TrimSpace(s.Step) == "" {
			continue
		}
		s.Step = truncateIf(strings.Join(strings.Fields(s.Step), " "), 120)
		out = append(out, s)
	}
	return out
}
func safeToolLabel(ev Event) string {
	kind, _ := ev.Metadata["tool_kind"].(string)
	name := strings.ToLower(ev.ToolName + " " + kind)
	input := strings.ToLower(ev.ToolInput)
	switch {
	case strings.Contains(name, "plan") || strings.Contains(name, "todo"):
		return "plan"
	case strings.Contains(name, "read"):
		return "read"
	case strings.Contains(name, "edit") || strings.Contains(name, "write") || strings.Contains(name, "patch"):
		return "edit"
	case strings.Contains(name, "search") || strings.Contains(name, "grep") || strings.Contains(name, "glob"):
		return "search"
	case strings.Contains(name, "bash") || strings.Contains(name, "exec") || strings.Contains(name, "command"):
		switch {
		case strings.Contains(input, " test") || strings.Contains(input, "pytest") || strings.Contains(input, "vitest"):
			return "test"
		case strings.Contains(input, "build") || strings.Contains(input, "compile") || strings.Contains(input, "make"):
			return "build"
		case strings.Contains(input, "rg ") || strings.Contains(input, "grep "):
			return "search"
		case strings.Contains(input, "sed ") || strings.Contains(input, "cat "):
			return "read"
		}
		return "command"
	}
	return "tool"
}
func progressOperation(i *I18n, op string) string {
	keys := map[string]MsgKey{"read": MsgProgressRead, "edit": MsgProgressEdit, "search": MsgProgressSearch, "test": MsgProgressTest, "build": MsgProgressBuild, "command": MsgProgressCommand, "plan": MsgProgressPlanning, "tool": MsgProgressTool}
	prefix := ""
	if strings.HasPrefix(op, "✓ ") || strings.HasPrefix(op, "✗ ") {
		prefix = string([]rune(op)[:2])
		op = string([]rune(op)[2:])
	}
	if k, ok := keys[op]; ok {
		return prefix + i.T(k)
	}
	return prefix + i.T(MsgProgressTool)
}
func (v ExecutionProgress) text(i *I18n, now time.Time) string {
	if v.StartedAt.IsZero() {
		return ""
	}
	duration := func(t time.Time) string {
		d := now.Sub(t)
		if d < 0 {
			d = 0
		}
		return d.Round(time.Second).String()
	}
	phase := v.Phase
	phaseKeys := map[string]MsgKey{"waiting_model": MsgProgressQueue, "waiting_response": MsgProgressResponse, "responding": MsgProgressResponding, "tool": MsgProgressTool}
	label := workflowStatus(i, phase)
	if k, ok := phaseKeys[phase]; ok {
		label = i.T(k)
	}
	out := i.Tf(MsgProgressTiming, label, duration(v.PhaseSince), duration(v.StartedAt), duration(v.LastAdvance), duration(v.LastActivity))
	if phase == "waiting_model" {
		out += "\n" + i.Tf(MsgProgressPosition, v.QueuePosition)
	}
	if v.Current != "" {
		out += "\n" + i.Tf(MsgWorkflowCurrent, progressOperation(i, v.Current))
	}
	completed := 0
	current, next := "", ""
	for _, s := range v.Plan {
		switch s.Status {
		case "completed":
			completed++
		case "in_progress":
			if current == "" {
				current = s.Step
			}
		case "pending":
			if next == "" {
				next = s.Step
			}
		}
	}
	if len(v.Plan) > 0 {
		if current == "" {
			current = i.T(MsgProgressNoCurrent)
		}
		out += "\n" + i.Tf(MsgProgressPlan, completed, len(v.Plan), current)
	}
	if next == "" {
		if len(v.Plan) == 0 {
			next = i.T(MsgProgressNoPlan)
		} else if completed < len(v.Plan) {
			next = i.T(MsgProgressFinishStep)
		} else {
			next = i.T(MsgProgressReview)
		}
	}
	out += "\n" + i.Tf(MsgProgressNext, next)
	if len(v.Recent) > 0 {
		out += "\n" + i.Tf(MsgProgressRecent, strings.Join(renderOperations(i, v.Recent), " · "))
	}
	if v.Summary != "" {
		out += "\n" + i.Tf(MsgProgressSummary, v.Summary)
	}
	if alert := v.attention(i, now); alert != "" {
		out += "\n" + alert
	}
	if phase != "submitted" && phase != "failed" {
		out += "\n" + i.T(MsgProgressControls)
	}
	return out
}
func (e *Engine) liveProgressText(key string) string {
	e.interactiveMu.Lock()
	s := e.interactiveStates[key]
	e.interactiveMu.Unlock()
	if s == nil {
		return ""
	}
	s.mu.Lock()
	p := s.progress
	s.mu.Unlock()
	if p == nil {
		return ""
	}
	return p.snapshot().text(e.i18n, time.Now())
}
func (e *Engine) saveExecutionProgress(p *TeamProject, t *TeamTask, v ExecutionProgress) {
	if p == nil || t == nil || e.teamProjects == nil {
		return
	}
	changed := false
	err := e.teamProjects.mutateProject(p.ID, func(live *TeamProject) error {
		task, ok := live.Tasks[t.ID]
		if !ok || live.Paused || live.WorkflowEpoch != t.Epoch || task.Epoch != t.Epoch || task.Attempt != t.Attempt || !taskActive(task.Status) {
			return nil
		}
		task.Progress = &v
		live.Tasks[t.ID] = task
		changed = true
		return nil
	})
	if err != nil {
		slog.Warn("task progress save failed", "error", err)
	} else if changed {
		e.queueWorkflowRefresh(p.ID)
	}
}

func renderOperations(i *I18n, ops []string) []string {
	out := make([]string, len(ops))
	for n, op := range ops {
		out[n] = progressOperation(i, op)
	}
	return out
}
func (p *executionTracker) finish(phase string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.phase(phase, time.Now())
}

// compactText keeps decisions and blockers visible outside expandable details.
func (v ExecutionProgress) compactText(i *I18n, now time.Time) string {
	if v.StartedAt.IsZero() {
		return ""
	}
	keys := map[string]MsgKey{"waiting_model": MsgProgressQueue, "waiting_response": MsgProgressResponse, "responding": MsgProgressResponding, "tool": MsgProgressTool}
	label := workflowStatus(i, v.Phase)
	if k, ok := keys[v.Phase]; ok {
		label = i.T(k)
	}
	d := now.Sub(v.StartedAt)
	if d < 0 {
		d = 0
	}
	out := i.Tf(MsgProgressBrief, label, d.Round(time.Second).String())
	count := 0
	current := ""
	for _, step := range v.Plan {
		if step.Status == "completed" {
			count++
		}
		if current == "" && step.Status == "in_progress" {
			current = step.Step
		}
	}
	if current == "" && v.Current != "" {
		current = progressOperation(i, v.Current)
	}
	if len(v.Plan) > 0 {
		if current == "" {
			current = i.T(MsgProgressNoCurrent)
		}
		out += "\n" + i.Tf(MsgProgressPlan, count, len(v.Plan), current)
	} else if current != "" {
		out += "\n" + i.Tf(MsgWorkflowCurrent, current)
	}
	if v.Phase == "waiting_model" {
		out += "\n" + i.Tf(MsgProgressPosition, v.QueuePosition)
	}
	if alert := v.attention(i, now); alert != "" {
		out += "\n" + alert
	}
	return out
}
func (v ExecutionProgress) attention(i *I18n, now time.Time) string {
	if v.Phase == "waiting_model" && now.Sub(v.PhaseSince) >= 5*time.Minute {
		return i.T(MsgProgressQueueSlow)
	}
	switch v.Phase {
	case "waiting_approval", "waiting_input", "submitted", "failed", "stopped":
		return ""
	}
	if now.Sub(v.LastActivity) >= 2*time.Minute {
		return i.T(MsgProgressSilent)
	}
	return ""
}
func (v ExecutionProgress) details(i *I18n, now time.Time) string {
	body := v.text(i, now)
	// Include every retained plan step, not just the current step or count.
	if len(v.Plan) > 0 {
		var lines []string
		for _, step := range v.Plan {
			symbol := "○"
			if step.Status == "completed" {
				symbol = "✓"
			}
			if step.Status == "in_progress" {
				symbol = "▶"
			}
			lines = append(lines, symbol+" "+step.Step)
		}
		body += "\n\n" + i.T(MsgProgressFullPlan) + "\n" + strings.Join(lines, "\n")
	}
	return body
}
