package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

type bootstrapReceiptPlatform struct {
	*stubPlatformEngine
	agent   *cujAgent
	started chan bool
}

func (p *bootstrapReceiptPlatform) ReportInputStarted(_ context.Context, reply any) {
	p.agent.mu.Lock()
	defer p.agent.mu.Unlock()
	p.started <- reply == "receipt-context" &&
		len(p.agent.sessions) == 1 && len(p.agent.sessions[0].getSentPrompts()) == 0
}

func TestBusyInputStartedBeforeBootstrappedTurn(t *testing.T) {
	// This test checks receipt ordering and prompt delivery, not persistence.
	// Disable persistence so asynchronous saves cannot race temp-directory cleanup.
	plat := &stubPlatformEngine{n: "test"}
	agent := &cujAgent{}
	engine := NewEngine("test", agent, []Platform{plat}, "", LangEnglish)
	env := &cujEnv{t: t, engine: engine, plat: plat, agent: agent}
	key := "test:alice"
	session := env.engine.sessions.GetOrCreateActive(key)
	session.AddHistory("user", "prior request")
	session.AddHistory("assistant", "prior result")
	session.SetAgentSessionID("previous-native-session", "previous-agent")
	env.engine.sessions.InvalidateForAgent(env.agent.Name())
	reporter := &bootstrapReceiptPlatform{
		stubPlatformEngine: env.plat,
		agent:              env.agent,
		started:            make(chan bool, 1),
	}
	env.engine.ReceiveMessage(reporter, &Message{
		SessionKey: key,
		Platform:   "test",
		MessageID:  "new-turn",
		UserID:     "alice",
		UserName:   "alice",
		Content:    "continue the project",
		ReplyCtx:   "receipt-context",
	})
	select {
	case beforeSend := <-reporter.started:
		if !beforeSend {
			t.Fatal("receipt did not identify the input before its native send")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("receipt missing on bootstrapped history send")
	}
	env.waitFor("successful bootstrap", 2*time.Second, func() bool {
		session.mu.Lock()
		defer session.mu.Unlock()
		return session.HistoryBootstrappedAgentType == env.agent.Name()
	})
	env.agent.mu.Lock()
	prompts := env.agent.sessions[0].getSentPrompts()
	env.agent.mu.Unlock()
	if len(prompts) != 1 || !strings.Contains(prompts[0], "prior result") ||
		strings.Count(prompts[0], "continue the project") != 1 {
		t.Fatalf("bootstrapped turn was lost or duplicated: %q", prompts)
	}
}
