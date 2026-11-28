package core

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"
)

type mgmtBlockedReplyPlatform struct {
	stubPlatformEngine
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *mgmtBlockedReplyPlatform) Reply(context.Context, any, string) error {
	p.once.Do(func() { close(p.entered) })
	<-p.release
	return nil
}

func TestMgmt_SessionRuntimeDoesNotWaitForOtherSessionReply(t *testing.T) {
	_, ts, e := testManagementServer(t, "tok")
	a := e.sessions.GetOrCreateActive("test:a:user")
	b := e.sessions.GetOrCreateActive("test:b:user")
	p := &mgmtBlockedReplyPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}, entered: make(chan struct{}), release: make(chan struct{})}
	e.interactiveStates["test:a:user"] = &interactiveState{busySession: a, agentSession: &stubAgentSession{}}
	e.interactiveStates["test:b:user"] = &interactiveState{busySession: b, agentSession: &stubAgentSession{}, platform: p}
	gen, ok := b.TryLock()
	if !ok {
		t.Fatal("could not acquire execution lock")
	}
	defer b.Unlock(gen)
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.queueMessageForBusySession(p, &Message{SessionKey: "test:b:user", Content: "queued"}, "test:b:user")
	}()
	defer func() {
		close(p.release)
		<-done
	}()
	select {
	case <-p.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("queue acknowledgment did not reach the platform")
	}
	client := &http.Client{Timeout: time.Second}
	for _, suffix := range []string{"/" + a.ID, "/" + b.ID, ""} {
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/projects/test-project/sessions"+suffix, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer tok")
		resp, err := client.Do(req)
		if err != nil {
			t.Errorf("reading %q waited for an unrelated platform reply: %v", suffix, err)
			continue
		}
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing response for %q: %v", suffix, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d", resp.StatusCode)
		}
	}
	if !e.interactiveMu.TryLock() {
		t.Error("status read retained the project interaction lock")
	} else {
		e.interactiveMu.Unlock()
	}
}

type mgmtRelayAgentSession struct {
	stubAgentSession
	sent   chan struct{}
	events chan Event
}

func (s *mgmtRelayAgentSession) Send(string, string, []ImageAttachment, []FileAttachment) error {
	close(s.sent)
	return nil
}
func (s *mgmtRelayAgentSession) Events() <-chan Event { return s.events }

type mgmtRelayAgent struct {
	stubAgent
	session *mgmtRelayAgentSession
}

func (a *mgmtRelayAgent) StartSession(context.Context, string) (AgentSession, error) {
	return a.session, nil
}

func TestMgmt_SessionRuntimeRelayIsOutsideInteractiveObservation(t *testing.T) {
	_, ts, e := testManagementServer(t, "tok")
	agentSession := &mgmtRelayAgentSession{sent: make(chan struct{}), events: make(chan Event, 1)}
	e.agent = &mgmtRelayAgent{session: agentSession}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := e.HandleRelay(ctx, "source", "test:chat:user", "work")
		done <- err
	}()
	defer func() {
		agentSession.events <- Event{Type: EventResult, Content: "done"}
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("relay did not finish")
		}
		cancel()
	}()
	select {
	case <-agentSession.sent:
	case <-time.After(3 * time.Second):
		t.Fatal("relay did not send to the agent")
	}
	sessions := e.sessions.AllSessions()
	if len(sessions) != 1 {
		t.Fatalf("saved relay sessions = %d", len(sessions))
	}
	// The relay is executing, but it neither takes the execution lock nor
	// registers an interactive state. False/null are explicitly scoped values.
	assertMgmtSessionRuntime(t, ts.URL+"/api/v1/projects/test-project/sessions", sessions[0].ID, false, nil)
}

type mgmtRuntimeRoutePlatform struct{ stubPlatformEngine }

func (*mgmtRuntimeRoutePlatform) ReconstructReplyCtx(key string) (any, error) { return key, nil }

func TestMgmt_SessionRuntimeSendDoesNotRequireLiveAgent(t *testing.T) {
	_, ts, e := testManagementServer(t, "tok")
	p := &mgmtRuntimeRoutePlatform{stubPlatformEngine{n: "test"}}
	e.platforms = []Platform{p}
	key := "test:chat:user"
	s := e.sessions.GetOrCreateActive(key)
	url := ts.URL + "/api/v1/projects/test-project"
	assertMgmtSessionRuntime(t, url+"/sessions", s.ID, false, nil)
	detail := mgmtGet(t, url+"/sessions/"+s.ID, "tok")
	var before struct {
		Live *bool `json:"live"`
	}
	if err := json.Unmarshal(detail.Data, &before); err != nil || before.Live == nil || *before.Live {
		t.Fatalf("expected live:false before outbound delivery, got %s", detail.Data)
	}
	r := mgmtPost(t, url+"/send", "tok", map[string]string{"session_key": key, "message": "notification"})
	if !r.OK {
		t.Fatalf("send without an interactive agent: %s", r.Error)
	}
	if sent := p.getSent(); len(sent) != 1 || sent[0] != "notification" {
		t.Errorf("platform messages = %v", sent)
	}
	e.interactiveMu.Lock()
	count := len(e.interactiveStates)
	e.interactiveMu.Unlock()
	if count != 0 {
		t.Error("outbound platform delivery created an interactive agent state")
	}
}
