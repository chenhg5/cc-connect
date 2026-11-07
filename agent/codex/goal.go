package codex

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func (a *Agent) SupportsGoals() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.backend == "app_server"
}

func cloneGoal(g *core.Goal) *core.Goal {
	if g == nil {
		return nil
	}
	x := *g
	if g.TokenBudget != nil {
		n := *g.TokenBudget
		x.TokenBudget = &n
	}
	return &x
}

func (s *appServerSession) GoalActive() bool {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return s.goal != nil && s.goal.Status == "active"
}

func (s *appServerSession) goalRequest(method string, params map[string]any) (*core.Goal, error) {
	threadID := s.CurrentSessionID()
	if threadID == "" {
		return nil, fmt.Errorf("native goal requires a thread")
	}
	params["threadId"] = threadID
	s.runtimeMu.RLock()
	revision := s.goalRevision
	s.runtimeMu.RUnlock()
	var response struct {
		Goal    *core.Goal `json:"goal"`
		Cleared *bool      `json:"cleared"`
	}
	if err := s.requestWithTimeout(method, params, &response, 10*time.Second); err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	if method == "thread/goal/clear" && response.Cleared == nil {
		return nil, fmt.Errorf("%s returned no clear result", method)
	}
	if response.Goal != nil && response.Goal.ThreadID != threadID {
		return nil, fmt.Errorf("%s returned a goal for another thread", method)
	}
	if method == "thread/goal/set" && response.Goal == nil {
		return nil, fmt.Errorf("%s returned no goal", method)
	}
	s.runtimeMu.Lock()
	if s.goalRevision == revision {
		s.goal = cloneGoal(response.Goal)
	}
	s.runtimeMu.Unlock()
	return response.Goal, nil
}

func (s *appServerSession) GetGoal() (*core.Goal, error) {
	return s.goalRequest("thread/goal/get", map[string]any{})
}

func (s *appServerSession) SetGoal(u core.GoalUpdate) (*core.Goal, error) {
	params := map[string]any{"origin": "user"}
	if u.Objective != nil {
		params["objective"] = *u.Objective
	}
	if u.Status != "" {
		params["status"] = u.Status
	}
	if u.TokenBudget != nil {
		params["tokenBudget"] = *u.TokenBudget
	} else if u.ClearTokenBudget {
		params["tokenBudget"] = nil
	}
	g, err := s.goalRequest("thread/goal/set", params)
	if err == nil && u.Status == "paused" {
		err = s.interruptGoalTurn()
	}
	return g, err
}

func (s *appServerSession) ClearGoal() error {
	s.runtimeMu.RLock()
	hadGoal := s.goal != nil
	s.runtimeMu.RUnlock()
	if _, err := s.goalRequest("thread/goal/clear", map[string]any{"origin": "user"}); err != nil {
		return err
	}
	if !hadGoal {
		return nil
	}
	return s.interruptGoalTurn()
}

// CancelTurn pauses a native goal before interrupting: a later resume must not
// silently restart work that the user explicitly stopped.
func (s *appServerSession) CancelTurn() error {
	if s.GoalActive() {
		_, err := s.SetGoal(core.GoalUpdate{Status: "paused"})
		return err
	}
	return s.interruptGoalTurn()
}

func (s *appServerSession) interruptGoalTurn() error {
	s.stateMu.Lock()
	turnID := s.currentTurn
	s.stateMu.Unlock()
	if turnID == "" {
		return nil
	}
	var response struct{}
	return s.requestWithTimeout("turn/interrupt", map[string]any{"threadId": s.CurrentSessionID(), "turnId": turnID}, &response, 10*time.Second)
}

func (s *appServerSession) handleGoalNotification(method string, raw json.RawMessage) {
	var n struct {
		ThreadID string     `json:"threadId"`
		Goal     *core.Goal `json:"goal"`
	}
	if json.Unmarshal(raw, &n) != nil || n.ThreadID == "" || n.ThreadID != s.CurrentSessionID() {
		return
	}
	if method == "thread/goal/cleared" {
		n.Goal = nil
	} else if n.Goal == nil {
		return
	}
	if n.Goal != nil && (n.Goal.ThreadID != n.ThreadID || strings.TrimSpace(n.Goal.Status) == "") {
		return
	}
	s.runtimeMu.Lock()
	old := s.goal
	changed := old == nil != (n.Goal == nil)
	if old != nil && n.Goal != nil {
		changed = old.Status != n.Goal.Status || old.Objective != n.Goal.Objective || (old.TokenBudget == nil) != (n.Goal.TokenBudget == nil)
		if old.TokenBudget != nil && n.Goal.TokenBudget != nil {
			changed = changed || *old.TokenBudget != *n.Goal.TokenBudget
		}
	}
	s.goal = cloneGoal(n.Goal)
	s.goalRevision++
	s.runtimeMu.Unlock()
	if changed {
		s.emit(core.Event{Type: core.EventGoal, Goal: cloneGoal(n.Goal), SessionID: n.ThreadID})
	}
}

func (s *appServerSession) steerGoalInput(input []map[string]any, turnID string) error {
	var response struct {
		TurnID string `json:"turnId"`
	}
	params := map[string]any{"threadId": s.CurrentSessionID(), "expectedTurnId": turnID, "input": input}
	if err := s.request("turn/steer", params, &response); err != nil {
		return err
	}
	if response.TurnID != turnID {
		return fmt.Errorf("goal steering returned unexpected turn %q", response.TurnID)
	}
	return nil
}
