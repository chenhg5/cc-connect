package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

func TestBuildAgentChoicesUsesIndependentProfileAndReceivingDirectory(t *testing.T) {
	var received []map[string]any
	core.RegisterAgent("choice-wiring-test", func(opts map[string]any) (core.Agent, error) {
		received = append(received, opts)
		return &stubMainAgent{workDir: opts["work_dir"].(string)}, nil
	})
	project := config.ProjectConfig{Name: "receiver", AgentChoices: map[string]string{"second": "source"}, Agent: config.AgentConfig{Type: "choice-wiring-test", Options: map[string]any{"work_dir": "/original"}}}
	cfg := &config.Config{DataDir: t.TempDir(), Projects: []config.ProjectConfig{project, {Name: "source", RunAsUser: "worker", RunAsEnv: []string{"LANG"}, Agent: config.AgentConfig{Type: "choice-wiring-test", Options: map[string]any{"work_dir": "/source-dir", "model": "source-model", "cmd": "source-cli"}}}}}
	defaultName, choices := buildAgentChoices(cfg, project)
	if defaultName != "default" || len(choices) != 2 || len(received) != 0 {
		t.Fatalf("choices are not lazy/deterministic: %s %+v", defaultName, choices)
	}
	var candidate core.AgentChoice
	for _, c := range choices {
		if c.Name == "second" {
			candidate = c
		}
	}
	a, err := candidate.New("/receiving-conversation")
	if err != nil {
		t.Fatal(err)
	}
	b, err := candidate.New("/another-conversation")
	if err != nil {
		t.Fatal(err)
	}
	if a == b || len(received) != 2 {
		t.Fatal("agent adapter shared across conversations")
	}
	opts := received[0]
	for key, want := range map[string]string{"work_dir": "/receiving-conversation", "cc_project": "receiver", "model": "source-model", "cmd": "source-cli", "run_as_user": "worker"} {
		if opts[key] != want {
			t.Fatalf("%s=%v want %s", key, opts[key], want)
		}
	}
	if !reflect.DeepEqual(opts["run_as_env"], []string{"LANG"}) || cfg.Projects[1].Agent.Options["work_dir"] != "/source-dir" {
		t.Fatal("source configuration mutated")
	}
}

// This uses the real installed/authenticated CLIs but records delivery locally.
// It never sends a test message to a live messaging platform.
func TestLiveAgentSwitchNativeCLIs(t *testing.T) {
	if os.Getenv("CC_CONNECT_AGENT_SWITCH_LIVE") != "1" {
		t.Skip("set CC_CONNECT_AGENT_SWITCH_LIVE=1")
	}
	path := os.Getenv("CC_CONNECT_AGENT_SWITCH_CONFIG")
	if path == "" {
		t.Fatal("CC_CONNECT_AGENT_SWITCH_CONFIG is required")
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var project config.ProjectConfig
	for _, p := range cfg.Projects {
		if p.Name == "codex-worker" {
			project = p
		}
	}
	if project.Name == "" {
		t.Fatal("live receiver profile missing")
	}
	project.AgentChoices = map[string]string{"claude": "cc-worker", "codex": "codex-worker", "kimi": "kimi-worker"}
	wd := t.TempDir()
	opts := buildAgentOptions(t.TempDir(), project)
	opts["work_dir"] = wd
	base, err := core.CreateAgent(project.Agent.Type, opts)
	if err != nil {
		t.Fatal(err)
	}
	wireAgentProviders(base, project.Agent)
	p := &liveAgentSwitchPlatform{}
	e := core.NewEngine("agent-switch-live-test", base, []core.Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), core.LangChinese)
	defer func() { _ = e.Stop() }()
	name, choices := buildAgentChoices(cfg, project)
	e.SetAgentChoices(name, choices)
	e.OnPlatformReady(p)
	key := "live:chat:user"
	send := func(content string) {
		e.ReceiveMessage(p, &core.Message{SessionKey: key, Platform: p.Name(), UserID: "user", Content: content, ReplyCtx: key})
	}
	send("/agent")
	p.mu.Lock()
	if len(p.cards) == 0 {
		p.mu.Unlock()
		t.Fatal("/agent did not render a card")
	}
	list := p.cards[len(p.cards)-1]
	p.mu.Unlock()
	for _, alias := range []string{"claude", "codex", "kimi", "codex"} {
		var action string
		for _, el := range list.Elements {
			if row, ok := el.(core.CardListItem); ok && row.BtnValue == "act:/agent switch "+alias {
				action = row.BtnValue
			}
		}
		if action == "" {
			t.Fatalf("no card button for %s", alias)
		}
		card := p.nav(action, key)
		if !strings.Contains(card.RenderText(), "已新建会话") {
			t.Fatal(card.RenderText())
		}
		s := e.GetSessions().GetOrCreateActive(key)
		if s.GetAgentSessionID() != "" || s.HistoryLen() != 0 {
			t.Fatal("card switch reused previous context")
		}
		marker := "SWITCH_NATIVE_READY_" + alias + "_" + time.Now().Format("150405")
		send("Reply with exactly " + marker + ". Do not call tools or write files.")
		deadline := time.Now().Add(4 * time.Minute)
		ready := false
		for time.Now().Before(deadline) {
			p.mu.Lock()
			output := strings.Join(p.messages, "\n")
			p.mu.Unlock()
			if strings.Contains(output, marker) && !s.Busy() {
				ready = true
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !ready {
			p.mu.Lock()
			output := strings.Join(p.messages, "\n")
			p.mu.Unlock()
			t.Fatalf("%s did not produce expected native reply: %s", alias, output)
		}
		t.Logf("%s: actual CLI reply delivered locally; fresh native session=%s", alias, s.GetAgentSessionID())
	}
}

type liveAgentSwitchPlatform struct {
	mu       sync.Mutex
	messages []string
	cards    []*core.Card
	nav      core.CardNavigationHandler
}

func (*liveAgentSwitchPlatform) Name() string                    { return "live" }
func (*liveAgentSwitchPlatform) Start(core.MessageHandler) error { return nil }
func (*liveAgentSwitchPlatform) Stop() error                     { return nil }
func (p *liveAgentSwitchPlatform) Reply(_ context.Context, _ any, s string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.messages = append(p.messages, s)
	return nil
}
func (p *liveAgentSwitchPlatform) Send(ctx context.Context, r any, s string) error {
	return p.Reply(ctx, r, s)
}
func (p *liveAgentSwitchPlatform) ReplyCard(_ context.Context, _ any, c *core.Card) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cards = append(p.cards, c)
	return nil
}
func (p *liveAgentSwitchPlatform) SendCard(ctx context.Context, r any, c *core.Card) error {
	return p.ReplyCard(ctx, r, c)
}
func (p *liveAgentSwitchPlatform) SetCardNavigationHandler(h core.CardNavigationHandler) { p.nav = h }
