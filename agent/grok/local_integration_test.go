package grok

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGrokLocalIntegration exercises the installed and logged-in Grok CLI.
// It is intentionally opt-in: ordinary unit tests must never access Grok or
// the network.
func TestGrokLocalIntegration(t *testing.T) {
	if os.Getenv("CC_CONNECT_GROK_INTEGRATION") != "1" {
		t.Skip("set CC_CONNECT_GROK_INTEGRATION=1 to run the local Grok integration test")
	}

	workDir := t.TempDir()
	rawAgent, err := New(map[string]any{
		"work_dir":     workDir,
		"cmd":          "grok",
		"mode":         "yolo",
		"timeout_mins": 3,
	})
	require.NoError(t, err)
	agent := rawAgent.(*Agent)

	testCtx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()
	session, err := agent.StartSession(testCtx, "")
	require.NoError(t, err)

	var sessionID string
	deleted := false
	defer func() {
		_ = session.Close()
		if deleted {
			return
		}
		if sessionID == "" {
			sessionID = session.CurrentSessionID()
		}
		if sessionID == "" {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if err := agent.DeleteSession(cleanupCtx, sessionID); err != nil {
			t.Errorf("clean up Grok integration session %q: %v", sessionID, err)
		}
	}()

	const firstMarker = "CC_CONNECT_GROK_FIRST_TURN_OK"
	firstPrompt := "Reply with exactly " + firstMarker + " and nothing else. Do not use any tools."
	require.NoError(t, session.Send(firstPrompt, "integration-turn-1", nil, nil))
	firstEvents, err := collectLocalGrokTurn(session.Events(), 4*time.Minute)
	require.NoError(t, err)
	firstResult := terminalResult(t, firstEvents)
	assert.Equal(t, firstMarker, strings.TrimSpace(firstResult.Content))

	sessionID = session.CurrentSessionID()
	require.NotEmpty(t, sessionID)
	assert.Equal(t, sessionID, firstResult.SessionID)
	require.Eventually(t, func() bool {
		return agent.ValidateSessionID(context.Background(), sessionID)
	}, 15*time.Second, 250*time.Millisecond, "new Grok session was not discoverable in its workspace")

	const secondMarker = "CC_CONNECT_GROK_SECOND_TURN_OK"
	secondPrompt := "You must use the run_terminal_command tool exactly once to execute pwd. " +
		"Do not infer or merely describe the directory. After the tool succeeds, reply with exactly " + secondMarker + " and nothing else."
	require.NoError(t, session.Send(secondPrompt, "integration-turn-2", nil, nil))
	secondEvents, err := collectLocalGrokTurn(session.Events(), 4*time.Minute)
	require.NoError(t, err)
	secondResult := terminalResult(t, secondEvents)
	assert.Equal(t, secondMarker, strings.TrimSpace(secondResult.Content))
	assert.Equal(t, sessionID, session.CurrentSessionID(), "second turn must resume the exact first-turn session")
	assert.Equal(t, sessionID, secondResult.SessionID)

	toolUse, toolResult := matchingTerminalToolEvents(t, secondEvents)
	assert.Equal(t, "run_terminal_command", toolUse.ToolName)
	assert.Contains(t, toolUse.ToolInput, "pwd")
	assert.Equal(t, toolUse.RequestID, toolResult.RequestID)
	require.NotNil(t, toolResult.ToolSuccess)
	assert.True(t, *toolResult.ToolSuccess)
	require.NotNil(t, toolResult.ToolExitCode)
	assert.Zero(t, *toolResult.ToolExitCode)
	resolvedWorkDir, err := filepath.EvalSymlinks(workDir)
	require.NoError(t, err)
	assert.Contains(t, toolResult.ToolResult, resolvedWorkDir)

	var history []core.HistoryEntry
	require.Eventually(t, func() bool {
		var historyErr error
		history, historyErr = agent.GetSessionHistory(context.Background(), sessionID, 0)
		return historyErr == nil &&
			historyContains(history, "user", firstMarker) &&
			historyContains(history, "assistant", firstMarker) &&
			historyContains(history, "user", secondMarker) &&
			historyContains(history, "assistant", secondMarker)
	}, 15*time.Second, 250*time.Millisecond, "persisted history did not contain both resumed turns")

	require.NoError(t, session.Close())
	deleteCtx, deleteCancel := context.WithTimeout(context.Background(), 30*time.Second)
	require.NoError(t, agent.DeleteSession(deleteCtx, sessionID))
	deleteCancel()
	deleted = true
	require.Eventually(t, func() bool {
		return !agent.ValidateSessionID(context.Background(), sessionID)
	}, 10*time.Second, 250*time.Millisecond, "deleted Grok session remained discoverable")
}

// TestGrokLocalNativeSlashIntegration verifies the non-ACP path end to end:
// Grok advertises a project command through its exact initialized slash table,
// then the same literal slash invocation is handled by a normal Agent session.
func TestGrokLocalNativeSlashIntegration(t *testing.T) {
	if os.Getenv("CC_CONNECT_GROK_INTEGRATION") != "1" {
		t.Skip("set CC_CONNECT_GROK_INTEGRATION=1 to run the local Grok integration test")
	}

	workDir := t.TempDir()
	commandDir := filepath.Join(workDir, ".grok", "commands")
	require.NoError(t, os.MkdirAll(commandDir, 0o755))
	commandPath := filepath.Join(commandDir, "cc-connect-native-probe.md")
	require.NoError(t, os.WriteFile(commandPath, []byte("Reply with exactly CC_CONNECT_NATIVE_PROBE_$ARGUMENTS and nothing else. Do not use tools.\n"), 0o644))

	rawAgent, err := New(map[string]any{
		"work_dir":     workDir,
		"cmd":          "grok",
		"mode":         "yolo",
		"timeout_mins": 3,
	})
	require.NoError(t, err)
	agent := rawAgent.(*Agent)

	discoveryCtx, discoveryCancel := context.WithTimeout(context.Background(), 30*time.Second)
	commands, err := agent.NativeSlashCommands(discoveryCtx)
	discoveryCancel()
	require.NoError(t, err)
	found := false
	for _, command := range commands {
		if command.Target == "cc-connect-native-probe" {
			found = true
			break
		}
	}
	require.True(t, found, "Grok init did not advertise the project native command")

	testCtx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	session, err := agent.StartSession(testCtx, "")
	require.NoError(t, err)
	var sessionID string
	defer func() {
		_ = session.Close()
		if sessionID == "" {
			sessionID = session.CurrentSessionID()
		}
		if sessionID != "" {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cleanupCancel()
			_ = agent.DeleteSession(cleanupCtx, sessionID)
		}
	}()

	require.NoError(t, session.Send("/cc-connect-native-probe marker-123", "native-probe", nil, nil))
	events, err := collectLocalGrokTurn(session.Events(), 3*time.Minute)
	require.NoError(t, err)
	result := terminalResult(t, events)
	assert.Equal(t, "CC_CONNECT_NATIVE_PROBE_marker-123", strings.TrimSpace(result.Content))
	sessionID = session.CurrentSessionID()
	require.NotEmpty(t, sessionID)
}

func collectLocalGrokTurn(events <-chan core.Event, timeout time.Duration) ([]core.Event, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	var collected []core.Event
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return collected, fmt.Errorf("Grok event channel closed before a terminal event")
			}
			collected = append(collected, event)
			switch event.Type {
			case core.EventResult:
				return collected, nil
			case core.EventError:
				return collected, fmt.Errorf("Grok turn failed: %w", event.Error)
			}
		case <-timer.C:
			return collected, fmt.Errorf("timed out after %s waiting for Grok turn", timeout)
		}
	}
}

func terminalResult(t *testing.T, events []core.Event) core.Event {
	t.Helper()
	require.NotEmpty(t, events)
	result := events[len(events)-1]
	require.Equal(t, core.EventResult, result.Type)
	require.True(t, result.Done)
	return result
}

func matchingTerminalToolEvents(t *testing.T, events []core.Event) (core.Event, core.Event) {
	t.Helper()
	for _, toolUse := range events {
		if toolUse.Type != core.EventToolUse || toolUse.ToolName != "run_terminal_command" || !strings.Contains(toolUse.ToolInput, "pwd") {
			continue
		}
		for _, toolResult := range events {
			if toolResult.Type == core.EventToolResult && toolResult.RequestID == toolUse.RequestID {
				return toolUse, toolResult
			}
		}
	}
	t.Fatalf("missing matching run_terminal_command ToolUse/ToolResult events: %+v", events)
	return core.Event{}, core.Event{}
}

func historyContains(history []core.HistoryEntry, role, content string) bool {
	for _, entry := range history {
		if entry.Role == role && strings.Contains(entry.Content, content) {
			return true
		}
	}
	return false
}
