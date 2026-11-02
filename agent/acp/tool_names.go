package acp

import (
	"encoding/json"
	"strings"
)

const toolNameCacheMaxEntries = 1000

type toolCallKey struct {
	sessionID  string
	toolCallID string
}

// resolveToolCallName merges the name from partial ACP updates. A result may
// omit title, so retain the last title (or initial kind) until the call ends.
func (s *acpSession) resolveToolCallName(params json.RawMessage) string {
	var wrap struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			Type       string `json:"sessionUpdate"`
			ToolCallID string `json:"toolCallId"`
			Title      string `json:"title"`
			Kind       string `json:"kind"`
			Status     string `json:"status"`
		} `json:"update"`
	}
	if json.Unmarshal(params, &wrap) != nil {
		return ""
	}
	u := wrap.Update
	if (u.Type != "tool_call" && u.Type != "tool_call_update") || u.ToolCallID == "" {
		return ""
	}
	if wrap.SessionID == "" {
		wrap.SessionID = s.currentACPSessionID()
	}
	key := toolCallKey{wrap.SessionID, u.ToolCallID}
	s.toolNameMu.Lock()
	defer s.toolNameMu.Unlock()
	name := u.Title
	if name == "" {
		name = s.toolNameByID[key]
	}
	if name == "" {
		name = u.Kind
	}
	status := strings.ToLower(strings.TrimSpace(u.Status))
	if status == "completed" || status == "failed" {
		delete(s.toolNameByID, key)
	} else if name != "" {
		if s.toolNameByID == nil {
			s.toolNameByID = make(map[toolCallKey]string)
		}
		// Bound incomplete calls too, in case the agent never sends a terminal update.
		if _, exists := s.toolNameByID[key]; !exists && len(s.toolNameByID) >= toolNameCacheMaxEntries {
			for k := range s.toolNameByID {
				delete(s.toolNameByID, k)
				if len(s.toolNameByID) <= toolNameCacheMaxEntries/2 {
					break
				}
			}
		}
		s.toolNameByID[key] = name
	}
	return name
}
