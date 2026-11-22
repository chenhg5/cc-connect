package core

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestMgmt_SessionDetail_DaemonUnavailablePreservesLocalHistory(t *testing.T) {
	for _, localHistory := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "cached"}[localHistory], func(t *testing.T) {
			_, ts, e := testManagementServer(t, "tok")
			e.agent = &sharedCompatHistoryAgent{sharedSettingsTestAgent: &sharedSettingsTestAgent{}, historyErr: errors.New("daemon unavailable")}
			s := e.sessions.GetOrCreateActive("chat")
			s.SetAgentSessionID("thread", "test")
			if localHistory {
				s.AddHistory("user", "cached question")
			}
			r := mgmtGet(t, ts.URL+"/api/v1/projects/test-project/sessions/"+s.ID, "tok")
			if !r.OK {
				t.Fatalf("detail unavailable: %s", r.Error)
			}
			var data struct {
				History []HistoryEntry `json:"history"`
			}
			if err := json.Unmarshal(r.Data, &data); err != nil {
				t.Fatal(err)
			}
			if localHistory && (len(data.History) != 1 || data.History[0].Content != "cached question") {
				t.Fatalf("cached history lost: %+v", data)
			}
			if !localHistory && len(data.History) != 0 {
				t.Fatalf("unexpected history: %+v", data)
			}
		})
	}
}

func TestMgmt_SessionDetail_UsesWorkspaceHistoryAgent(t *testing.T) {
	_, ts, e := testManagementServer(t, "tok")
	e.agent = &sharedCompatHistoryAgent{sharedSettingsTestAgent: &sharedSettingsTestAgent{}, history: []HistoryEntry{{Content: "WRONG PROJECT HISTORY"}}}
	e.workspacePool = newWorkspacePool(time.Hour)
	ws := e.workspacePool.GetOrCreate("/workspace")
	ws.agent = &sharedCompatHistoryAgent{sharedSettingsTestAgent: &sharedSettingsTestAgent{}, history: []HistoryEntry{{Role: "user", Content: "WORKSPACE HISTORY"}}}
	ws.sessions = NewSessionManager("")
	s := ws.sessions.GetOrCreateActive("chat")
	s.SetAgentSessionID("thread", "test")
	r := mgmtGet(t, ts.URL+"/api/v1/projects/test-project/sessions/"+s.ID, "tok")
	if !r.OK {
		t.Fatalf("workspace detail unavailable: %s", r.Error)
	}
	var data struct {
		History    []HistoryEntry `json:"history"`
		SessionKey string         `json:"session_key"`
	}
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.History) != 1 || data.History[0].Content != "WORKSPACE HISTORY" || data.SessionKey != "chat" {
		t.Fatalf("wrong workspace history: %+v", data)
	}
}
