package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestWorkspaceAppServerUsesOneConnectionAndIsolatesThreads(t *testing.T) {
	agent, logPath := newWorkspaceFakeAppServerAgent(t)
	t.Cleanup(func() {
		if err := agent.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})

	workspace := fakeWorkspace(t, agent)
	firstPage, err := agent.ListNativeConversations(context.Background(), workspace, core.NativePageRequest{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(firstPage.Data) != 1 || firstPage.Data[0].ID != "thread-1" || firstPage.NextCursor != "page-2" {
		t.Fatalf("first thread page = %#v", firstPage)
	}
	secondPage, err := agent.ListNativeConversations(context.Background(), workspace, core.NativePageRequest{Cursor: firstPage.NextCursor, Limit: 1})
	if err != nil || len(secondPage.Data) != 1 || secondPage.Data[0].ID != "thread-2" || secondPage.NextCursor != "" {
		t.Fatalf("second thread page = %#v, %v", secondPage, err)
	}
	detail, err := agent.ReadNativeConversation(context.Background(), workspace, "thread-1")
	if err != nil || detail.Settings.Model != "gpt-test" || detail.DeepLink != "codex://threads/thread-1" {
		t.Fatalf("ReadNativeConversation() = %#v, %v", detail, err)
	}
	turns, err := agent.ListNativeTurns(context.Background(), workspace, "thread-1", core.NativePageRequest{})
	if err != nil || len(turns.Data) != 1 || turns.Data[0].ID != "turn-history" {
		t.Fatalf("ListNativeTurns() = %#v, %v", turns, err)
	}
	items, err := agent.ListNativeItems(context.Background(), workspace, "thread-1", "turn-history", core.NativePageRequest{})
	if err != nil || len(items.Data) != 2 {
		t.Fatalf("ListNativeItems() = %#v, %v", items, err)
	}
	firstItems, err := agent.ListNativeItems(context.Background(), workspace, "thread-1", "turn-history", core.NativePageRequest{Limit: 1})
	if err != nil || len(firstItems.Data) != 1 || firstItems.NextCursor == "" {
		t.Fatalf("ListNativeItems(first page) = %#v, %v", firstItems, err)
	}
	secondItems, err := agent.ListNativeItems(context.Background(), workspace, "thread-1", "turn-history", core.NativePageRequest{Cursor: firstItems.NextCursor, Limit: 1})
	if err != nil || len(secondItems.Data) != 1 || secondItems.NextCursor != "" {
		t.Fatalf("ListNativeItems(second page) = %#v, %v", secondItems, err)
	}
	created, err := agent.StartNativeConversation(context.Background(), workspace)
	if err != nil || created.Thread.ID != "thread-new" || created.Thread.Name != "thread-new" {
		t.Fatalf("StartNativeConversation() = %#v, %v", created, err)
	}

	sessionOneRaw, err := agent.StartSession(context.Background(), "legacy-1")
	if err != nil {
		t.Fatal(err)
	}
	sessionOneAgain, err := agent.StartSession(context.Background(), "legacy-1")
	if err != nil {
		t.Fatal(err)
	}
	if sessionOneRaw != sessionOneAgain {
		t.Fatal("the same thread did not reuse its logical session")
	}
	sessionTwo, err := agent.StartSession(context.Background(), "legacy-2")
	if err != nil {
		t.Fatal(err)
	}
	if err := sessionOneRaw.Send("first", "m1", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := sessionTwo.Send("second", "m2", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := collectAppServerText(t, sessionOneRaw.Events()); got != "reply:legacy-1:first" {
		t.Fatalf("thread-1 reply = %q", got)
	}
	if got := collectAppServerText(t, sessionTwo.Events()); got != "reply:legacy-2:second" {
		t.Fatalf("thread-2 reply = %q", got)
	}

	if err := sessionOneRaw.Send("approve", "m3", nil, nil); err != nil {
		t.Fatal(err)
	}
	var approval core.Event
	select {
	case approval = <-sessionOneRaw.Events():
	case <-time.After(2 * time.Second):
		t.Fatal("thread-1 did not receive approval")
	}
	if approval.Type != core.EventPermissionRequest || !strings.Contains(approval.ToolInput, "legacy-1") {
		t.Fatalf("approval = %#v", approval)
	}
	select {
	case event := <-sessionTwo.Events():
		t.Fatalf("thread-2 received thread-1 event: %#v", event)
	case <-time.After(50 * time.Millisecond):
	}
	if err := sessionOneRaw.RespondPermission(approval.RequestID, core.PermissionResult{Behavior: "allow"}); err != nil {
		t.Fatal(err)
	}
	if got := collectAppServerText(t, sessionOneRaw.Events()); got != "approved:legacy-1" {
		t.Fatalf("approved reply = %q", got)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(data)
	if strings.Count(logText, "initialize\n") != 1 {
		t.Fatalf("physical initialize count = %d, log:\n%s", strings.Count(logText, "initialize\n"), logText)
	}
	if !strings.Contains(logText, "transport:stdio://") {
		t.Fatalf("App Server did not use the only supported stdio transport, log:\n%s", logText)
	}
	if !strings.Contains(logText, "experimentalApi:true") {
		t.Fatalf("initialize did not enable experimentalApi, log:\n%s", logText)
	}
	if !strings.Contains(logText, "mcpOpenaiForm:true") {
		t.Fatalf("initialize did not enable OpenAI MCP form elicitation, log:\n%s", logText)
	}
	if !strings.Contains(logText, "optOutNotifications:0") {
		t.Fatalf("initialize still opts out of native events, log:\n%s", logText)
	}
	if strings.Count(logText, "thread/list\n") != 2 || !strings.Contains(logText, "approval-response:approval-legacy-1") {
		t.Fatalf("fake server log:\n%s", logText)
	}

	if err := sessionOneRaw.Send("hold", "m4", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := sessionOneRaw.Close(); err != nil {
		t.Fatal(err)
	}
	if !sessionTwo.Alive() {
		t.Fatal("closing one logical session closed another thread")
	}
	data, err = os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "turn/interrupt:legacy-1:turn-legacy-1") {
		t.Fatalf("logical close did not interrupt the active turn, log:\n%s", data)
	}
}

func TestStartNativeConversationDoesNotWaitForNoopSettingsNotification(t *testing.T) {
	agent, _ := newWorkspaceFakeAppServerAgent(t)
	agent.configEnv = append(agent.configEnv, "CC_WORKSPACE_FAKE_NOOP_SETTINGS_NO_NOTIFICATION=1")
	t.Cleanup(func() {
		if err := agent.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	created, err := agent.StartNativeConversation(ctx, fakeWorkspace(t, agent))
	if err != nil {
		t.Fatal(err)
	}
	if created.Thread.ID != "thread-new" || created.Settings.Revision == "" {
		t.Fatalf("StartNativeConversation() = %#v", created)
	}
}

func TestSubscribeNativeConversationReusesLoadedEmptyThread(t *testing.T) {
	agent, _ := newWorkspaceFakeAppServerAgent(t)
	agent.configEnv = append(agent.configEnv,
		"CC_WORKSPACE_FAKE_NOOP_SETTINGS_NO_NOTIFICATION=1",
		"CC_WORKSPACE_FAKE_EMPTY_THREAD_NO_ROLLOUT=1",
	)
	t.Cleanup(func() {
		if err := agent.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	workspace := fakeWorkspace(t, agent)
	created, err := agent.StartNativeConversation(ctx, workspace)
	if err != nil {
		t.Fatal(err)
	}
	subscription, err := agent.SubscribeNativeConversation(ctx, workspace, created.Thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Cancel()
	if subscription.Generation == 0 {
		t.Fatal("subscription generation is empty")
	}
	read, err := agent.ReadNativeConversation(ctx, workspace, created.Thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if read.Thread.ID != created.Thread.ID || read.Settings.Revision == "" {
		t.Fatalf("ReadNativeConversation() = %#v", read)
	}
}

func TestReadNativeConversation_RetriesAfterFailedResume(t *testing.T) {
	agent, logPath := newWorkspaceFakeAppServerAgent(t)
	agent.configEnv = append(agent.configEnv, "CC_WORKSPACE_FAKE_RESUME_FAIL_ONCE=1")
	t.Cleanup(func() {
		if err := agent.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	workspace := fakeWorkspace(t, agent)
	if _, err := agent.ReadNativeConversation(ctx, workspace, "thread-1"); err == nil || !strings.Contains(err.Error(), "temporary resume failure") {
		t.Fatalf("首次 resume 错误 = %v", err)
	}
	detail, err := agent.ReadNativeConversation(ctx, workspace, "thread-1")
	if err != nil || detail.Thread.ID != "thread-1" || detail.Settings.Revision == "" {
		t.Fatalf("第二次 ReadNativeConversation() = %#v, %v", detail, err)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(logData), "thread/resume\n"); count != 2 {
		t.Fatalf("thread/resume 次数 = %d, want 2; log:\n%s", count, logData)
	}
}

func TestWorkspaceAppServerReconnectsAfterDisconnect(t *testing.T) {
	agent, logPath := newWorkspaceFakeAppServerAgent(t)
	t.Cleanup(func() {
		if err := agent.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
	session, err := agent.StartSession(context.Background(), "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Send("disconnect", "m1", nil, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-session.Events():
	case <-time.After(2 * time.Second):
		t.Fatal("disconnected logical session did not terminate")
	}
	deadline := time.Now().Add(2 * time.Second)
	for session.Alive() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if session.Alive() {
		t.Fatal("logical session remained alive after transport disconnect")
	}
	workspace := fakeWorkspace(t, agent)
	if _, err := agent.ListNativeConversations(context.Background(), workspace, core.NativePageRequest{Limit: 1}); err != nil {
		t.Fatalf("ListNativeConversations after reconnect: %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "initialize\n"); got != 2 {
		t.Fatalf("initialize count after reconnect = %d, log:\n%s", got, data)
	}
}

func TestWorkspaceNativeConnectionGenerationChangesAndRejectsStaleMutations(t *testing.T) {
	agent, logPath := newWorkspaceFakeAppServerAgent(t)
	t.Cleanup(func() { _ = agent.Stop() })
	workspace := fakeWorkspace(t, agent)

	first, err := agent.SubscribeNativeConversation(context.Background(), workspace, "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	if first.Generation == 0 {
		t.Fatal("first subscription returned zero generation")
	}

	agent.controlMu.Lock()
	oldControl := agent.control
	agent.control = nil
	agent.controlMu.Unlock()
	if err := oldControl.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-first.Events:
		if ok {
			t.Fatal("old subscription emitted an event after its connection closed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("old subscription did not close with its connection")
	}
	first.Cancel()

	second, err := agent.SubscribeNativeConversation(context.Background(), workspace, "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Cancel()
	if second.Generation == 0 || second.Generation == first.Generation {
		t.Fatalf("connection generations did not change: first=%d second=%d", first.Generation, second.Generation)
	}

	mode := "plan"
	staleOperations := []struct {
		name string
		run  func() error
	}{
		{name: "settings", run: func() error {
			_, err := agent.UpdateNativeConversationSettings(context.Background(), workspace, "thread-1", first.Generation, core.NativeThreadSettingsPatch{Mode: &mode})
			return err
		}},
		{name: "turn start", run: func() error {
			_, err := agent.StartNativeTurn(context.Background(), workspace, "thread-1", first.Generation, core.NativeTurnStartRequest{Input: []core.NativeUserInput{{Type: "text", Text: "stale"}}})
			return err
		}},
		{name: "turn steer", run: func() error {
			_, err := agent.SteerNativeTurn(context.Background(), workspace, "thread-1", first.Generation, "turn-thread-1", []core.NativeUserInput{{Type: "text", Text: "stale"}})
			return err
		}},
		{name: "turn interrupt", run: func() error {
			return agent.InterruptNativeTurn(context.Background(), workspace, "thread-1", first.Generation, "turn-thread-1")
		}},
		{name: "interaction response", run: func() error {
			return agent.RespondNativeInteraction(context.Background(), workspace, "thread-1", first.Generation, json.RawMessage(`"request"`), json.RawMessage(`{"decision":"accept"}`))
		}},
		{name: "realtime start", run: func() error {
			return agent.StartNativeRealtime(context.Background(), workspace, "thread-1", first.Generation, core.NativeRealtimeStartRequest{SDP: "offer", Voice: "marin", Version: "v2"})
		}},
		{name: "realtime append", run: func() error {
			return agent.AppendNativeRealtimeText(context.Background(), workspace, "thread-1", first.Generation, "stale")
		}},
		{name: "realtime stop", run: func() error {
			return agent.StopNativeRealtime(context.Background(), workspace, "thread-1", first.Generation)
		}},
	}
	for _, operation := range staleOperations {
		if err := operation.run(); !errors.Is(err, core.ErrNativeConnectionStale) {
			t.Fatalf("%s error = %v, want ErrNativeConnectionStale", operation.name, err)
		}
	}

	if _, err := agent.StartNativeTurn(context.Background(), workspace, "thread-1", second.Generation, core.NativeTurnStartRequest{Input: []core.NativeUserInput{{Type: "text", Text: "current"}}}); err != nil {
		t.Fatalf("current generation turn start: %v", err)
	}
	waitNativeMethod(t, second.Events, "turn/completed")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(data)
	if got := strings.Count(logText, "thread/resume\n"); got != 2 {
		t.Fatalf("thread/resume count = %d, want one per subscription; log:\n%s", got, logText)
	}
	if strings.Contains(logText, "thread/settings/update\n") || strings.Contains(logText, "thread/realtime/start\n") ||
		strings.Contains(logText, "thread/realtime/appendText\n") || strings.Contains(logText, "thread/realtime/stop\n") {
		t.Fatalf("stale generation emitted mutation RPC; log:\n%s", logText)
	}
}

func TestWorkspaceNativeConversationRoutesInteractionsSettingsAndRealtime(t *testing.T) {
	agent, _ := newWorkspaceFakeAppServerAgent(t)
	t.Cleanup(func() { _ = agent.Stop() })
	workspace := fakeWorkspace(t, agent)
	subscription, err := agent.SubscribeNativeConversation(context.Background(), workspace, "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Cancel()
	events := subscription.Events
	generation := subscription.Generation

	result, err := agent.StartNativeTurn(context.Background(), workspace, "thread-1", generation, core.NativeTurnStartRequest{
		ClientMessageID: "client-1", Input: []core.NativeUserInput{{Type: "text", Text: "approve"}},
	})
	if err != nil || result.TurnID != "turn-thread-1" {
		t.Fatalf("StartNativeTurn() = %#v, %v", result, err)
	}
	approval := waitNativeMethod(t, events, "item/commandExecution/requestApproval")
	if approval.ConnectionGeneration == 0 || len(approval.RequestID) == 0 || !rawDecisionListContains(approval.AllowedDecisions, json.RawMessage(`"accept"`)) {
		t.Fatalf("approval envelope = %#v", approval)
	}
	if err := agent.RespondNativeInteraction(
		context.Background(), workspace, "thread-1", approval.ConnectionGeneration+1, approval.RequestID,
		json.RawMessage(`{"decision":"accept"}`),
	); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale interaction response error = %v", err)
	}
	if err := agent.RespondNativeInteraction(
		context.Background(), workspace, "thread-1", approval.ConnectionGeneration, approval.RequestID,
		json.RawMessage(`{"decision":"accept"}`),
	); err != nil {
		t.Fatal(err)
	}
	waitNativeMethod(t, events, "turn/completed")
	if _, err := agent.StartNativeTurn(context.Background(), workspace, "thread-1", generation, core.NativeTurnStartRequest{
		Input: []core.NativeUserInput{{Type: "text", Text: "unknown"}},
	}); err != nil {
		t.Fatal(err)
	}
	unknown := waitNativeMethod(t, events, "future/nativeEvent")
	if !strings.Contains(string(unknown.Payload), "kept-verbatim") {
		t.Fatalf("unknown event payload = %s", unknown.Payload)
	}
	waitNativeMethod(t, events, "turn/completed")

	mode := "plan"
	settings, err := agent.UpdateNativeConversationSettings(context.Background(), workspace, "thread-1", generation, core.NativeThreadSettingsPatch{Mode: &mode})
	if err != nil {
		t.Fatal(err)
	}
	if settings.Model != "gpt-other" || settings.Effort != "high" || settings.CollaborationMode == nil ||
		settings.CollaborationMode.Settings.Model != "gpt-other" || settings.CollaborationMode.Settings.ReasoningEffort == nil ||
		*settings.CollaborationMode.Settings.ReasoningEffort != "high" {
		t.Fatalf("plan mask defaults were not applied to top-level and nested settings: %#v", settings)
	}

	model := "gpt-test"
	effort := "medium"
	settings, err = agent.UpdateNativeConversationSettings(context.Background(), workspace, "thread-1", generation, core.NativeThreadSettingsPatch{
		Mode: &mode, Model: &model, PlanEffort: &effort,
	})
	if err != nil {
		t.Fatal(err)
	}
	if settings.Model != model || settings.CollaborationMode == nil || settings.CollaborationMode.Mode != "plan" ||
		settings.CollaborationMode.Settings.Model != model || settings.CollaborationMode.Settings.ReasoningEffort == nil ||
		*settings.CollaborationMode.Settings.ReasoningEffort != effort {
		t.Fatalf("canonical plan settings = %#v", settings)
	}
	defaultMode := "default"
	settings, err = agent.UpdateNativeConversationSettings(context.Background(), workspace, "thread-1", generation, core.NativeThreadSettingsPatch{Mode: &defaultMode})
	if err != nil {
		t.Fatal(err)
	}
	if settings.Effort != "medium" || settings.CollaborationMode == nil || settings.CollaborationMode.Mode != "default" {
		t.Fatalf("default mode did not restore normal effort: %#v", settings)
	}
	if _, err := agent.StartNativeTurn(context.Background(), workspace, "thread-1", generation, core.NativeTurnStartRequest{
		Input: []core.NativeUserInput{{Type: "text", Text: "hold"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.SteerNativeTurn(context.Background(), workspace, "thread-1", generation, "stale-turn", []core.NativeUserInput{{Type: "text", Text: "more"}}); err == nil || core.IsNativeAcceptanceUnknown(err) {
		t.Fatalf("stale steer error = %v, want definite rejection", err)
	}
	if err := agent.InterruptNativeTurn(context.Background(), workspace, "thread-1", generation, "stale-turn"); err == nil || core.IsNativeAcceptanceUnknown(err) {
		t.Fatalf("stale interrupt error = %v, want definite rejection", err)
	}
	if _, err := agent.SteerNativeTurn(context.Background(), workspace, "thread-1", generation, "turn-thread-1", []core.NativeUserInput{{Type: "text", Text: "more"}}); err != nil {
		t.Fatal(err)
	}
	if err := agent.InterruptNativeTurn(context.Background(), workspace, "thread-1", generation, "turn-thread-1"); err != nil {
		t.Fatal(err)
	}
	waitNativeMethod(t, events, "turn/completed")

	if err := agent.StartNativeRealtime(context.Background(), workspace, "thread-1", generation, core.NativeRealtimeStartRequest{SDP: "offer-sdp", Voice: "marin", Version: "v2"}); err != nil {
		t.Fatal(err)
	}
	sdp := waitNativeMethod(t, events, "thread/realtime/sdp")
	if !strings.Contains(string(sdp.Payload), "answer-sdp") {
		t.Fatalf("realtime SDP event = %#v", sdp)
	}
	if err := agent.AppendNativeRealtimeText(context.Background(), workspace, "thread-1", generation, "hello realtime"); err != nil {
		t.Fatal(err)
	}
	if err := agent.StopNativeRealtime(context.Background(), workspace, "thread-1", generation); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceNativeTurnCompletedBeforeRPCResponseStaysTerminal(t *testing.T) {
	agent, _ := newWorkspaceFakeAppServerAgent(t)
	t.Cleanup(func() { _ = agent.Stop() })
	workspace := fakeWorkspace(t, agent)
	subscription, err := agent.SubscribeNativeConversation(context.Background(), workspace, "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Cancel()
	result, err := agent.StartNativeTurn(context.Background(), workspace, "thread-1", subscription.Generation, core.NativeTurnStartRequest{
		Input: []core.NativeUserInput{{Type: "text", Text: "complete-before-response"}},
	})
	if err != nil || result.TurnID != "turn-thread-1" {
		t.Fatalf("StartNativeTurn() = %#v, %v", result, err)
	}
	waitNativeMethod(t, subscription.Events, "turn/completed")
	snapshot, err := agent.ReadNativeConversation(context.Background(), workspace, "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ActiveTurn != nil {
		t.Fatalf("completed turn was revived by RPC response: %#v", snapshot.ActiveTurn)
	}
}

func TestWorkspaceNativeConversationRejectsForgedWorkspace(t *testing.T) {
	agent, _ := newWorkspaceFakeAppServerAgent(t)
	t.Cleanup(func() { _ = agent.Stop() })
	workspace := fakeWorkspace(t, agent)
	subscription, err := agent.SubscribeNativeConversation(context.Background(), workspace, "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Cancel()
	forged := workspace
	forged.RootPath = filepath.Dir(workspace.RootPath)
	if _, err := agent.ListNativeConversations(context.Background(), forged, core.NativePageRequest{}); err == nil {
		t.Fatal("forged workspace payload was accepted")
	}
	if _, err := agent.ReadNativeConversation(context.Background(), workspace, "foreign"); err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("foreign thread error = %v", err)
	}
	if _, err := agent.ReadNativeConversation(context.Background(), workspace, "mismatch"); err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("mismatched thread id error = %v", err)
	}
	if _, err := agent.ReadNativeConversation(context.Background(), workspace, "missing"); !errors.Is(err, core.ErrNativeThreadNotFound) || !strings.Contains(err.Error(), "thread/read") {
		t.Fatalf("missing thread error = %v", err)
	}
	if _, err := agent.SubscribeNativeConversation(context.Background(), workspace, "resume-foreign"); !errors.Is(err, core.ErrNativeThreadNotFound) || !strings.Contains(err.Error(), "thread/resume") {
		t.Fatalf("foreign resumed thread error = %v", err)
	}
}

func TestWorkspaceNativeConversationReportsMissingCapability(t *testing.T) {
	agent, _ := newWorkspaceFakeAppServerAgent(t)
	agent.configEnv = append(agent.configEnv, "CC_WORKSPACE_FAKE_MISSING=collaborationMode/list")
	t.Cleanup(func() { _ = agent.Stop() })
	catalog, err := agent.NativeRuntimeCatalog(context.Background(), fakeWorkspace(t, agent))
	if err != nil {
		t.Fatal(err)
	}
	capability := catalog.Capabilities[nativeCapabilityCollaborationMode]
	if capability.Supported || !strings.Contains(capability.Reason, "method not found") {
		t.Fatalf("missing collaboration capability = %#v", capability)
	}
}

func TestWorkspaceNativeSettingsNotificationsCannotConfirmAnotherIntent(t *testing.T) {
	agent, _ := newWorkspaceFakeAppServerAgent(t)
	agent.configEnv = append(agent.configEnv, "CC_WORKSPACE_FAKE_SETTINGS_DELAY=50ms")
	t.Cleanup(func() { _ = agent.Stop() })
	workspace := fakeWorkspace(t, agent)
	control, _, cwd, err := agent.nativeControl(context.Background(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.resumeNativeThread(context.Background(), control, cwd, "thread-1"); err != nil {
		t.Fatal(err)
	}

	type result struct {
		model    string
		settings core.NativeThreadSettings
		err      error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, model := range []string{"gpt-test", "gpt-other"} {
		model := model
		go func() {
			<-start
			settings, updateErr := control.waitNativeSettings(context.Background(), "thread-1", map[string]any{
				"threadId": "thread-1", "model": model,
			})
			results <- result{model: model, settings: settings, err: updateErr}
		}()
	}
	close(start)
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.settings.Model != result.model {
			t.Fatalf("settings intent for %q confirmed by %q notification", result.model, result.settings.Model)
		}
	}
}

func TestWorkspaceNativePlanModeUsesPlanEffortAsEffectiveEffort(t *testing.T) {
	agent, _ := newWorkspaceFakeAppServerAgent(t)
	agent.configEnv = append(agent.configEnv, "CC_WORKSPACE_FAKE_PLAN_EFFORT_IS_EFFECTIVE=1")
	t.Cleanup(func() { _ = agent.Stop() })
	workspace := fakeWorkspace(t, agent)
	subscription, err := agent.SubscribeNativeConversation(context.Background(), workspace, "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Cancel()

	mode := "plan"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	settings, err := agent.UpdateNativeConversationSettings(ctx, workspace, "thread-1", subscription.Generation, core.NativeThreadSettingsPatch{Mode: &mode})
	if err != nil {
		t.Fatalf("plan settings update did not observe the canonical notification: %v", err)
	}
	if settings.Effort != "high" || settings.CollaborationMode == nil || settings.CollaborationMode.Settings.ReasoningEffort == nil ||
		*settings.CollaborationMode.Settings.ReasoningEffort != "high" {
		t.Fatalf("plan effort was not canonical at both levels: %#v", settings)
	}
}

func TestWorkspaceNativePlanModeAcceptsServerManagedDeveloperInstructions(t *testing.T) {
	agent, _ := newWorkspaceFakeAppServerAgent(t)
	agent.configEnv = append(agent.configEnv, "CC_WORKSPACE_FAKE_SERVER_MANAGED_MODE_INSTRUCTIONS=1")
	t.Cleanup(func() { _ = agent.Stop() })
	workspace := fakeWorkspace(t, agent)
	subscription, err := agent.SubscribeNativeConversation(context.Background(), workspace, "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Cancel()

	mode := "plan"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	settings, err := agent.UpdateNativeConversationSettings(ctx, workspace, "thread-1", subscription.Generation, core.NativeThreadSettingsPatch{Mode: &mode})
	if err != nil {
		t.Fatalf("server-managed plan instructions prevented settings confirmation: %v", err)
	}
	if settings.CollaborationMode == nil || settings.CollaborationMode.Settings.DeveloperInstructions == nil ||
		*settings.CollaborationMode.Settings.DeveloperInstructions != "server-managed" {
		t.Fatalf("canonical server-managed instructions were not preserved: %#v", settings)
	}
}

func TestUpdateNativeConversationSettings_ResumesThreadBeforeMutation(t *testing.T) {
	agent, _ := newWorkspaceFakeAppServerAgent(t)
	agent.configEnv = append(agent.configEnv, "CC_WORKSPACE_FAKE_REQUIRE_RESUME=1")
	t.Cleanup(func() { _ = agent.Stop() })
	workspace := fakeWorkspace(t, agent)
	control, _, _, err := agent.nativeControl(context.Background(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	mode := "plan"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	settings, err := agent.UpdateNativeConversationSettings(ctx, workspace, "thread-1", control.connectionGeneration, core.NativeThreadSettingsPatch{Mode: &mode})
	if err != nil {
		t.Fatalf("settings mutation did not resume the thread first: %v", err)
	}
	if settings.CollaborationMode == nil || settings.CollaborationMode.Mode != "plan" {
		t.Fatalf("settings mutation returned unexpected settings: %#v", settings)
	}
}

func TestWorkspaceNativeServerRequestsRouteByThreadAndGeneration(t *testing.T) {
	agent, _ := newWorkspaceFakeAppServerAgent(t)
	t.Cleanup(func() { _ = agent.Stop() })
	workspace := fakeWorkspace(t, agent)
	subscriptionOne, err := agent.SubscribeNativeConversation(context.Background(), workspace, "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	defer subscriptionOne.Cancel()
	subscriptionTwo, err := agent.SubscribeNativeConversation(context.Background(), workspace, "thread-2")
	if err != nil {
		t.Fatal(err)
	}
	defer subscriptionTwo.Cancel()
	for _, threadID := range []string{"thread-1", "thread-2"} {
		if _, err := agent.StartNativeTurn(context.Background(), workspace, threadID, subscriptionOne.Generation, core.NativeTurnStartRequest{Input: []core.NativeUserInput{{Type: "text", Text: "approve"}}}); err != nil {
			t.Fatal(err)
		}
	}
	one := waitNativeMethod(t, subscriptionOne.Events, "item/commandExecution/requestApproval")
	two := waitNativeMethod(t, subscriptionTwo.Events, "item/commandExecution/requestApproval")
	if one.ThreadID != "thread-1" || two.ThreadID != "thread-2" || string(one.RequestID) == string(two.RequestID) || one.ConnectionGeneration != two.ConnectionGeneration {
		t.Fatalf("routed requests = %#v / %#v", one, two)
	}
	response := json.RawMessage(`{"decision":"accept"}`)
	if err := agent.RespondNativeInteraction(context.Background(), workspace, "thread-2", one.ConnectionGeneration, one.RequestID, response); err == nil {
		t.Fatal("cross-thread interaction response was accepted")
	}
	if err := agent.RespondNativeInteraction(context.Background(), workspace, "thread-1", one.ConnectionGeneration, one.RequestID, response); err != nil {
		t.Fatal(err)
	}
	if err := agent.RespondNativeInteraction(context.Background(), workspace, "thread-2", two.ConnectionGeneration, two.RequestID, response); err != nil {
		t.Fatal(err)
	}
	waitNativeMethod(t, subscriptionOne.Events, "turn/completed")
	waitNativeMethod(t, subscriptionTwo.Events, "turn/completed")
}

func TestWorkspaceNativeAndLegacySessionCannotOwnSameThread(t *testing.T) {
	agent, _ := newWorkspaceFakeAppServerAgent(t)
	t.Cleanup(func() { _ = agent.Stop() })
	subscription, err := agent.SubscribeNativeConversation(context.Background(), fakeWorkspace(t, agent), "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Cancel()
	if _, err := agent.StartSession(context.Background(), "thread-1"); err == nil || !strings.Contains(err.Error(), "native lifecycle") {
		t.Fatalf("legacy/native ownership conflict error = %v", err)
	}

	legacyAgent, _ := newWorkspaceFakeAppServerAgent(t)
	t.Cleanup(func() { _ = legacyAgent.Stop() })
	legacy, err := legacyAgent.StartSession(context.Background(), "thread-2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacyAgent.SubscribeNativeConversation(context.Background(), fakeWorkspace(t, legacyAgent), "thread-2"); err == nil || !strings.Contains(err.Error(), "logical lifecycle") {
		t.Fatalf("native/logical ownership conflict error = %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	resumed, err := legacyAgent.SubscribeNativeConversation(context.Background(), fakeWorkspace(t, legacyAgent), "thread-2")
	if err != nil {
		t.Fatalf("native ownership after logical close: %v", err)
	}
	resumed.Cancel()
}

func TestWorkspaceNativeConversationSharesConnectionAcrossWorkspaces(t *testing.T) {
	agent, logPath := newWorkspaceFakeAppServerAgent(t)
	t.Cleanup(func() { _ = agent.Stop() })
	secondRoot := filepath.Join(agent.workDir, "second-root")
	if err := os.Mkdir(secondRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	state := map[string]any{
		"local-projects": map[string]any{"test-project": map[string]any{
			"id": "test-project", "name": "Test project", "rootPaths": []string{agent.workDir, secondRoot},
		}},
		"project-order": []string{"test-project"},
	}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(agent.codexHome, ".codex-global-state.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	workspaces, err := agent.ListWorkspaces(context.Background())
	if err != nil || len(workspaces) != 2 {
		t.Fatalf("workspaces = %#v, %v", workspaces, err)
	}
	for _, workspace := range workspaces {
		if _, err := agent.ListNativeConversations(context.Background(), workspace, core.NativePageRequest{Limit: 1}); err != nil {
			t.Fatal(err)
		}
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(logData), "initialize\n"); count != 1 {
		t.Fatalf("initialize count across workspaces = %d, log:\n%s", count, logData)
	}
}

func TestNativeUserInputsMapOnlyVerifiedServerInputs(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "image.png")
	if err := os.WriteFile(imagePath, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	mapped, err := nativeUserInputs([]core.NativeUserInput{
		{Type: "text", Text: "hello"},
		{Type: "image", URL: "data:image/png;base64,cG5n", Detail: "high"},
		{Type: "image", LocalPath: imagePath},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(mapped) != 3 || mapped[0]["type"] != "text" || mapped[1]["type"] != "image" || mapped[2]["type"] != "localImage" {
		t.Fatalf("mapped inputs = %#v", mapped)
	}
	for _, input := range []core.NativeUserInput{
		{Type: "audio", URL: "https://example.test/audio.mp3"},
		{Type: "audio", LocalPath: imagePath},
	} {
		if _, err := nativeUserInputs([]core.NativeUserInput{input}); err == nil || !strings.Contains(err.Error(), "verified text attachment reference") {
			t.Fatalf("raw audio input was accepted: %#v, %v", input, err)
		}
	}
	if _, err := nativeUserInputs([]core.NativeUserInput{{Type: "file", LocalPath: imagePath}}); err == nil {
		t.Fatal("raw file input was accepted")
	}
}

func TestNativeAllowedDecisionsPreservesStructuredCommandChoices(t *testing.T) {
	for _, method := range []string{"item/commandExecution/requestApproval", "item/fileChange/requestApproval", "mcpServer/elicitation/request"} {
		if got := nativeAllowedDecisions(method, json.RawMessage(`{"threadId":"thread-1"}`)); len(got) != 0 {
			t.Fatalf("%s invented decisions = %#v", method, got)
		}
	}
	structured := json.RawMessage(`{"availableDecisions":[{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["git","status"]}}]}`)
	wantStructured := json.RawMessage(`{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["git","status"]}}`)
	if got := nativeAllowedDecisions("item/commandExecution/requestApproval", structured); len(got) != 1 || !rawMessagesEqual(got[0], wantStructured) {
		t.Fatalf("structured availableDecisions = %#v", got)
	}
	explicit := json.RawMessage(`{"availableDecisions":["accept",{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["git","status"]}},"decline"]}`)
	got := nativeAllowedDecisions("item/commandExecution/requestApproval", explicit)
	if len(got) != 3 || !rawMessagesEqual(got[0], json.RawMessage(`"accept"`)) ||
		!rawMessagesEqual(got[1], wantStructured) || !rawMessagesEqual(got[2], json.RawMessage(`"decline"`)) {
		t.Fatalf("explicit choices = %#v", got)
	}
}

func rawDecisionListContains(values []json.RawMessage, expected json.RawMessage) bool {
	for _, value := range values {
		if rawMessagesEqual(value, expected) {
			return true
		}
	}
	return false
}

func TestValidateNativeInteractionResponseCoversAllInteractiveKinds(t *testing.T) {
	tests := []struct {
		name      string
		pending   nativePendingRequest
		response  string
		wantError bool
	}{
		{name: "file accept", pending: nativePendingRequest{Method: "item/fileChange/requestApproval", Params: json.RawMessage(`{"availableDecisions":["acceptForSession"]}`)}, response: `{"decision":"acceptForSession"}`},
		{name: "file unknown", pending: nativePendingRequest{Method: "item/fileChange/requestApproval"}, response: `{"decision":"always"}`, wantError: true},
		{name: "approval unknown field", pending: nativePendingRequest{Method: "item/fileChange/requestApproval", Params: json.RawMessage(`{"availableDecisions":["accept"]}`)}, response: `{"decision":"accept","secret":"unexpected"}`, wantError: true},
		{name: "structured command exact", pending: nativePendingRequest{Method: "item/commandExecution/requestApproval", Params: json.RawMessage(`{"availableDecisions":[{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["git","status"]}}]}`)}, response: `{"decision":{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["git","status"]}}}`},
		{name: "structured command rejects fallback string", pending: nativePendingRequest{Method: "item/commandExecution/requestApproval", Params: json.RawMessage(`{"availableDecisions":[{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["git","status"]}}]}`)}, response: `{"decision":"accept"}`, wantError: true},
		{name: "command missing choices rejects schema fallback", pending: nativePendingRequest{Method: "item/commandExecution/requestApproval", Params: json.RawMessage(`{"threadId":"thread-1"}`)}, response: `{"decision":"acceptForSession"}`, wantError: true},
		{name: "command missing choices rejects unknown", pending: nativePendingRequest{Method: "item/commandExecution/requestApproval", Params: json.RawMessage(`{"threadId":"thread-1"}`)}, response: `{"decision":"always"}`, wantError: true},
		{name: "mcp accept", pending: nativePendingRequest{Method: "mcpServer/elicitation/request", Params: json.RawMessage(`{"availableDecisions":["accept"]}`)}, response: `{"action":"accept","content":{"name":"value"}}`},
		{name: "mcp missing content", pending: nativePendingRequest{Method: "mcpServer/elicitation/request", Params: json.RawMessage(`{"availableDecisions":["accept"]}`)}, response: `{"action":"accept"}`, wantError: true},
		{name: "mcp null accepted content", pending: nativePendingRequest{Method: "mcpServer/elicitation/request", Params: json.RawMessage(`{"availableDecisions":["accept"]}`)}, response: `{"action":"accept","content":null}`, wantError: true},
		{name: "mcp decline null", pending: nativePendingRequest{Method: "mcpServer/elicitation/request", Params: json.RawMessage(`{"availableDecisions":["decline"]}`)}, response: `{"action":"decline","content":null}`},
		{name: "mcp decline non-null", pending: nativePendingRequest{Method: "mcpServer/elicitation/request", Params: json.RawMessage(`{"availableDecisions":["decline"]}`)}, response: `{"action":"decline","content":{}}`, wantError: true},
		{name: "mcp unknown field", pending: nativePendingRequest{Method: "mcpServer/elicitation/request", Params: json.RawMessage(`{"availableDecisions":["cancel"]}`)}, response: `{"action":"cancel","content":null,"extra":true}`, wantError: true},
		{name: "permissions requested", pending: nativePendingRequest{Method: "item/permissions/requestApproval", Params: json.RawMessage(`{"permissions":{"network":{"enabled":true}}}`)}, response: `{"permissions":{"network":{"enabled":true}},"scope":"turn"}`},
		{name: "permissions subset", pending: nativePendingRequest{Method: "item/permissions/requestApproval", Params: json.RawMessage(`{"permissions":{"network":{"hosts":["api.example.com","cdn.example.com"]},"fileSystem":{"read":["/tmp/a"]}}}`)}, response: `{"permissions":{"network":{"hosts":["api.example.com"]}}}`},
		{name: "permissions escalation", pending: nativePendingRequest{Method: "item/permissions/requestApproval", Params: json.RawMessage(`{"permissions":{"network":{"enabled":false}}}`)}, response: `{"permissions":{"network":{"enabled":true}}}`, wantError: true},
		{name: "question answer", pending: nativePendingRequest{Method: "item/tool/requestUserInput", Params: json.RawMessage(`{"questions":[{"id":"q1"}]}`)}, response: `{"answers":{"q1":{"answers":["yes"]}}}`},
		{name: "unknown question", pending: nativePendingRequest{Method: "item/tool/requestUserInput", Params: json.RawMessage(`{"questions":[{"id":"q1"}]}`)}, response: `{"answers":{"q2":{"answers":["yes"]}}}`, wantError: true},
		{name: "question nested unknown field", pending: nativePendingRequest{Method: "item/tool/requestUserInput", Params: json.RawMessage(`{"questions":[{"id":"q1"}]}`)}, response: `{"answers":{"q1":{"answers":["yes"],"extra":true}}}`, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateNativeApprovalResponse(test.pending, json.RawMessage(test.response))
			if (err != nil) != test.wantError {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestNativeServerRequestResolvedClearsPendingInteraction(t *testing.T) {
	session := &appServerSession{
		nativeThreads: map[string]string{}, nativeStates: map[string]*nativeConversationState{},
		nativeSubscriptions:   make(map[string]map[uint64]*nativeEventSubscription),
		nativeRequests:        make(map[string]nativePendingRequest),
		nativeSettingsWaiters: make(map[string]map[uint64]chan core.NativeThreadSettings),
		threadOwners:          make(map[string]string), connectionGeneration: 7,
	}
	session.alive.Store(true)
	if err := session.registerNativeThread("thread-1", "/tmp/project"); err != nil {
		t.Fatal(err)
	}
	events, cancel, err := session.subscribeNative("thread-1")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	requestID := json.RawMessage(`"request-1"`)
	key := nativeRequestKey(session.connectionGeneration, "thread-1", requestID)
	session.nativeMu.Lock()
	session.nativeRequests[key] = nativePendingRequest{ThreadID: "thread-1", RequestID: requestID, Generation: 7}
	session.nativeStateLocked("thread-1").PendingInteractions[key] = core.NativeInteraction{ID: key, ThreadID: "thread-1"}
	session.nativeMu.Unlock()

	session.handleNativeNotification("serverRequest/resolved", json.RawMessage(`{"threadId":"thread-1","requestId":"request-1"}`))
	event := waitNativeMethod(t, events, "serverRequest/resolved")
	if event.InteractionID != key || string(event.RequestID) != string(requestID) {
		t.Fatalf("resolved event = %#v", event)
	}
	session.nativeMu.Lock()
	_, requestPending := session.nativeRequests[key]
	_, interactionPending := session.nativeStateLocked("thread-1").PendingInteractions[key]
	session.nativeMu.Unlock()
	if requestPending || interactionPending {
		t.Fatal("resolved server request remained pending")
	}
}

func TestNativeSettingsNotificationIncludesMaterializedSettings(t *testing.T) {
	session := &appServerSession{
		nativeThreads: map[string]string{}, nativeStates: map[string]*nativeConversationState{},
		nativeSubscriptions:   make(map[string]map[uint64]*nativeEventSubscription),
		nativeRequests:        make(map[string]nativePendingRequest),
		nativeSettingsWaiters: make(map[string]map[uint64]chan core.NativeThreadSettings),
		threadOwners:          make(map[string]string), connectionGeneration: 8,
	}
	session.alive.Store(true)
	if err := session.registerNativeThread("thread-1", "/tmp/project"); err != nil {
		t.Fatal(err)
	}
	events, cancel, err := session.subscribeNative("thread-1")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	payload, err := json.Marshal(map[string]any{
		"threadId": "thread-1", "threadSettings": fakeNativeSettings("/tmp/project", "gpt-test", "high", map[string]any{
			"mode": "plan", "settings": map[string]any{"model": "gpt-test", "reasoning_effort": "high", "developer_instructions": nil},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	session.handleNativeNotification("thread/settings/updated", payload)
	event := waitNativeMethod(t, events, "thread/settings/updated")
	if !bytes.Equal(event.Payload, payload) {
		t.Fatalf("event payload was rewritten:\n got: %s\nwant: %s", event.Payload, payload)
	}
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal(event.Payload, &rawFields); err != nil {
		t.Fatalf("decode raw payload: %v", err)
	}
	if _, injected := rawFields["settings"]; injected {
		t.Fatalf("raw payload contains injected settings: %s", event.Payload)
	}
	if event.Settings == nil || event.Settings.Revision == "" || event.Settings.Model != "gpt-test" || event.Settings.Effort != "high" ||
		event.Settings.CollaborationMode == nil || event.Settings.CollaborationMode.Mode != "plan" {
		t.Fatalf("typed settings = %#v", event.Settings)
	}
}

func TestNativeEventSubscriptionOverflowClosesGeneration(t *testing.T) {
	subscription := newNativeEventSubscription()
	overflowed := false
	for i := 0; i < nativeEventSubscriptionQueueLimit+2; i++ {
		if !subscription.enqueue(core.NativeEventEnvelope{Method: fmt.Sprintf("event-%d", i+1)}) {
			overflowed = true
			break
		}
	}
	if !overflowed {
		t.Fatal("native subscription did not close on queue overflow")
	}
	if _, ok := <-subscription.out; ok {
		t.Fatal("closed native subscription emitted an old generation event")
	}
	if subscription.enqueue(core.NativeEventEnvelope{}) {
		t.Fatal("closed native subscription accepted another event")
	}
}

func waitNativeMethod(t *testing.T, events <-chan core.NativeEventEnvelope, method string) core.NativeEventEnvelope {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatalf("native event stream closed before %s", method)
			}
			if event.Method == method {
				return event
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for native event %s", method)
		}
	}
}

func TestAppServerLogicalSessionKeepsEventsChannelAfterClose(t *testing.T) {
	session := &appServerSession{
		events:           make(chan core.Event, 1),
		pendingApprovals: make(map[string]chan core.PermissionResult),
	}
	session.alive.Store(true)

	before := session.Events()
	session.closeLogical()
	after := session.Events()
	if after != before {
		t.Fatal("Events channel changed while the logical session was closing")
	}
	if _, ok := <-after; ok {
		t.Fatal("Events channel remains open after the logical session closes")
	}
}

func collectAppServerText(t *testing.T, events <-chan core.Event) string {
	t.Helper()
	var text string
	deadline := time.After(2 * time.Second)
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("event stream closed before result")
			}
			if event.Type == core.EventText {
				text += event.Content
			}
			if event.Type == core.EventResult {
				return text
			}
			if event.Type == core.EventError {
				t.Fatalf("agent event error: %v", event.Error)
			}
		case <-deadline:
			t.Fatal("timed out waiting for turn result")
		}
	}
}

func newWorkspaceFakeAppServerAgent(t *testing.T) (*Agent, string) {
	t.Helper()
	dir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "fake-codex")
	scriptBody := fmt.Sprintf("#!/bin/sh\nexec %q -test.run '^TestWorkspaceFakeAppServerProcess$' -- \"$@\"\n", executable)
	if err := os.WriteFile(script, []byte(scriptBody), 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "requests.log")
	state := map[string]any{
		"local-projects": map[string]any{
			"test-project": map[string]any{"id": "test-project", "name": "Test project", "rootPaths": []string{dir}},
		},
		"project-order": []string{"test-project"},
	}
	stateData, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".codex-global-state.json"), stateData, 0o600); err != nil {
		t.Fatal(err)
	}
	return &Agent{
		workDir: dir, backend: "app_server", mode: "suggest",
		cmd: script, activeIdx: -1, codexHome: dir,
		configEnv: []string{"CC_WORKSPACE_FAKE_SERVER=1", "CC_WORKSPACE_FAKE_LOG=" + logPath, "CC_WORKSPACE_FAKE_CWD=" + dir},
	}, logPath
}

func fakeWorkspace(t *testing.T, agent *Agent) core.Workspace {
	t.Helper()
	workspaces, err := agent.ListWorkspaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(workspaces) != 1 {
		t.Fatalf("workspaces = %#v", workspaces)
	}
	return workspaces[0]
}

func TestWorkspaceFakeAppServerProcess(t *testing.T) {
	if os.Getenv("CC_WORKSPACE_FAKE_SERVER") != "1" {
		return
	}
	logPath := os.Getenv("CC_WORKSPACE_FAKE_LOG")
	cwd := os.Getenv("CC_WORKSPACE_FAKE_CWD")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(2)
	}
	defer func() { _ = logFile.Close() }()
	writer := bufio.NewWriter(os.Stdout)
	write := func(value any) {
		data, _ := json.Marshal(value)
		_, _ = writer.Write(append(data, '\n'))
		_ = writer.Flush()
	}
	logMethod := func(method string) {
		_, _ = fmt.Fprintln(logFile, method)
		_ = logFile.Sync()
	}
	transport := ""
	for index, arg := range os.Args {
		if arg == "--listen" && index+1 < len(os.Args) {
			transport = os.Args[index+1]
			break
		}
	}
	logMethod("transport:" + transport)
	if transport != "stdio://" {
		os.Exit(2)
	}
	pendingApprovals := make(map[string]string)
	failedResumes := make(map[string]bool)
	resumedThreads := make(map[string]bool)
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request map[string]json.RawMessage
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		var method string
		_ = json.Unmarshal(request["method"], &method)
		if method == "" {
			var approvalID string
			_ = json.Unmarshal(request["id"], &approvalID)
			if threadID := pendingApprovals[approvalID]; threadID != "" {
				logMethod("approval-response:" + approvalID)
				delete(pendingApprovals, approvalID)
				fakeAppServerCompleteTurn(write, threadID, "approved:"+threadID)
			}
			continue
		}
		logMethod(method)
		if method == "initialized" {
			continue
		}
		id := request["id"]
		respond := func(result any) { write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result}) }
		if method == os.Getenv("CC_WORKSPACE_FAKE_MISSING") {
			write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32601, "message": "method not found: " + method}})
			continue
		}
		switch method {
		case "initialize":
			var params struct {
				Capabilities struct {
					ExperimentalAPI                bool     `json:"experimentalApi"`
					MCPServerOpenAIFormElicitation bool     `json:"mcpServerOpenaiFormElicitation"`
					OptOutNotificationMethods      []string `json:"optOutNotificationMethods"`
				} `json:"capabilities"`
			}
			_ = json.Unmarshal(request["params"], &params)
			logMethod(fmt.Sprintf("experimentalApi:%t", params.Capabilities.ExperimentalAPI))
			logMethod(fmt.Sprintf("mcpOpenaiForm:%t", params.Capabilities.MCPServerOpenAIFormElicitation))
			logMethod(fmt.Sprintf("optOutNotifications:%d", len(params.Capabilities.OptOutNotificationMethods)))
			respond(map[string]any{"protocolVersion": "test"})
		case "account/rateLimits/read":
			respond(map[string]any{"rateLimits": map[string]any{}})
		case "thread/list":
			var params struct {
				Cursor string `json:"cursor"`
				Cwd    string `json:"cwd"`
			}
			_ = json.Unmarshal(request["params"], &params)
			listCwd := params.Cwd
			if listCwd == "" {
				listCwd = cwd
			}
			if params.Cursor == "" {
				respond(map[string]any{"data": []any{fakeNativeThread("thread-1", listCwd)}, "nextCursor": "page-2", "backwardsCursor": "back-1"})
			} else {
				respond(map[string]any{"data": []any{fakeNativeThread("thread-2", listCwd)}, "nextCursor": nil, "backwardsCursor": "back-2"})
			}
		case "thread/read":
			var params struct {
				ThreadID     string `json:"threadId"`
				IncludeTurns *bool  `json:"includeTurns"`
			}
			_ = json.Unmarshal(request["params"], &params)
			if params.IncludeTurns == nil {
				write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32602, "message": "thread/read requires includeTurns"}})
				continue
			}
			if params.ThreadID == "missing" {
				write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32004, "message": "thread not found"}})
				continue
			}
			threadCwd := cwd
			if params.ThreadID == "foreign" {
				threadCwd = filepath.Join(cwd, "foreign")
			}
			returnedThreadID := params.ThreadID
			if params.ThreadID == "mismatch" {
				returnedThreadID = "different-thread"
			}
			thread := fakeNativeThread(returnedThreadID, threadCwd)
			if *params.IncludeTurns && params.ThreadID != "thread-new" {
				thread["turns"] = []any{map[string]any{
					"id": "turn-history", "status": "completed",
					"items": []any{
						map[string]any{"id": "user-1", "type": "userMessage", "text": "hello"},
						map[string]any{"id": "agent-1", "type": "agentMessage", "text": "world"},
					},
				}}
			}
			respond(map[string]any{"thread": thread})
		case "thread/start":
			thread := fakeNativeThread("thread-new", cwd)
			respond(fakeNativeRuntime(thread, cwd))
		case "thread/resume":
			var params struct {
				ThreadID               string `json:"threadId"`
				ExcludeTurns           *bool  `json:"excludeTurns"`
				PersistExtendedHistory *bool  `json:"persistExtendedHistory"`
			}
			_ = json.Unmarshal(request["params"], &params)
			if os.Getenv("CC_WORKSPACE_FAKE_RESUME_FAIL_ONCE") == "1" && !failedResumes[params.ThreadID] {
				failedResumes[params.ThreadID] = true
				write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32600, "message": "temporary resume failure"}})
				continue
			}
			if os.Getenv("CC_WORKSPACE_FAKE_EMPTY_THREAD_NO_ROLLOUT") == "1" && params.ThreadID == "thread-new" {
				write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32600, "message": "no rollout found for thread id " + params.ThreadID}})
				continue
			}
			if params.PersistExtendedHistory == nil && (params.ExcludeTurns == nil || !*params.ExcludeTurns) {
				write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32602, "message": "thread/resume requires excludeTurns=true"}})
				continue
			}
			resumeCwd := cwd
			if params.ThreadID == "resume-foreign" {
				resumeCwd = filepath.Join(cwd, "foreign")
			}
			resumedThreads[params.ThreadID] = true
			respond(fakeNativeRuntime(fakeNativeThread(params.ThreadID, resumeCwd), resumeCwd))
		case "model/list":
			var params struct {
				Cursor string `json:"cursor"`
			}
			_ = json.Unmarshal(request["params"], &params)
			if params.Cursor == "" {
				respond(map[string]any{"data": []any{fakeNativeModel("gpt-test", true)}, "nextCursor": "model-page-2"})
			} else {
				respond(map[string]any{"data": []any{fakeNativeModel("gpt-other", false)}, "nextCursor": nil})
			}
		case "collaborationMode/list":
			respond(map[string]any{"data": []any{
				map[string]any{"name": "Default", "mode": "default"},
				map[string]any{"name": "Plan", "mode": "plan", "model": "gpt-other", "reasoning_effort": "high"},
			}})
		case "permissionProfile/list":
			var params struct {
				Cursor string `json:"cursor"`
			}
			_ = json.Unmarshal(request["params"], &params)
			if params.Cursor == "" {
				respond(map[string]any{"data": []any{map[string]any{"id": ":workspace", "description": "Workspace", "allowed": true}}, "nextCursor": "permission-page-2"})
			} else {
				respond(map[string]any{"data": []any{map[string]any{"id": ":read-only", "description": "Read only", "allowed": true}}, "nextCursor": nil})
			}
		case "thread/realtime/listVoices":
			respond(map[string]any{"voices": map[string]any{"v1": []string{"alloy"}, "v2": []string{"marin"}, "defaultV1": "alloy", "defaultV2": "marin"}})
		case "thread/settings/update":
			var params struct {
				ThreadID          string          `json:"threadId"`
				Model             string          `json:"model"`
				Effort            string          `json:"effort"`
				CollaborationMode json.RawMessage `json:"collaborationMode"`
			}
			_ = json.Unmarshal(request["params"], &params)
			if (os.Getenv("CC_WORKSPACE_FAKE_RESUME_FAIL_ONCE") == "1" || os.Getenv("CC_WORKSPACE_FAKE_REQUIRE_RESUME") == "1") && !resumedThreads[params.ThreadID] {
				write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32600, "message": "thread not found: " + params.ThreadID}})
				continue
			}
			if params.ThreadID == "00000000-0000-7000-8000-000000000000" {
				write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32004, "message": "thread not found"}})
				continue
			}
			if os.Getenv("CC_WORKSPACE_FAKE_NOOP_SETTINGS_NO_NOTIFICATION") == "1" && params.Model == "" && params.Effort == "" && len(params.CollaborationMode) == 0 {
				respond(map[string]any{})
				continue
			}
			if delay, delayErr := time.ParseDuration(os.Getenv("CC_WORKSPACE_FAKE_SETTINGS_DELAY")); delayErr == nil && delay > 0 {
				time.Sleep(delay)
			}
			respond(map[string]any{})
			model := params.Model
			if model == "" {
				model = "gpt-test"
			}
			effort := params.Effort
			if effort == "" {
				effort = "medium"
			}
			mode := any(map[string]any{"mode": "default", "settings": map[string]any{"model": model, "reasoning_effort": effort, "developer_instructions": nil}})
			if len(params.CollaborationMode) > 0 {
				_ = json.Unmarshal(params.CollaborationMode, &mode)
				if os.Getenv("CC_WORKSPACE_FAKE_PLAN_EFFORT_IS_EFFECTIVE") == "1" {
					var canonical struct {
						Mode     string `json:"mode"`
						Settings struct {
							ReasoningEffort *string `json:"reasoning_effort"`
						} `json:"settings"`
					}
					if json.Unmarshal(params.CollaborationMode, &canonical) == nil && canonical.Mode == "plan" && canonical.Settings.ReasoningEffort != nil {
						effort = *canonical.Settings.ReasoningEffort
					}
				}
				if os.Getenv("CC_WORKSPACE_FAKE_SERVER_MANAGED_MODE_INSTRUCTIONS") == "1" {
					if modeValue, ok := mode.(map[string]any); ok {
						if settingsValue, ok := modeValue["settings"].(map[string]any); ok {
							settingsValue["developer_instructions"] = "server-managed"
						}
					}
				}
			}
			write(map[string]any{"method": "thread/settings/updated", "params": map[string]any{"threadId": params.ThreadID, "threadSettings": fakeNativeSettings(cwd, model, effort, mode)}})
		case "turn/start":
			var params struct {
				ThreadID string `json:"threadId"`
				Input    []struct {
					Text string `json:"text"`
				} `json:"input"`
			}
			_ = json.Unmarshal(request["params"], &params)
			prompt := ""
			if len(params.Input) > 0 {
				prompt = params.Input[0].Text
			}
			if prompt == "complete-before-response" {
				write(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": params.ThreadID, "turn": map[string]any{"id": "turn-" + params.ThreadID}}})
				fakeAppServerCompleteTurn(write, params.ThreadID, "completed-before-response")
				respond(map[string]any{"turn": map[string]any{"id": "turn-" + params.ThreadID}})
				continue
			}
			respond(map[string]any{"turn": map[string]any{"id": "turn-" + params.ThreadID}})
			write(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": params.ThreadID, "turn": map[string]any{"id": "turn-" + params.ThreadID}}})
			switch prompt {
			case "approve":
				approvalID := "approval-" + params.ThreadID
				pendingApprovals[approvalID] = params.ThreadID
				write(map[string]any{"jsonrpc": "2.0", "id": approvalID, "method": "item/commandExecution/requestApproval", "params": map[string]any{
					"threadId": params.ThreadID, "command": "echo " + params.ThreadID, "cwd": cwd,
					"availableDecisions": []string{"accept", "acceptForSession", "decline", "cancel"},
				}})
			case "disconnect":
				return
			case "hold":
				// 保持 Turn 活动，测试逻辑 session Close 发送 turn/interrupt。
			case "unknown":
				write(map[string]any{"method": "future/nativeEvent", "params": map[string]any{"threadId": params.ThreadID, "turnId": "turn-" + params.ThreadID, "value": "kept-verbatim"}})
				fakeAppServerCompleteTurn(write, params.ThreadID, "unknown-done")
			default:
				fakeAppServerCompleteTurn(write, params.ThreadID, "reply:"+params.ThreadID+":"+prompt)
			}
		case "turn/interrupt":
			var params struct {
				ThreadID string `json:"threadId"`
				TurnID   string `json:"turnId"`
			}
			_ = json.Unmarshal(request["params"], &params)
			logMethod("turn/interrupt:" + params.ThreadID + ":" + params.TurnID)
			if params.TurnID != "turn-"+params.ThreadID {
				write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32010, "message": "stale turn id"}})
			} else {
				respond(map[string]any{})
				write(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": params.ThreadID, "turn": map[string]any{"id": params.TurnID, "status": "interrupted"}}})
			}
		case "turn/steer":
			var params struct {
				ThreadID       string `json:"threadId"`
				ExpectedTurnID string `json:"expectedTurnId"`
			}
			_ = json.Unmarshal(request["params"], &params)
			if params.ExpectedTurnID != "turn-"+params.ThreadID {
				write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32010, "message": "stale turn id"}})
			} else {
				respond(map[string]any{"turnId": params.ExpectedTurnID})
			}
		case "thread/realtime/start":
			var params struct {
				ThreadID string `json:"threadId"`
			}
			_ = json.Unmarshal(request["params"], &params)
			respond(map[string]any{})
			write(map[string]any{"method": "thread/realtime/sdp", "params": map[string]any{"threadId": params.ThreadID, "sdp": "answer-sdp"}})
		case "thread/realtime/appendText", "thread/realtime/stop":
			respond(map[string]any{})
		default:
			respond(map[string]any{})
		}
	}
}

func fakeNativeThread(threadID, cwd string) map[string]any {
	return map[string]any{
		"id": threadID, "cwd": cwd, "name": threadID, "preview": threadID,
		"status": map[string]any{"type": "idle"}, "createdAt": int64(1), "updatedAt": int64(2), "turns": []any{},
	}
}

func fakeNativeRuntime(thread map[string]any, cwd string) map[string]any {
	return map[string]any{
		"thread": thread, "cwd": cwd, "model": "gpt-test", "modelProvider": "openai",
		"reasoningEffort": "medium", "serviceTier": "standard", "approvalPolicy": "on-request",
		"approvalsReviewer": "user", "sandbox": map[string]any{"type": "workspaceWrite"},
		"activePermissionProfile": map[string]any{"id": ":workspace"},
	}
}

func fakeNativeSettings(cwd, model, effort string, mode any) map[string]any {
	return map[string]any{
		"cwd": cwd, "model": model, "modelProvider": "openai", "effort": effort,
		"serviceTier": "standard", "personality": "pragmatic", "summary": "auto",
		"activePermissionProfile": map[string]any{"id": ":workspace"},
		"approvalPolicy":          "on-request", "approvalsReviewer": "user",
		"sandboxPolicy": map[string]any{"type": "workspaceWrite"}, "collaborationMode": mode,
	}
}

func fakeNativeModel(model string, isDefault bool) map[string]any {
	return map[string]any{
		"id": model, "model": model, "displayName": model, "description": model,
		"hidden": false, "isDefault": isDefault, "defaultReasoningEffort": "medium",
		"supportedReasoningEfforts": []any{
			map[string]any{"reasoningEffort": "medium", "description": "Medium"},
			map[string]any{"reasoningEffort": "high", "description": "High"},
		},
		"inputModalities": []string{"text", "image"}, "supportsPersonality": true,
		"serviceTiers":       []any{map[string]any{"id": "standard", "name": "Standard", "description": "Standard"}},
		"defaultServiceTier": "standard",
	}
}

func fakeAppServerCompleteTurn(write func(any), threadID, text string) {
	turnID := "turn-" + threadID
	write(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": threadID, "turnId": turnID, "item": map[string]any{"type": "agentMessage", "text": text}}})
	write(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": threadID, "turn": map[string]any{"id": turnID, "status": "completed"}}})
}
