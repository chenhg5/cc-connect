package core

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSharedManagementSwitchValidatesBeforeReplacingObserver(t *testing.T) {
	env, _, first := newSharedSettingsEnv(t)
	second := env.e.sessions.NewSideSession("test:user", "Second")
	second.SetAgentSessionID("second", "shared-test")
	second.AddHistory("user", "SECOND HISTORY")
	w := httptest.NewRecorder()
	body := fmt.Sprintf(`{"session_key":"test:user","session_id":%q}`, second.ID)
	(&ManagementServer{}).handleProjectSessionSwitch(w, httptest.NewRequest("POST", "/switch", strings.NewReader(body)), env.e)
	if w.Code != 200 {
		t.Fatalf("switch: %d %s", w.Code, w.Body)
	}
	if first.Alive() {
		t.Fatal("old observer still alive")
	}
	as := env.e.agent.(*sharedSettingsTestAgent).connection("second")
	as.emit(Event{Type: EventText, Content: "SECOND OBSERVER LIVE", Metadata: map[string]any{"phase": "commentary"}})
	env.await("SECOND OBSERVER LIVE")
	bad := env.e.sessions.NewSideSession("test:user", "Bad")
	bad.SetAgentSessionID("bad", "shared-test")
	w = httptest.NewRecorder()
	body = fmt.Sprintf(`{"session_key":"test:user","session_id":%q}`, bad.ID)
	(&ManagementServer{}).handleProjectSessionSwitch(w, httptest.NewRequest("POST", "/switch", strings.NewReader(body)), env.e)
	if w.Code == 200 || !as.Alive() || env.e.sessions.GetOrCreateActive("test:user").GetAgentSessionID() != "second" {
		t.Fatal("failed switch replaced current observer")
	}
}
