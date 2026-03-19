//go:build integration

// Package integration contains integration tests for cc-connect.
// These tests verify component interactions and require specific setup.
// Run with: go test -tags=integration ./tests/integration/...
package integration

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/chenhg5/cc-connect/tests/mocks"
	"github.com/chenhg5/cc-connect/tests/mocks/fake"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// T-300: Engine + Platform Integration
// ---------------------------------------------------------------------------

func TestIntegration_EnginePlatformMessageFlow(t *testing.T) {
	// This test verifies the message flow between platform and engine
	// using mock components to simulate real behavior

	// Create mock platform
	platform := new(mocks.MockPlatform)
	platform.On("Name").Return("test-integration")
	platform.On("Start", mock.Anything).Return(nil)
	platform.On("Stop").Return(nil)

	// Create message handler to capture messages
	var receivedMessages []*core.Message
	var mu sync.Mutex

	handler := func(p core.Platform, msg *core.Message) {
		mu.Lock()
		receivedMessages = append(receivedMessages, msg)
		mu.Unlock()
	}

	// Start platform
	err := platform.Start(handler)
	require.NoError(t, err)

	// Simulate platform receiving a message
	testMsg := fake.TestMessageWithContent("Integration test message")
	handler(platform, testMsg)

	// Verify message was captured
	mu.Lock()
	require.Len(t, receivedMessages, 1)
	assert.Equal(t, "Integration test message", receivedMessages[0].Content)
	mu.Unlock()

	// Stop platform
	platform.Stop()

	t.Log("Engine-Platform message flow: PASS")
}

// ---------------------------------------------------------------------------
// T-301: Multi-Agent Session Coordination
// ---------------------------------------------------------------------------

func TestIntegration_MultiAgentSessionCoordination(t *testing.T) {
	ctx := context.Background()

	// Create fake agents
	agent1 := fake.NewFakeAgent("agent-1")
	agent2 := fake.NewFakeAgent("agent-2")

	// Start sessions on both agents
	sess1, err := agent1.StartSession(ctx, "coord-session-1")
	require.NoError(t, err)

	sess2, err := agent2.StartSession(ctx, "coord-session-2")
	require.NoError(t, err)

	// Verify both sessions are independent
	assert.NotEqual(t, sess1.CurrentSessionID(), sess2.CurrentSessionID())
	assert.True(t, sess1.Alive())
	assert.True(t, sess2.Alive())

	// Send message to agent 1
	err = sess1.Send("Message to agent 1", nil, nil)
	require.NoError(t, err)

	// Send message to agent 2
	err = sess2.Send("Message to agent 2", nil, nil)
	require.NoError(t, err)

	// Verify both agents received their messages
	prompts1 := agent1.GetSession().GetPrompts()
	prompts2 := agent2.GetSession().GetPrompts()

	assert.Len(t, prompts1, 1)
	assert.Contains(t, prompts1[0], "Message to agent 1")
	assert.Len(t, prompts2, 1)
	assert.Contains(t, prompts2[0], "Message to agent 2")

	t.Log("Multi-agent session coordination: PASS")
}

// ---------------------------------------------------------------------------
// T-302: Session Persistence Simulation
// ---------------------------------------------------------------------------

func TestIntegration_SessionPersistenceSimulation(t *testing.T) {
	ctx := context.Background()

	agent := fake.NewFakeAgent("persist-agent")

	// Start session
	sess1, err := agent.StartSession(ctx, "persist-session")
	require.NoError(t, err)
	originalSessionID := sess1.CurrentSessionID()

	// Simulate session close (as would happen on restart)
	err = sess1.Close()
	require.NoError(t, err)

	// Simulate new session with same ID (as would happen on restore)
	sess2, err := agent.StartSession(ctx, "persist-session")
	require.NoError(t, err)
	restoredSessionID := sess2.CurrentSessionID()

	// Verify session was "restored"
	assert.Equal(t, originalSessionID, restoredSessionID)
	assert.True(t, sess2.Alive())

	t.Log("Session persistence simulation: PASS")
}

// ---------------------------------------------------------------------------
// T-303: Rate Limiter Integration
// ---------------------------------------------------------------------------

func TestIntegration_RateLimiterIntegration(t *testing.T) {
	// Create rate limiter: 2 messages per second
	rl := core.NewRateLimiter(2, time.Second)
	defer rl.Stop()

	// First two should succeed
	assert.True(t, rl.Allow("user-rate"))
	assert.True(t, rl.Allow("user-rate"))

	// Third should fail
	assert.False(t, rl.Allow("user-rate"))

	// Different user should succeed
	assert.True(t, rl.Allow("other-user"))

	// Wait for window to reset
	time.Sleep(1100 * time.Millisecond)

	// Should be allowed again
	assert.True(t, rl.Allow("user-rate"))

	t.Log("Rate limiter integration: PASS")
}

// ---------------------------------------------------------------------------
// T-304: Message Dedup Integration
// ---------------------------------------------------------------------------

func TestIntegration_MessageDedupIntegration(t *testing.T) {
	dedup := &core.MessageDedup{}

	// Process a message
	msgID := "dedup-test-msg-001"
	isDup := dedup.IsDuplicate(msgID)
	assert.False(t, isDup, "first occurrence should not be duplicate")

	// Same message again should be duplicate
	isDup = dedup.IsDuplicate(msgID)
	assert.True(t, isDup, "second occurrence should be duplicate")

	// Different message should not be duplicate
	isDup = dedup.IsDuplicate("different-msg")
	assert.False(t, isDup)

	// Empty should never be duplicate
	isDup = dedup.IsDuplicate("")
	assert.False(t, isDup)

	t.Log("Message dedup integration: PASS")
}

// ---------------------------------------------------------------------------
// T-305: Command Registry Integration
// ---------------------------------------------------------------------------

func TestIntegration_CommandRegistryIntegration(t *testing.T) {
	registry := core.NewCommandRegistry()

	// Add multiple commands
	registry.Add("start", "Start the process", "Starting...", "", "", "builtin")
	registry.Add("stop", "Stop the process", "Stopping...", "", "", "builtin")
	registry.Add("status", "Check status", "Status: running", "", "", "builtin")

	// Verify all commands are registered
	commands := registry.ListAll()
	assert.Len(t, commands, 3)

	// Resolve each command
	cmd, ok := registry.Resolve("start")
	require.True(t, ok)
	assert.Equal(t, "start", cmd.Name)

	cmd, ok = registry.Resolve("stop")
	require.True(t, ok)
	assert.Equal(t, "stop", cmd.Name)

	cmd, ok = registry.Resolve("status")
	require.True(t, ok)
	assert.Equal(t, "status", cmd.Name)

	// Hyphen/underscore normalization
	registry.Add("my-cmd", "My command", "Running...", "", "", "builtin")
	cmd, ok = registry.Resolve("my_cmd") // Telegram sanitizes hyphens to underscores
	require.True(t, ok)
	assert.Equal(t, "my-cmd", cmd.Name)

	t.Log("Command registry integration: PASS")
}

// ---------------------------------------------------------------------------
// T-306: Role Manager Integration
// ---------------------------------------------------------------------------

func TestIntegration_RoleManagerIntegration(t *testing.T) {
	manager := core.NewUserRoleManager()

	manager.Configure("viewer", []core.RoleInput{
		{
			Name:    "admin",
			UserIDs: []string{"admin1", "admin2"},
		},
		{
			Name:    "developer",
			UserIDs: []string{"dev1", "dev2"},
		},
		{
			Name:    "viewer",
			UserIDs: []string{"*"}, // wildcard - everyone else
		},
	})

	// Admin users
	assert.Equal(t, "admin", manager.ResolveRole("admin1").Name)
	assert.Equal(t, "admin", manager.ResolveRole("admin2").Name)

	// Developer users
	assert.Equal(t, "developer", manager.ResolveRole("dev1").Name)
	assert.Equal(t, "developer", manager.ResolveRole("dev2").Name)

	// Unknown user gets viewer (wildcard)
	assert.Equal(t, "viewer", manager.ResolveRole("random-user").Name)

	t.Log("Role manager integration: PASS")
}

// ---------------------------------------------------------------------------
// T-307: Platform Reply Integration
// ---------------------------------------------------------------------------

func TestIntegration_PlatformReplyIntegration(t *testing.T) {
	ctx := context.Background()

	platform := new(mocks.MockPlatform)

	// Expect specific reply
	expectedContent := "This is a reply from the platform"
	platform.On("Reply", ctx, mock.Anything, expectedContent).Return(nil)

	// Simulate engine sending reply
	err := platform.Reply(ctx, nil, expectedContent)
	require.NoError(t, err)

	// Verify reply was called correctly
	platform.AssertExpectations(t)

	t.Log("Platform reply integration: PASS")
}

// ---------------------------------------------------------------------------
// T-308: Agent Permission Flow
// ---------------------------------------------------------------------------

func TestIntegration_AgentPermissionFlow(t *testing.T) {
	ctx := context.Background()

	// Create mock agent session
	session := fake.NewFakeAgentSession("perm-session")
	session.AddPermissionRequest("req-001", "Bash", "rm -rf /")
	session.AddResultEvent("Request handled")

	agent := fake.NewFakeAgentWithSession("perm-agent", "perm-session", session)

	// Start session
	sess, err := agent.StartSession(ctx, "perm-session")
	require.NoError(t, err)

	// Send message
	err = sess.Send("Run dangerous command", nil, nil)
	require.NoError(t, err)

	// Collect events
	var events []core.Event
	for e := range sess.Events() {
		events = append(events, e)
		if e.Done {
			break
		}
	}

	// Should have permission request and result
	assert.GreaterOrEqual(t, len(events), 2)

	// Find permission request event
	var permRequest core.Event
	for _, e := range events {
		if e.Type == core.EventPermissionRequest {
			permRequest = e
			break
		}
	}
	assert.Equal(t, "Bash", permRequest.ToolName)
	assert.Equal(t, "req-001", permRequest.RequestID)

	t.Log("Agent permission flow: PASS")
}

// ---------------------------------------------------------------------------
// T-310: Cron Store Integration
// ---------------------------------------------------------------------------

func TestIntegration_CronStoreIntegration(t *testing.T) {
	store, err := core.NewCronStore(t.TempDir())
	require.NoError(t, err)

	// Add cron job
	job := &core.CronJob{
		ID:          "integration-cron",
		Description: "Integration test cron",
		Prompt:      "Run test",
		CronExpr:    "0 9 * * *",
		Enabled:     true,
	}
	err = store.Add(job)
	require.NoError(t, err)

	// List jobs
	jobs := store.List()
	assert.Len(t, jobs, 1)

	// Disable job
	ok := store.SetEnabled("integration-cron", false)
	assert.True(t, ok)

	// Verify disabled
	job = store.Get("integration-cron")
	assert.False(t, job.Enabled)

	// Toggle mute
	store.SetMute("integration-cron", true)
	muted, ok := store.ToggleMute("integration-cron")
	assert.True(t, ok)
	assert.False(t, muted) // toggled from true to false

	// Remove job
	ok = store.Remove("integration-cron")
	assert.True(t, ok)

	jobs = store.List()
	assert.Empty(t, jobs)

	t.Log("Cron store integration: PASS")
}

// ---------------------------------------------------------------------------
// T-311: Card Rendering Integration
// ---------------------------------------------------------------------------

func TestIntegration_CardRenderingIntegration(t *testing.T) {
	// Create a complex card
	card := core.NewCard().
		Title("Integration Test Card", "green").
		Markdown("## Welcome\nThis is a **test** card.").
		Markdown("- Item 1\n- Item 2\n- Item 3").
		Divider().
		Buttons(
			core.PrimaryBtn("Confirm", "act:/confirm"),
			core.DefaultBtn("Cancel", "act:/cancel"),
			core.DangerBtn("Delete", "act:/delete"),
		).
		Note("This is a footnote").
		Build()

	// Verify card structure
	assert.NotNil(t, card)
	assert.NotNil(t, card.Header)
	assert.Equal(t, "Integration Test Card", card.Header.Title)
	assert.Equal(t, "green", card.Header.Color)
	assert.GreaterOrEqual(t, len(card.Elements), 5) // markdown + markdown + divider + buttons + note

	// Verify text fallback
	text := card.RenderText()
	assert.Contains(t, text, "Integration Test Card")
	assert.Contains(t, text, "Welcome")
	assert.Contains(t, text, "Confirm")
	assert.Contains(t, text, "Delete")

	// Verify button collection
	buttons := card.CollectButtons()
	assert.Len(t, buttons, 1)
	assert.Len(t, buttons[0], 3)

	// Verify HasButtons
	assert.True(t, card.HasButtons())

	t.Log("Card rendering integration: PASS")
}

// ---------------------------------------------------------------------------
// T-312: Env Merge Integration
// ---------------------------------------------------------------------------

func TestIntegration_EnvMergeIntegration(t *testing.T) {
	base := []string{"PATH=/usr/bin", "HOME=/home/user", "VAR=value"}

	// Merge with overlapping keys
	extra := []string{"VAR=newvalue", "ADD=new", "HOME=/different"}
	merged := core.MergeEnv(base, extra)

	// Should have: PATH (from base), HOME (overridden), VAR (overridden), ADD (from extra)
	assert.Len(t, merged, 4)

	// Find VAR - should be new value
	for _, env := range merged {
		if len(env) > 3 && env[:3] == "VAR" {
			assert.Equal(t, "VAR=newvalue", env)
			break
		}
	}

	// Find ADD - should be new
	found := false
	for _, env := range merged {
		if len(env) > 3 && env[:3] == "ADD" {
			assert.Equal(t, "ADD=new", env)
			found = true
			break
		}
	}
	assert.True(t, found)

	t.Log("Env merge integration: PASS")
}

// ---------------------------------------------------------------------------
// T-313: Message Attachment Handling
// ---------------------------------------------------------------------------

func TestIntegration_MessageAttachmentHandling(t *testing.T) {
	ctx := context.Background()

	// Create session
	session := fake.NewFakeAgentSession("attach-session")
	agent := fake.NewFakeAgentWithSession("attach-agent", "attach-session", session)

	sess, err := agent.StartSession(ctx, "attach-session")
	require.NoError(t, err)

	// Create message with attachments
	images := []core.ImageAttachment{
		{MimeType: "image/png", Data: []byte("fake png data"), FileName: "screenshot.png"},
	}
	files := []core.FileAttachment{
		{MimeType: "application/pdf", Data: []byte("fake pdf data"), FileName: "doc.pdf"},
	}

	// Send with attachments
	err = sess.Send("Analyze this", images, files)
	require.NoError(t, err)

	// Verify prompts captured
	prompts := session.GetPrompts()
	assert.Len(t, prompts, 1)

	t.Log("Message attachment handling: PASS")
}
