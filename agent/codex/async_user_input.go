package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func (s *appServerSession) handleAsyncUserInput(item map[string]any) bool {
	if item["delivery"] != "async" {
		return false
	}
	var input []struct {
		Title   string   `json:"title"`
		Options []string `json:"options"`
	}
	raw, err := json.Marshal(item["questions"])
	if err == nil {
		err = json.Unmarshal(raw, &input)
	}
	var questions []core.UserQuestion
	if err == nil {
		for _, in := range input {
			if strings.TrimSpace(in.Title) == "" {
				continue
			}
			q := core.UserQuestion{Question: in.Title}
			for _, option := range in.Options {
				q.Options = append(q.Options, core.UserQuestionOption{Label: option})
			}
			questions = append(questions, q)
		}
	}
	id, _ := item["id"].(string)
	if id != "" && len(questions) > 0 {
		s.emit(core.Event{Type: core.EventUserInputRequest, RequestID: id, Questions: questions})
	} else if text, _ := item["text"].(string); strings.TrimSpace(text) != "" {
		s.emit(core.Event{Type: core.EventText, Content: text})
	}
	return true
}

func (s *appServerSession) SteerUserInput(prompt string) (bool, error) {
	return s.steerUserInput(prompt, 5*time.Second)
}

func (s *appServerSession) steerUserInput(prompt string, timeout time.Duration) (bool, error) {
	s.steerMu.Lock()
	defer s.steerMu.Unlock()
	if strings.TrimSpace(prompt) == "" {
		return false, fmt.Errorf("%w: empty input", core.ErrSteerRejected)
	}
	s.stateMu.Lock()
	turnID := s.currentTurn
	s.stateMu.Unlock()
	if turnID == "" {
		return false, nil
	}
	params := map[string]any{
		"threadId":       s.CurrentSessionID(),
		"expectedTurnId": turnID,
		"input": []map[string]any{{
			"type": "text", "text": prompt, "text_elements": []any{},
		}},
	}
	var response struct {
		TurnID string `json:"turnId"`
	}
	if err := s.requestWithTimeout("turn/steer", params, &response, timeout); err != nil {
		var rejected *rpcError
		if errors.As(err, &rejected) && (rejected.Code == -32600 || rejected.Code == -32601 || rejected.Code == -32602) {
			return false, fmt.Errorf("%w: %s", core.ErrSteerRejected, rejected)
		}
		return false, fmt.Errorf("codex app-server steering delivery unconfirmed: %w", err)
	}
	if response.TurnID != turnID {
		return false, fmt.Errorf("codex app-server steering returned turn %q, want %q", response.TurnID, turnID)
	}
	return true, nil
}

var _ core.UserInputSteerer = (*appServerSession)(nil)
