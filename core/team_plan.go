package core

import (
	"fmt"
	"strings"
	"time"
)

// Planning is manager-owned durable state. It never publishes a message,
// reserves a member, or starts a model. Only dispatch can release planned work.
func (s *TeamProjectStore) planTask(projectID string, req TeamActionRequest) (TeamTask, error) {
	var task TeamTask
	err := s.mutateProject(projectID, func(p *TeamProject) error {
		if p.Paused || !roundAcceptsWork(*p) {
			return fmt.Errorf("project must be ready and unpaused to plan work")
		}
		if !stringSliceContains(p.Bots, req.To) || req.To == p.Manager {
			return fmt.Errorf("select a project member")
		}
		if strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Deliverable) == "" || strings.TrimSpace(req.Message) == "" {
			return fmt.Errorf("plan requires title, deliverable and instructions")
		}
		if req.TaskID != "" {
			var ok bool
			task, ok = p.Tasks[req.TaskID]
			if !ok || task.Status != "planned" {
				return fmt.Errorf("only planned work can be edited")
			}
		} else {
			// Repeated identical plans are idempotent, including CLI retries.
			for _, existing := range p.Tasks {
				if existing.Status == "planned" && existing.ManagerSessionKey == req.SessionKey && existing.Assignee == req.To && existing.Title == req.Title && existing.Instruction == req.Message && existing.Deliverable == req.Deliverable && strings.Join(existing.DependsOn, "\x00") == strings.Join(req.DependsOn, "\x00") {
					task = existing
					return nil
				}
			}
			p.TaskSequence++
			task.ID, task.Attempt = fmt.Sprintf("T%03d", p.TaskSequence), 1
		}
		for _, dep := range req.DependsOn {
			if _, ok := p.Tasks[dep]; !ok || dep == task.ID || taskDependsOn(*p, dep, task.ID, map[string]bool{}) {
				return fmt.Errorf("missing or cyclic dependency: %s", dep)
			}
		}
		task.Title, task.Assignee, task.Instruction = req.Title, req.To, req.Message
		task.Deliverable, task.DependsOn = req.Deliverable, req.DependsOn
		task.Round = projectRound(*p)
		task.Status, task.Epoch, task.UpdatedAt = "planned", p.WorkflowEpoch, time.Now().UTC()
		task.ManagerSessionKey = req.SessionKey
		task.RequesterID = p.Creator
		if origin, ok := p.ManagerContexts[req.SessionKey]; ok && origin.RequesterID != "" {
			task.RequesterID = origin.RequesterID
		} else if p.RequesterID != "" {
			task.RequesterID = p.RequesterID
		}
		if p.Tasks == nil {
			p.Tasks = map[string]TeamTask{}
		}
		p.Tasks[task.ID] = task
		return nil
	})
	return task, err
}

func taskDependsOn(p TeamProject, id, target string, seen map[string]bool) bool {
	if seen[id] {
		return false
	}
	seen[id] = true
	for _, dep := range p.Tasks[id].DependsOn {
		if dep == target || taskDependsOn(p, dep, target, seen) {
			return true
		}
	}
	return false
}

func (s *TeamProjectStore) cancelPlannedTask(projectID, taskID string) error {
	return s.updateTask(projectID, taskID, func(t *TeamTask) error {
		if t.Status != "planned" {
			return fmt.Errorf("only planned work can be cancelled; pause active work first")
		}
		t.Status = "cancelled"
		return nil
	})
}
