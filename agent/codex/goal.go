package codex

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

type goalContinuationOptions struct {
	Enabled bool
	Prompt  string
	Limit   int
}

func (cs *codexSession) GoalContinuationEnabled() bool { return cs.goalAutoContinue }

func (cs *codexSession) GoalStatus() (string, bool) {
	cs.contextMu.RLock()
	defer cs.contextMu.RUnlock()
	return cs.goalStatus, cs.goalKnown
}

func (cs *codexSession) GoalContinuationPrompt() string { return cs.goalContinuationPrompt }

func (cs *codexSession) GoalContinuationLimit() int { return cs.goalContinuationLimit }

func (cs *codexSession) refreshGoalStatusFromRollout() {
	cs.contextMu.Lock()
	cs.goalStatus, cs.goalKnown = "", false
	cs.contextMu.Unlock()
	sessionID := strings.TrimSpace(cs.CurrentSessionID())
	if sessionID == "" {
		return
	}
	cs.contextMu.RLock()
	cachedPath := cs.sessionFile
	cs.contextMu.RUnlock()
	home, err := resolveCodexHome(cs.extraEnv)
	if err != nil {
		slog.Warn("codex goal: unable to resolve CODEX_HOME; continuation disabled for this turn", "session_id", sessionID, "error", err)
		return
	}
	path := findSessionFileInCodexHome(home, sessionID)
	if path == "" {
		path = cachedPath
	}
	status, known, err := readGoalStatusFromRollout(path)
	if err != nil {
		slog.Warn("codex goal: unable to read rollout; continuation disabled for this turn", "session_id", sessionID, "path", path, "error", err)
		return
	}
	cs.contextMu.Lock()
	cs.goalStatus = status
	cs.goalKnown = known
	cs.contextMu.Unlock()
}

func readGoalStatusFromRollout(path string) (string, bool, error) {
	if strings.TrimSpace(path) == "" {
		return "", false, fmt.Errorf("goal rollout path is empty")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = f.Close() }()
	var status string
	var known bool
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		var entry struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil {
			continue
		}
		candidate, candidateKnown, cleared := parseGoalStatusRecord(entry.Type, entry.Payload)
		if cleared {
			status, known = "", false
		} else if candidateKnown {
			status, known = candidate, true
		}
	}
	if err := scanner.Err(); err != nil {
		return "", false, err
	}
	return status, known, nil
}

func parseGoalStatusRecord(recordType string, payload json.RawMessage) (status string, known, cleared bool) {
	var record struct {
		Type   string          `json:"type"`
		Goal   json.RawMessage `json:"goal"`
		Status string          `json:"status"`
		Output json.RawMessage `json:"output"`
	}
	if json.Unmarshal(payload, &record) != nil {
		return "", false, false
	}
	kind := record.Type
	if recordType == "thread/goal/updated" || recordType == "thread/goal/cleared" {
		kind = recordType
	}
	if recordType == "event_msg" || recordType == "notification" || recordType == "event" || recordType == "server_notification" || recordType == kind {
		switch kind {
		case "thread_goal_cleared", "threadGoalCleared", "thread/goal/cleared":
			return "", false, true
		case "thread_goal_updated", "threadGoalUpdated", "thread/goal/updated":
			if len(record.Goal) > 0 {
				return parseGoalSnapshot(record.Goal)
			}
			if record.Status != "" {
				return record.Status, true, false
			}
			return "", false, true // An unreadable update invalidates older active state.
		}
	}
	// Only tool outputs can contain serialized Goal snapshots. Never inspect
	// ordinary messages, tool inputs, or arbitrary nested objects for status.
	if recordType != "response_item" || (kind != "custom_tool_call_output" && kind != "function_call_output") {
		return "", false, false
	}
	var output string
	if json.Unmarshal(record.Output, &output) == nil {
		return parseGoalToolText(output)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(record.Output, &blocks) != nil {
		return "", false, false
	}
	for i := len(blocks) - 1; i >= 0; i-- {
		if blocks[i].Type == "input_text" || blocks[i].Type == "text" {
			if s, k, c := parseGoalToolText(blocks[i].Text); k || c {
				return s, k, c
			}
		}
	}
	return "", false, false
}

func parseGoalToolText(text string) (string, bool, bool) {
	var snapshot struct {
		Goal json.RawMessage `json:"goal"`
	}
	if json.Unmarshal([]byte(text), &snapshot) != nil || len(snapshot.Goal) == 0 {
		return "", false, false
	}
	return parseGoalSnapshot(snapshot.Goal)
}

func parseGoalSnapshot(raw json.RawMessage) (string, bool, bool) {
	var goal struct {
		Status string `json:"status"`
	}
	if string(raw) == "null" || json.Unmarshal(raw, &goal) != nil || goal.Status == "" {
		return "", false, true
	}
	// Preserve unknown status values: they replace any older active snapshot
	// and will fail the engine's exact active check.
	return goal.Status, true, false
}
