package core

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var errDuplicateTaskHandoff = errors.New("duplicate task handoff")

// TeamTask is durable task state, independent of an agent turn or message card.
// Submitted means there is a result to inspect; only explicit acceptance unlocks dependencies.
type TeamTask struct {
	ProcessMessageIDs  []string           `json:"process_message_ids,omitempty"`
	Round              uint64             `json:"round,omitempty"`
	CancelledFollowups map[string]string  `json:"cancelled_followups,omitempty"`
	DependencyAttempts map[string]int     `json:"dependency_attempts,omitempty"`
	History            []TeamTaskAttempt  `json:"history,omitempty"`
	Followups          map[string]string  `json:"followups,omitempty"`
	SeenMessages       []string           `json:"seen_messages,omitempty"`
	RequesterID        string             `json:"requester_id,omitempty"`
	ManagerSessionKey  string             `json:"manager_session_key,omitempty"`
	NativeThread       bool               `json:"native_thread,omitempty"`
	Progress           *ExecutionProgress `json:"progress,omitempty"`
	ID                 string             `json:"id"`
	Title              string             `json:"title"`
	Assignee           string             `json:"assignee"`
	Reason             string             `json:"reason"`
	Instruction        string             `json:"instruction"`
	Deliverable        string             `json:"deliverable"`
	DependsOn          []string           `json:"depends_on,omitempty"`
	Status             string             `json:"status"`
	Result             string             `json:"result,omitempty"`
	Evidence           string             `json:"evidence,omitempty"`
	SessionKey         string             `json:"session_key,omitempty"`
	RootMessageID      string             `json:"root_message_id,omitempty"`
	Epoch              uint64             `json:"epoch"`
	Attempt            int                `json:"attempt"`
	ReportID           string             `json:"report_id,omitempty"`
	ResultMessageID    string             `json:"result_message_id,omitempty"`
	UpdatedAt          time.Time          `json:"updated_at"`
}

type TeamTaskAttempt struct {
	ProcessMessageIDs []string `json:"process_message_ids,omitempty"`
	Round             uint64   `json:"round,omitempty"`
	RootMessageID     string   `json:"root_message_id,omitempty"`
	SessionKey        string   `json:"session_key,omitempty"`
	Attempt           int      `json:"attempt"`
	Instruction       string   `json:"instruction"`
	Result            string   `json:"result,omitempty"`
	Evidence          string   `json:"evidence,omitempty"`
	Status            string   `json:"status"`
	ReportID          string   `json:"report_id,omitempty"`
	ResultMessageID   string   `json:"result_message_id,omitempty"`
}

func nextTaskAttempt(t *TeamTask, instruction string) {
	t.History = append(t.History, TeamTaskAttempt{ProcessMessageIDs: append([]string(nil), t.ProcessMessageIDs...), Round: taskRound(*t), RootMessageID: t.RootMessageID, SessionKey: t.SessionKey, Attempt: t.Attempt, Instruction: t.Instruction, Result: t.Result, Evidence: t.Evidence, Status: t.Status, ReportID: t.ReportID, ResultMessageID: t.ResultMessageID})
	t.Attempt++
	t.Instruction = instruction
	t.Result, t.Evidence, t.ReportID = "", "", ""
	t.ResultMessageID = ""
	t.ProcessMessageIDs = nil
	t.Progress = nil
	t.Status = "queued"
}

func taskActive(status string) bool {
	switch status {
	case "queued", "running", "waiting_approval", "waiting_input":
		return true
	}
	return false
}
func (s *TeamProjectStore) mutateProject(id string, fn func(*TeamProject) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return err
	}
	p, ok := all[id]
	if !ok {
		return fmt.Errorf("project not found")
	}
	if err = fn(&p); err != nil {
		return err
	}
	all[id] = p
	return s.save(all)
}
func (s *TeamProjectStore) updateTask(projectID, taskID string, fn func(*TeamTask) error) error {
	return s.mutateProject(projectID, func(p *TeamProject) error {
		task, ok := p.Tasks[taskID]
		if !ok {
			return fmt.Errorf("task not found: %s", taskID)
		}
		if err := fn(&task); err != nil {
			return err
		}
		task.UpdatedAt = time.Now().UTC()
		p.Tasks[taskID] = task
		return nil
	})
}
func (s *TeamProjectStore) assignTask(projectID string, req TeamActionRequest) (TeamTask, error) {
	var task TeamTask
	err := s.mutateProject(projectID, func(p *TeamProject) error {
		if p.Paused {
			return fmt.Errorf("project paused; the owner must use /project resume")
		}
		if !roundAcceptsWork(*p) {
			return fmt.Errorf("project is not ready")
		}
		if !stringSliceContains(p.Bots, req.To) || req.To == p.Manager {
			return fmt.Errorf("select a project member")
		}
		if req.sourceMessageID != "" {
			for _, existing := range p.Tasks {
				if stringSliceContains(existing.SeenMessages, req.sourceMessageID) {
					return errDuplicateTaskHandoff
				}
			}
		}
		for _, other := range p.Tasks {
			if (taskActive(other.Status) || len(other.Followups) > 0) && other.Assignee == req.To {
				return fmt.Errorf("member already executing %s", other.ID)
			}
		}
		if taskActive(p.Reports[req.To].Status) {
			return fmt.Errorf("member is %s; wait for its report", p.Reports[req.To].Status)
		}
		if req.TaskID != "" {
			old, ok := p.Tasks[req.TaskID]
			if !ok || old.Assignee != req.To {
				return fmt.Errorf("retry requires the same task and assignee")
			}
			if old.Status != "planned" && old.Status != "failed" && old.Status != "stopped" && old.Status != "interrupted" && old.Status != "blocked" && old.Status != "submitted" && old.Status != "accepted" {
				return fmt.Errorf("task cannot be retried in %s", old.Status)
			}
			task = old
			if old.Status == "planned" {
				if strings.TrimSpace(req.Message) != "" {
					task.Instruction = req.Message
				}
			} else {
				if strings.TrimSpace(req.Message) == "" {
					return fmt.Errorf("rework instructions required")
				}
				nextTaskAttempt(&task, req.Message)
			}
			// Each dispatched attempt has its own incoming group message. Keep the
			// old route only in history, including while the new send is pending.
			task.RootMessageID, task.SessionKey = "", ""
			task.Reason = req.Reason
		} else {
			// Legacy callers still work, but new manager instructions always provide metadata.
			title := strings.TrimSpace(req.Title)
			if title == "" {
				title = truncateIf(strings.Join(strings.Fields(req.Message), " "), 80)
			}
			if title == "" {
				return fmt.Errorf("task instructions required")
			}
			p.TaskSequence++
			task = TeamTask{ID: fmt.Sprintf("T%03d", p.TaskSequence), Title: title, Assignee: req.To, Reason: req.Reason, Instruction: req.Message, Deliverable: req.Deliverable, DependsOn: req.DependsOn, Attempt: 1}
		}
		task.DependencyAttempts = map[string]int{}
		for _, dep := range task.DependsOn {
			d, ok := p.Tasks[dep]
			if !ok || d.Status != "accepted" || len(d.Followups) > 0 || !taskDependenciesReady(*p, d, map[string]bool{}) {
				return fmt.Errorf("dependency %s has not been accepted", dep)
			}
			task.DependencyAttempts[dep] = d.Attempt
		}
		task.Status = "queued"
		if req.sourceMessageID != "" {
			task.RootMessageID, task.SessionKey = req.sourceMessageID, req.sourceThread
			task.SeenMessages = append(task.SeenMessages, req.sourceMessageID)
		}
		task.Round = projectRound(*p)
		task.Epoch = p.WorkflowEpoch
		task.UpdatedAt = time.Now().UTC()
		if p.Tasks == nil {
			p.Tasks = map[string]TeamTask{}
		}
		p.Tasks[task.ID] = task
		if p.Reports == nil {
			p.Reports = map[string]TeamMemberReport{}
		}
		p.Reports[req.To] = TeamMemberReport{Epoch: task.Epoch, TaskID: task.ID, Attempt: task.Attempt, ID: task.ID, Bot: req.To, Status: "queued", Content: task.Instruction, UpdatedAt: task.UpdatedAt}
		return nil
	})
	return task, err
}
func applyTaskReport(t *TeamTask, r TeamMemberReport) error {
	if t.Status == "stopped" || t.Status == "accepted" {
		return nil
	}
	if t.ReportID != "" && t.ReportID != r.ID {
		return nil
	}
	t.ReportID = r.ID
	t.Status = r.Status
	if r.Status == "completed" {
		t.Status = "submitted"
	}
	if !taskActive(t.Status) || t.Status == "waiting_input" {
		t.Result = r.Content
	}
	return nil
}
func (s *TeamProjectStore) acceptTask(projectID, taskID, evidence string) error {
	if strings.TrimSpace(evidence) == "" {
		return fmt.Errorf("acceptance requires --evidence describing inspected artifacts and verification")
	}
	return s.mutateProject(projectID, func(p *TeamProject) error {
		if !roundAcceptsWork(*p) {
			return errProjectEnded
		}
		t, ok := p.Tasks[taskID]
		if !ok {
			return fmt.Errorf("task not found: %s", taskID)
		}
		if taskRound(t) != projectRound(*p) {
			return fmt.Errorf("task belongs to an archived round")
		}
		if !taskDependenciesReady(*p, t, map[string]bool{}) {
			return fmt.Errorf("dependency changed; inspect the new result before accepting this task")
		}
		if t.Status != "submitted" || len(t.Followups) > 0 {
			return fmt.Errorf("only submitted work can be accepted; task is %s", t.Status)
		}
		t.Status = "accepted"
		t.Evidence = evidence
		t.UpdatedAt = time.Now().UTC()
		p.Tasks[taskID] = t
		return nil
	})
}
func (s *TeamProjectStore) setWorkflowPaused(id string, paused bool) error {
	return s.mutateProject(id, func(p *TeamProject) error {
		if !roundAcceptsWork(*p) {
			return errProjectEnded
		}
		p.Paused = paused
		p.WorkflowEpoch++
		if paused {
			for key, t := range p.Tasks {
				if taskActive(t.Status) || len(t.Followups) > 0 {
					cancelTaskFollowups(&t)
					t.Status = "stopped"
					t.UpdatedAt = time.Now().UTC()
					p.Tasks[key] = t
				}
			}
			for key, r := range p.Reports {
				if taskActive(r.Status) {
					r.Status = "stopped"
					r.UpdatedAt = time.Now().UTC()
					p.Reports[key] = r
				}
			}
		}
		return nil
	})
}
func taskForSession(p TeamProject, bot, key string) *TeamTask {
	var newest *TeamTask
	for _, t := range p.Tasks {
		if t.Assignee == bot && t.SessionKey == key && key != "" && (newest == nil || t.UpdatedAt.After(newest.UpdatedAt)) {
			copy := t
			newest = &copy
		}
	}
	return newest
}

func archivedTaskSession(p TeamProject, bot, key string) bool {
	for _, task := range p.Tasks {
		if task.Assignee != bot || task.SessionKey == key {
			continue
		}
		for _, attempt := range task.History {
			if key != "" && attempt.SessionKey == key {
				return true
			}
		}
	}
	return false
}
func projectDisplayMode(p *TeamProject) string {
	if p == nil {
		return ""
	}
	if p.DisplayMode == "" {
		return "compact"
	}
	return p.DisplayMode
}

// A downstream result is valid only for the exact accepted inputs it used.
func taskDependenciesReady(p TeamProject, t TeamTask, seen map[string]bool) bool {
	if seen[t.ID] {
		return false
	}
	seen[t.ID] = true
	defer delete(seen, t.ID)
	for _, id := range t.DependsOn {
		d, ok := p.Tasks[id]
		if !ok || d.Status != "accepted" || len(d.Followups) > 0 {
			return false
		}
		if attempt := t.DependencyAttempts[id]; attempt != 0 && attempt != d.Attempt {
			return false
		}
		if !taskDependenciesReady(p, d, seen) {
			return false
		}
	}
	return true
}

func cancelTaskFollowups(t *TeamTask) {
	if len(t.Followups) == 0 {
		return
	}
	if t.CancelledFollowups == nil {
		t.CancelledFollowups = map[string]string{}
	}
	for id, body := range t.Followups {
		t.CancelledFollowups[id] = body
	}
	t.Followups = nil
}
