package codex

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Resume recovers a subscription, but notifications from the disconnected
// interval are not guaranteed to replay. Reconcile only our unfinished turn.
func (s *managedSession) reconcileReconnect(snapshot managedSnapshot, expected string, rpc *daemonRPC) error {
	if expected == "" {
		return nil
	}
	s.mu.Lock()
	done := s.completed[expected]
	s.mu.Unlock()
	if done {
		return nil
	}
	turn := findManagedTurn(snapshot.Thread.Turns, expected)
	if turn == nil {
		var response struct {
			Thread managedThread `json:"thread"`
		}
		if err := rpc.request(s.ctx, "thread/read", map[string]any{"threadId": snapshot.Thread.ID, "includeTurns": true}, &response); err != nil {
			return fmt.Errorf("reconcile disconnected turn %s: %w", expected, err)
		}
		if response.Thread.ID != snapshot.Thread.ID || !sameManagedCwd(s.agent.GetWorkDir(), response.Thread.Cwd) {
			return fmt.Errorf("reconnect reconciliation returned a different thread/workspace")
		}
		turn = findManagedTurn(response.Thread.Turns, expected)
	}
	if turn == nil {
		return fmt.Errorf("reconnect could not locate unfinished turn %s", expected)
	}
	switch turn.Status {
	case "inProgress":
		return s.recoverRunningTurnText(*turn)
	case "completed", "failed", "interrupted":
		var parts []string
		for _, item := range turn.Items {
			if item["type"] != "agentMessage" {
				continue
			}
			phase, _ := item["phase"].(string)
			text, _ := item["text"].(string)
			if text != "" && (phase == "final_answer" || phase == "") {
				parts = append(parts, text)
			}
		}
		content := strings.Join(parts, "\n\n")
		errorText := ""
		if turn.Error != nil {
			errorText = turn.Error.Message
		}
		return s.completeManagedTurn(turn.ID, turn.Status, errorText, &content)
	default:
		return fmt.Errorf("reconnect returned unknown state %q for unfinished turn %s", turn.Status, expected)
	}
}

func findManagedTurn(turns []managedTurn, id string) *managedTurn {
	for i := range turns {
		if turns[i].ID == id {
			return &turns[i]
		}
	}
	return nil
}

// Resume retains completed items even while their turn continues. Restore them
// through the normal item-ID deduplication and authoritative text repair path.
func (s *managedSession) recoverRunningTurnText(turn managedTurn) error {
	for _, item := range turn.Items {
		text, _ := item["text"].(string)
		id, _ := item["id"].(string)
		if item["type"] != "agentMessage" || text == "" || id == "" {
			continue
		}
		recovered := make(map[string]any, len(item))
		for key, value := range item {
			if key != "questions" { // Historical async prompts are not replayed.
				recovered[key] = value
			}
		}
		raw, err := json.Marshal(map[string]any{"turnId": turn.ID, "item": recovered})
		if err != nil {
			return fmt.Errorf("recover running turn text: %w", err)
		}
		if err := s.handleItem(daemonMessage{Method: "item/completed", Params: raw}); err != nil {
			return fmt.Errorf("recover running turn text: %w", err)
		}
	}
	return nil
}
