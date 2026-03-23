//go:build integration

package integration

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/stretchr/testify/require"
)

const integrationSharedAgentName = "integration-shared-routing-agent"

var (
	registerIntegrationSharedAgentOnce sync.Once
	integrationMessageSeq              uint64
)

type integrationRoutingAgent struct {
	workDir string
}

func (a *integrationRoutingAgent) Name() string { return integrationSharedAgentName }

func (a *integrationRoutingAgent) StartSession(_ context.Context, sessionID string) (core.AgentSession, error) {
	return newIntegrationRoutingSession(sessionID, a.workDir), nil
}

func (a *integrationRoutingAgent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	return nil, nil
}

func (a *integrationRoutingAgent) Stop() error { return nil }

type integrationRoutingSession struct {
	mu        sync.RWMutex
	sessionID string
	workDir   string
	alive     bool
	events    <-chan core.Event
}

func newIntegrationRoutingSession(sessionID, workDir string) *integrationRoutingSession {
	ch := make(chan core.Event)
	close(ch)
	return &integrationRoutingSession{
		sessionID: sessionID,
		workDir:   workDir,
		alive:     true,
		events:    ch,
	}
}

func (s *integrationRoutingSession) Send(prompt string, _ []core.ImageAttachment, _ []core.FileAttachment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.alive {
		return io.ErrClosedPipe
	}

	ch := make(chan core.Event, 1)
	ch <- core.Event{
		Type:    core.EventResult,
		Content: fmt.Sprintf("workspace=%s prompt=%s", s.workDir, prompt),
		Done:    true,
	}
	close(ch)
	s.events = ch
	return nil
}

func (s *integrationRoutingSession) RespondPermission(string, core.PermissionResult) error {
	return nil
}

func (s *integrationRoutingSession) Events() <-chan core.Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.events
}

func (s *integrationRoutingSession) CurrentSessionID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessionID
}

func (s *integrationRoutingSession) Alive() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.alive
}

func (s *integrationRoutingSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alive = false
	return nil
}

type integrationPlatform struct {
	mu           sync.Mutex
	name         string
	channelNames map[string]string
	handler      core.MessageHandler
	outputs      []string
}

func newIntegrationPlatform(name string, channelNames map[string]string) *integrationPlatform {
	return &integrationPlatform{
		name:         name,
		channelNames: channelNames,
		outputs:      make([]string, 0),
	}
}

func (p *integrationPlatform) Name() string { return p.name }

func (p *integrationPlatform) Start(handler core.MessageHandler) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.handler = handler
	return nil
}

func (p *integrationPlatform) Reply(_ context.Context, _ any, content string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.outputs = append(p.outputs, content)
	return nil
}

func (p *integrationPlatform) Send(_ context.Context, _ any, content string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.outputs = append(p.outputs, content)
	return nil
}

func (p *integrationPlatform) Stop() error { return nil }

func (p *integrationPlatform) ResolveChannelName(channelID string) (string, error) {
	if name, ok := p.channelNames[channelID]; ok {
		return name, nil
	}
	return "", fmt.Errorf("unknown channel %q", channelID)
}

func (p *integrationPlatform) Emit(msg *core.Message) {
	p.mu.Lock()
	handler := p.handler
	p.mu.Unlock()
	if handler == nil {
		panic("integrationPlatform.Emit called before Start")
	}
	handler(p, msg)
}

func (p *integrationPlatform) ClearOutputs() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.outputs = p.outputs[:0]
}

func (p *integrationPlatform) Outputs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]string, len(p.outputs))
	copy(cp, p.outputs)
	return cp
}

func (p *integrationPlatform) WaitForOutputContaining(t *testing.T, needle string) string {
	t.Helper()
	var matched string
	require.Eventually(t, func() bool {
		for _, output := range p.Outputs() {
			if strings.Contains(output, needle) {
				matched = output
				return true
			}
		}
		return false
	}, 3*time.Second, 20*time.Millisecond)
	return matched
}

func registerIntegrationSharedAgent() {
	registerIntegrationSharedAgentOnce.Do(func() {
		core.RegisterAgent(integrationSharedAgentName, func(opts map[string]any) (core.Agent, error) {
			workDir, _ := opts["work_dir"].(string)
			return &integrationRoutingAgent{workDir: workDir}, nil
		})
	})
}

func newIntegrationEngine(t *testing.T, projectName string, platform *integrationPlatform, baseDir, bindingStore, sessionStore string) *core.Engine {
	t.Helper()
	registerIntegrationSharedAgent()

	engine := core.NewEngine(
		projectName,
		&integrationRoutingAgent{workDir: baseDir},
		[]core.Platform{platform},
		sessionStore,
		core.LangEnglish,
	)
	engine.SetAdminFrom("admin")
	engine.SetMultiWorkspace(baseDir, bindingStore)
	require.NoError(t, engine.Start())
	t.Cleanup(func() {
		_ = engine.Stop()
	})
	return engine
}

func integrationMessage(platformName, channelID, userID, content string) *core.Message {
	seq := atomic.AddUint64(&integrationMessageSeq, 1)
	chatName := channelID
	return &core.Message{
		SessionKey: fmt.Sprintf("%s:%s:%s", platformName, channelID, userID),
		Platform:   platformName,
		MessageID:  fmt.Sprintf("msg-%d", seq),
		UserID:     userID,
		UserName:   userID,
		ChatName:   chatName,
		Content:    content,
		ReplyCtx:   fmt.Sprintf("reply-%d", seq),
	}
}

func TestIntegration_SharedWorkspaceBindingLiveSyncAcrossProjects(t *testing.T) {
	baseDir := t.TempDir()
	bindingStore := filepath.Join(t.TempDir(), "workspace_bindings.json")
	channelID := "shared-channel"
	channelNames := map[string]string{channelID: "shared-channel"}
	platformName := "shared-platform"

	sharedDir := filepath.Join(baseDir, "shared-workspace")
	require.NoError(t, os.MkdirAll(sharedDir, 0o755))

	platformA := newIntegrationPlatform(platformName, channelNames)
	engineA := newIntegrationEngine(t, "project-a", platformA, baseDir, bindingStore, filepath.Join(t.TempDir(), "project-a-sessions.json"))
	_ = engineA

	platformB := newIntegrationPlatform(platformName, channelNames)
	engineB := newIntegrationEngine(t, "project-b", platformB, baseDir, bindingStore, filepath.Join(t.TempDir(), "project-b-sessions.json"))
	_ = engineB

	platformA.Emit(integrationMessage(platformA.Name(), channelID, "user-a", "/workspace shared bind shared-workspace"))
	platformA.WaitForOutputContaining(t, "Shared workspace bound")

	platformA.ClearOutputs()
	platformA.Emit(integrationMessage(platformA.Name(), channelID, "user-a", "hello from project a"))
	platformA.WaitForOutputContaining(t, sharedDir)

	platformB.ClearOutputs()
	platformB.Emit(integrationMessage(platformB.Name(), channelID, "user-b", "hello from project b"))
	platformB.WaitForOutputContaining(t, sharedDir)

	platformA.ClearOutputs()
	platformA.Emit(integrationMessage(platformA.Name(), channelID, "user-a", "/workspace shared unbind"))
	platformA.WaitForOutputContaining(t, "Shared workspace unbound")

	platformB.ClearOutputs()
	platformB.Emit(integrationMessage(platformB.Name(), channelID, "user-b", "after shared unbind"))
	platformB.WaitForOutputContaining(t, "No workspace found for this channel")
}

func TestIntegration_ProjectWorkspaceOverridesSharedAcrossProjects(t *testing.T) {
	baseDir := t.TempDir()
	bindingStore := filepath.Join(t.TempDir(), "workspace_bindings.json")
	channelID := "override-channel"
	channelNames := map[string]string{channelID: "override-channel"}
	platformName := "shared-platform"

	sharedDir := filepath.Join(baseDir, "shared-workspace")
	projectBDir := filepath.Join(baseDir, "project-b-workspace")
	require.NoError(t, os.MkdirAll(sharedDir, 0o755))
	require.NoError(t, os.MkdirAll(projectBDir, 0o755))

	platformA := newIntegrationPlatform(platformName, channelNames)
	engineA := newIntegrationEngine(t, "project-a", platformA, baseDir, bindingStore, filepath.Join(t.TempDir(), "project-a-sessions.json"))
	_ = engineA

	platformB := newIntegrationPlatform(platformName, channelNames)
	engineB := newIntegrationEngine(t, "project-b", platformB, baseDir, bindingStore, filepath.Join(t.TempDir(), "project-b-sessions.json"))
	_ = engineB

	platformA.Emit(integrationMessage(platformA.Name(), channelID, "user-a", "/workspace shared bind shared-workspace"))
	platformA.WaitForOutputContaining(t, "Shared workspace bound")

	platformB.ClearOutputs()
	platformB.Emit(integrationMessage(platformB.Name(), channelID, "user-b", "/workspace bind project-b-workspace"))
	platformB.WaitForOutputContaining(t, "Workspace bound")

	platformA.ClearOutputs()
	platformA.Emit(integrationMessage(platformA.Name(), channelID, "user-a", "route project a"))
	platformA.WaitForOutputContaining(t, sharedDir)

	platformB.ClearOutputs()
	platformB.Emit(integrationMessage(platformB.Name(), channelID, "user-b", "route project b"))
	platformB.WaitForOutputContaining(t, projectBDir)
}

func TestIntegration_ProjectWorkspaceRouteUsesAbsolutePath(t *testing.T) {
	baseDir := t.TempDir()
	bindingStore := filepath.Join(t.TempDir(), "workspace_bindings.json")
	channelID := "route-channel"
	channelNames := map[string]string{channelID: "route-channel"}

	routedDir := filepath.Join(t.TempDir(), "routed workspace")
	require.NoError(t, os.MkdirAll(routedDir, 0o755))

	platform := newIntegrationPlatform("proj-route-platform", channelNames)
	engine := newIntegrationEngine(t, "project-route", platform, baseDir, bindingStore, filepath.Join(t.TempDir(), "project-route-sessions.json"))
	_ = engine

	platform.Emit(integrationMessage(platform.Name(), channelID, "user-route", "/workspace route "+routedDir))
	platform.WaitForOutputContaining(t, "Workspace routed")

	platform.ClearOutputs()
	platform.Emit(integrationMessage(platform.Name(), channelID, "user-route", "hello routed workspace"))
	platform.WaitForOutputContaining(t, routedDir)
}

func TestIntegration_SharedWorkspaceRouteLiveSyncAcrossProjects(t *testing.T) {
	baseDir := t.TempDir()
	bindingStore := filepath.Join(t.TempDir(), "workspace_bindings.json")
	channelID := "shared-route-channel"
	channelNames := map[string]string{channelID: "shared-route-channel"}
	platformName := "shared-platform"

	routedDir := filepath.Join(t.TempDir(), "shared routed workspace")
	require.NoError(t, os.MkdirAll(routedDir, 0o755))

	platformA := newIntegrationPlatform(platformName, channelNames)
	engineA := newIntegrationEngine(t, "project-shared-route-a", platformA, baseDir, bindingStore, filepath.Join(t.TempDir(), "project-shared-route-a-sessions.json"))
	_ = engineA

	platformB := newIntegrationPlatform(platformName, channelNames)
	engineB := newIntegrationEngine(t, "project-shared-route-b", platformB, baseDir, bindingStore, filepath.Join(t.TempDir(), "project-shared-route-b-sessions.json"))
	_ = engineB

	platformA.Emit(integrationMessage(platformA.Name(), channelID, "user-a", "/workspace shared route "+routedDir))
	platformA.WaitForOutputContaining(t, "Shared workspace routed")

	platformA.ClearOutputs()
	platformA.Emit(integrationMessage(platformA.Name(), channelID, "user-a", "hello from shared route a"))
	platformA.WaitForOutputContaining(t, routedDir)

	platformB.ClearOutputs()
	platformB.Emit(integrationMessage(platformB.Name(), channelID, "user-b", "hello from shared route b"))
	platformB.WaitForOutputContaining(t, routedDir)
}
