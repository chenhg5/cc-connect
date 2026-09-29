package core

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type workspaceEffortAgent struct {
	stubModelModeAgent
	effortMu sync.Mutex
	levels   []string
	effort   string
}

func (a *workspaceEffortAgent) AvailableReasoningEfforts() []string { return a.levels }
func (a *workspaceEffortAgent) GetReasoningEffort() string {
	a.effortMu.Lock()
	defer a.effortMu.Unlock()
	return a.effort
}
func (a *workspaceEffortAgent) SetReasoningEffort(v string) {
	a.effortMu.Lock()
	defer a.effortMu.Unlock()
	a.effort = v
}

func TestReasoningCards_MultiWorkspaceIsolation(t *testing.T) {
	global := &workspaceEffortAgent{levels: []string{"global"}, effort: "global"}
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := NewEngine("test", global, []Platform{p}, "", LangEnglish)
	e.SetMultiWorkspace(t.TempDir(), filepath.Join(t.TempDir(), "bindings.json"))
	var wg sync.WaitGroup
	start := make(chan struct{})
	for n := 0; n < 2; n++ {
		dir := normalizeWorkspacePath(t.TempDir())
		channel := fmt.Sprintf("C-reasoning-%d", n)
		key := "feishu:" + channel + ":u1"
		e.workspaceBindings.Bind("project:test", channel, "chan", dir)
		ws := e.workspacePool.GetOrCreate(dir)
		level := fmt.Sprintf("workspace%d", n)
		a := &workspaceEffortAgent{levels: []string{level}}
		ws.agent = a
		ws.sessions = NewSessionManager(filepath.Join(t.TempDir(), "sessions.json"))
		s := ws.sessions.GetOrCreateActive(key)
		s.SetAgentSessionID("workspace-session", "test")
		s.AddHistory("user", "workspace history")
		ws.sessions.Save()
		gs := e.sessions.GetOrCreateActive(key)
		gs.SetAgentSessionID("global-session", "test")
		gs.AddHistory("user", "global history")
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			cards := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
			for i := 0; i < 10; i++ {
				e.cmdReasoning(cards, &Message{SessionKey: key, ReplyCtx: "ctx"}, nil)
				initial := fmt.Sprintf("%+v", cards.repliedCards[len(cards.repliedCards)-1].Elements)
				if !strings.Contains(initial, level) || strings.Contains(initial, "global") {
					t.Errorf("wrong initial card: %s", initial)
				}

				for _, action := range []string{"nav:/reasoning", "act:/reasoning 1"} {
					card := e.handleCardNav(action, key)
					got := fmt.Sprintf("%+v", card.Elements)
					if !strings.Contains(got, level) || strings.Contains(got, "global") {
						t.Errorf("wrong %s card: %s", action, got)
					}
				}
			}
			if a.GetReasoningEffort() != level {
				t.Errorf("workspace effort not updated")
			}
			if s.GetAgentSessionID() != "" {
				t.Errorf("workspace session not reset")
			}
			if len(s.GetHistory(10)) != 0 {
				t.Error("workspace history not cleared")
			}
			saved := NewSessionManager(ws.sessions.storePath).GetOrCreateActive(key)
			if saved.GetAgentSessionID() != "" || len(saved.GetHistory(10)) != 0 {
				t.Error("workspace reset not saved")
			}
			if len(gs.GetHistory(10)) != 1 {
				t.Error("global history changed")
			}
			if gs.GetAgentSessionID() != "global-session" {
				t.Errorf("global session reset")
			}
		}()
	}
	close(start)
	wg.Wait()
	if global.GetReasoningEffort() != "global" {
		t.Fatal("global agent changed")
	}
}

func TestReasoning_EmptyEfforts(t *testing.T) {
	a := &workspaceEffortAgent{effort: "unchanged"}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "test:user", ReplyCtx: "ctx"}
	for _, args := range [][]string{nil, {"high"}} {
		e.cmdReasoning(p, msg, args)
		got := p.sent[len(p.sent)-1]
		if got != e.i18n.T(MsgReasoningNotSupported) {
			t.Fatalf("empty list reply: %s", got)
		}
	}
	for _, action := range []string{"nav:/reasoning", "act:/reasoning 1"} {
		card := e.handleCardNav(action, msg.SessionKey)
		got := fmt.Sprintf("%+v", card.Elements)
		if strings.Contains(got, "<>") || !strings.Contains(got, e.i18n.T(MsgReasoningNotSupported)) {
			t.Fatalf("empty list card: %s", got)
		}
		for _, el := range card.Elements {
			if _, ok := el.(CardSelect); ok {
				t.Fatal("empty select rendered")
			}
		}
	}
	if a.GetReasoningEffort() != "unchanged" {
		t.Fatal("empty list action changed agent")
	}
}
