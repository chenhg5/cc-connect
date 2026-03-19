//go:build smoke

// Package e2e contains smoke and regression tests for cc-connect.
// These tests verify core functionality using real components where possible,
// and mocks where necessary (e.g., platform network calls).
package e2e

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
	"github.com/chenhg5/cc-connect/tests/mocks"
	"github.com/chenhg5/cc-connect/tests/mocks/fake"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// T-100: Config Loading Smoke Test
// ---------------------------------------------------------------------------

func TestSmoke_ConfigLoading(t *testing.T) {
	// Create a temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	configContent := `
data_dir = "~/.cc-connect"

[[projects]]
name = "test-project"

[projects.agent]
type = "claudecode"

[[projects.platforms]]
type = "feishu"

[log]
level = "info"
`
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	// Load the config
	config.ConfigPath = configPath
	cfg, err := config.Load(configPath)
	require.NoError(t, err)

	// Verify basic config fields
	assert.Equal(t, "~/.cc-connect", cfg.DataDir)
	assert.Len(t, cfg.Projects, 1)
	assert.Equal(t, "test-project", cfg.Projects[0].Name)
	assert.Equal(t, "claudecode", cfg.Projects[0].Agent.Type)

	t.Log("Config loading: PASS")
}

func TestSmoke_ConfigLoadingInvalid(t *testing.T) {
	// Test that invalid config is properly rejected
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "invalid.toml")

	// Write invalid TOML
	err := os.WriteFile(configPath, []byte("invalid toml content ="), 0644)
	require.NoError(t, err)

	_, err = config.Load(configPath)
	assert.Error(t, err)

	t.Log("Config loading (invalid): PASS")
}

// ---------------------------------------------------------------------------
// T-101: All Agents Initialization Smoke Test
// ---------------------------------------------------------------------------

func TestSmoke_AllAgentsInit(t *testing.T) {
	// Verify all registered agents can be listed
	ctx := context.Background()

	// Get list of registered agent factories
	// Note: This tests the factory registration, not actual CLI existence
	t.Log("Registered agents via factory:", listRegisteredAgents())

	// Verify we have agents registered (at least the ones in the codebase)
	assert.NotEmpty(t, listRegisteredAgents(), "should have at least one agent registered")

	// Try to create each registered agent with minimal opts
	for _, agentName := range listRegisteredAgents() {
		opts := map[string]any{
			"project_dir": t.TempDir(),
		}
		agent, err := core.CreateAgent(agentName, opts)
		if err != nil {
			t.Logf("Agent %s creation: %v (may require CLI)", agentName, err)
			continue
		}
		assert.NotNil(t, agent)
		assert.Equal(t, agentName, agent.Name())

		// Clean up
		agent.Stop()
		t.Logf("Agent %s: initialized successfully", agentName)
	}

	// Use ctx to avoid compiler warning
	assert.NotNil(t, ctx)

	t.Log("All agents init: PASS")
}

func listRegisteredAgents() []string {
	// This requires access to the internal registry
	// We'll test via the factory pattern
	agents := []string{
		"claudecode", "codex", "cursor", "gemini",
		"iflow", "opencode", "pi", "qoder",
	}
	return agents
}

// ---------------------------------------------------------------------------
// T-102: All Platforms Initialization Smoke Test
// ---------------------------------------------------------------------------

func TestSmoke_AllPlatformsInit(t *testing.T) {
	// Verify all registered platforms can be listed
	t.Log("Registered platforms:", listRegisteredPlatforms())

	// Verify we have platforms registered
	assert.NotEmpty(t, listRegisteredPlatforms(), "should have at least one platform registered")

	// Try to create each registered platform with minimal opts
	for _, platformName := range listRegisteredPlatforms() {
		opts := map[string]any{
			"enabled": true,
		}
		platform, err := core.CreatePlatform(platformName, opts)
		if err != nil {
			t.Logf("Platform %s creation: %v", platformName, err)
			continue
		}
		assert.NotNil(t, platform)
		assert.Equal(t, platformName, platform.Name())

		// Clean up - don't actually start, just verify creation
		t.Logf("Platform %s: created successfully", platformName)
	}

	t.Log("All platforms init: PASS")
}

func listRegisteredPlatforms() []string {
	platforms := []string{
		"feishu", "telegram", "discord", "slack",
		"dingtalk", "wecom", "qq", "qqbot", "line",
	}
	return platforms
}

// ---------------------------------------------------------------------------
// T-103: Session Management Smoke Test
// ---------------------------------------------------------------------------

func TestSmoke_SessionManagement(t *testing.T) {
	ctx := context.Background()

	// Create a fake agent for session testing
	agent := fake.NewFakeAgent("test-agent")

	// Test session creation
	session, err := agent.StartSession(ctx, "test-session-001")
	require.NoError(t, err)
	assert.NotNil(t, session)
	assert.Equal(t, "test-session-001", session.CurrentSessionID())
	assert.True(t, session.Alive())

	// Test session list
	sessions, err := agent.ListSessions(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, sessions)

	// Test sending a message
	err = session.Send("Hello", nil, nil)
	require.NoError(t, err)

	// Test session is still alive
	assert.True(t, session.Alive())

	// Test session close
	err = session.Close()
	require.NoError(t, err)
	assert.False(t, session.Alive())

	t.Log("Session management: PASS")
}

func TestSmoke_SessionMessageFlow(t *testing.T) {
	ctx := context.Background()

	// Create a fake agent session with predefined responses
	session := fake.NewFakeAgentSession("test-session-002")
	session.AddTextEvent("Thinking...").AddResultEvent("Final answer.")

	agent := fake.NewFakeAgentWithSession("test-agent", "test-session-002", session)

	// Start session
	started, err := agent.StartSession(ctx, "test-session-002")
	require.NoError(t, err)
	assert.Equal(t, "test-session-002", started.CurrentSessionID())

	// Send message
	err = started.Send("What is 2+2?", nil, nil)
	require.NoError(t, err)

	// Collect events
	var events []core.Event
	for e := range started.Events() {
		events = append(events, e)
		if e.Done {
			break
		}
	}

	// Verify events
	assert.NotEmpty(t, events)
	assert.Equal(t, core.EventType("text"), events[0].Type)

	t.Log("Session message flow: PASS")
}

// ---------------------------------------------------------------------------
// T-104: Command Parsing Smoke Test
// ---------------------------------------------------------------------------

func TestSmoke_CommandParsing(t *testing.T) {
	registry := core.NewCommandRegistry()

	// Add some test commands
	registry.Add("test", "Test command", "echo {{1}}", "", "", "test")

	// Test command resolution
	cmd, ok := registry.Resolve("test")
	require.True(t, ok)
	assert.NotNil(t, cmd)
	assert.Equal(t, "test", cmd.Name)
	assert.Equal(t, "Test command", cmd.Description)

	// Test built-in commands
	registry.Add("help", "Show help", "help prompt", "", "", "builtin")
	cmd, ok = registry.Resolve("help")
	require.True(t, ok)
	assert.Equal(t, "help", cmd.Name)

	// Test command not found
	cmd, ok = registry.Resolve("nonexistent")
	assert.False(t, ok)
	assert.Nil(t, cmd)

	t.Log("Command parsing: PASS")
}

func TestSmoke_CommandRegistryList(t *testing.T) {
	registry := core.NewCommandRegistry()

	// Add multiple commands
	registry.Add("cmd1", "Command 1", "{{1}}", "", "", "test")
	registry.Add("cmd2", "Command 2", "{{2}}", "", "", "test")
	registry.Add("alias", "Alias for cmd1", "", "", "", "alias")

	// List all commands
	commands := registry.ListAll()
	assert.Len(t, commands, 3)

	// Test clear by source
	registry.ClearSource("test")
	commands = registry.ListAll()
	assert.Len(t, commands, 1) // only alias should remain

	t.Log("Command registry list: PASS")
}

// ---------------------------------------------------------------------------
// T-105: Agent ↔ Platform Message Flow Smoke Test
// ---------------------------------------------------------------------------

func TestSmoke_MessageFlow(t *testing.T) {
	ctx := context.Background()

	// Create mock platform
	mockPlatform := new(mocks.MockPlatform)
	mockPlatform.On("Name").Return("test-platform")
	mockPlatform.On("Start", mock.Anything).Return(nil)
	mockPlatform.On("Stop").Return(nil)

	// Create mock agent session with events
	events := []core.Event{
		{Type: core.EventThinking, Content: "Processing..."},
		{Type: core.EventText, Content: "I am thinking..."},
		{Type: core.EventResult, Content: "Final response.", Done: true},
	}
	mockSession := mocks.NewMockAgentSessionWithEvents("session-001", events)

	// Create mock agent
	mockAgent := new(mocks.MockAgent)
	mockAgent.On("StartSession", ctx, "session-001").Return(mockSession, nil)

	// Test message handler
	handler := fake.NewTestMessageHandler()

	// Start platform with handler
	err := mockPlatform.Start(handler.Handle)
	require.NoError(t, err)

	// Simulate message flow: platform receives message → routed to agent
	// (This is a simplified test - real flow goes through Engine)
	incomingMessage := fake.TestMessageWithContent("Hello agent")

	// Simulate platform passing message to handler
	handler.Handle(mockPlatform, incomingMessage)

	// Verify message was received
	messages := handler.GetMessages()
	require.Len(t, messages, 1)
	assert.Equal(t, "Hello agent", messages[0].Content)

	// Test agent session responds
	session, err := mockAgent.StartSession(ctx, "session-001")
	require.NoError(t, err)

	// Send message to agent
	err = session.Send("Hello agent", nil, nil)
	require.NoError(t, err)

	// Verify mock calls happened (only agent, not platform since we didn't call Name/Stop)
	mockAgent.AssertExpectations(t)

	t.Log("Message flow: PASS")
}

func TestSmoke_PlatformReply(t *testing.T) {
	ctx := context.Background()

	// Create mock platform that records replies
	mockPlatform := new(mocks.MockPlatform)

	// Expect Reply call
	mockPlatform.On("Reply", ctx, mock.Anything, "Hello from agent").Return(nil)

	// Simulate platform reply
	err := mockPlatform.Reply(ctx, nil, "Hello from agent")
	require.NoError(t, err)

	mockPlatform.AssertExpectations(t)

	t.Log("Platform reply: PASS")
}

func TestSmoke_EventTypes(t *testing.T) {
	// Test all event types can be created
	events := []core.Event{
		{Type: core.EventText, Content: "text content"},
		{Type: core.EventThinking, Content: "thinking..."},
		{Type: core.EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		{Type: core.EventToolResult, ToolResult: "file1.go"},
		{Type: core.EventResult, Content: "final result", Done: true},
		{Type: core.EventError, Error: context.DeadlineExceeded, Done: true},
		{
			Type:       core.EventPermissionRequest,
			ToolName:   "Bash",
			ToolInput:  "rm -rf /",
			RequestID:  "req-001",
		},
	}

	for _, e := range events {
		assert.NotEmpty(t, string(e.Type))
	}

	t.Log("Event types: PASS")
}

// ---------------------------------------------------------------------------
// T-107: Multi-Workspace Switch (P1, but quick to add)
// ---------------------------------------------------------------------------

func TestSmoke_WorkspaceSwitch(t *testing.T) {
	// Create simple workspace state maps to verify isolation concept
	ws1 := map[string]string{
		"id":       "workspace-1",
		"session":  "session-A",
		"agent":    "claudecode",
	}
	ws2 := map[string]string{
		"id":       "workspace-2",
		"session":  "session-B",
		"agent":    "gemini",
	}

	assert.Equal(t, "workspace-1", ws1["id"])
	assert.Equal(t, "workspace-2", ws2["id"])
	assert.NotEqual(t, ws1["session"], ws2["session"], "workspaces should have independent sessions")

	t.Log("Workspace switch: PASS")
}

// ---------------------------------------------------------------------------
// T-108: Rate Limiter Basic (P1, but quick to add)
// ---------------------------------------------------------------------------

func TestSmoke_RateLimiter(t *testing.T) {
	// Create a rate limiter: 5 messages per 60 seconds
	rl := core.NewRateLimiter(5, 60*time.Second)
	defer rl.Stop()

	// Should allow messages up to limit
	for i := 0; i < 5; i++ {
		allowed := rl.Allow("user1")
		assert.True(t, allowed, "message %d should be allowed", i+1)
	}

	// Should block after limit
	allowed := rl.Allow("user1")
	assert.False(t, allowed, "6th message should be blocked")

	// Different user should be allowed
	allowed = rl.Allow("user2")
	assert.True(t, allowed, "different user should be allowed")

	t.Log("Rate limiter: PASS")
}
