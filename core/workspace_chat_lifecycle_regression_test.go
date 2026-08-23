package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type workspaceChatRecordingDeliveryPlatform struct {
	sent chan string
}

func (p *workspaceChatRecordingDeliveryPlatform) Name() string               { return "wecom" }
func (p *workspaceChatRecordingDeliveryPlatform) Start(MessageHandler) error { return nil }
func (p *workspaceChatRecordingDeliveryPlatform) Stop() error                { return nil }
func (p *workspaceChatRecordingDeliveryPlatform) Reply(context.Context, any, string) error {
	return nil
}
func (p *workspaceChatRecordingDeliveryPlatform) Send(_ context.Context, _ any, content string) error {
	p.sent <- content
	return nil
}

type workspaceChatContextCheckingRepository struct {
	*workspaceChatMemoryRepository
}

func (r *workspaceChatContextCheckingRepository) MarkSubmissionAccepted(ctx context.Context, requestID, threadID, nativeTurnID string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("accepted submission context: %w", err)
	}
	return r.workspaceChatMemoryRepository.MarkSubmissionAccepted(ctx, requestID, threadID, nativeTurnID)
}

func (r *workspaceChatContextCheckingRepository) MaterializeDraft(ctx context.Context, draftID, requestID, threadID, nativeTurnID string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("draft materialization context: %w", err)
	}
	return r.workspaceChatMemoryRepository.MaterializeDraft(ctx, draftID, requestID, threadID, nativeTurnID)
}

func (r *workspaceChatContextCheckingRepository) ResolveInteraction(ctx context.Context, interactionID, status string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("interaction terminal context: %w", err)
	}
	return r.workspaceChatMemoryRepository.ResolveInteraction(ctx, interactionID, status)
}

func TestWorkspaceChatCommitSurvivesClientCancellationAfterNativeAcceptance(t *testing.T) {
	t.Run("thread turn", func(t *testing.T) {
		fixture := newWorkspaceChatTestFixture(t)
		fixture.service.repo = &workspaceChatContextCheckingRepository{fixture.repository}
		ctx, cancel := context.WithCancel(context.Background())
		fixture.agent.mu.Lock()
		fixture.agent.afterStartTurn = cancel
		fixture.agent.mu.Unlock()

		result, err := fixture.service.StartTurn(ctx, "web:admin", "request-cancelled-after-accept", fixture.workspaceA.Ref,
			ConversationRef{Kind: ConversationKindThread, ID: fixture.threadA},
			[]NativeUserInput{{Type: "text", Text: "accepted"}}, NativeThreadSettingsPatch{})
		if err != nil {
			t.Fatalf("StartTurn() error = %v", err)
		}
		submission := fixture.repository.submission("request-cancelled-after-accept")
		if result.TurnID == "" || submission.Status != "accepted" || len(submission.InputJSON) != 0 {
			t.Fatalf("accepted submission = %#v, result = %#v", submission, result)
		}
	})

	t.Run("draft materialization", func(t *testing.T) {
		fixture := newWorkspaceChatTestFixture(t)
		fixture.service.repo = &workspaceChatContextCheckingRepository{fixture.repository}
		draft, err := fixture.service.CreateDraft(context.Background(), "web:admin", fixture.workspaceA.Ref)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		fixture.agent.mu.Lock()
		fixture.agent.afterStartTurn = cancel
		fixture.agent.mu.Unlock()

		result, err := fixture.service.StartTurn(ctx, "web:admin", "request-draft-cancelled-after-accept", fixture.workspaceA.Ref,
			ConversationRef{Kind: ConversationKindDraft, ID: draft.ID},
			[]NativeUserInput{{Type: "text", Text: "accepted draft"}}, NativeThreadSettingsPatch{})
		if err != nil {
			t.Fatalf("StartTurn(draft) error = %v", err)
		}
		persisted, err := fixture.repository.GetDraft(context.Background(), draft.ID)
		if err != nil {
			t.Fatal(err)
		}
		if result.TurnID == "" || persisted == nil || persisted.State != "materialized" {
			t.Fatalf("materialized draft = %#v, result = %#v", persisted, result)
		}
	})

	t.Run("interaction response", func(t *testing.T) {
		fixture := newWorkspaceChatTestFixture(t)
		fixture.service.repo = &workspaceChatContextCheckingRepository{fixture.repository}
		interaction := NativeInteraction{
			ID: "interaction-cancelled-after-response", Kind: "item/commandExecution/requestApproval", ThreadID: fixture.threadA,
			TurnID: "turn-pending", RequestID: json.RawMessage(`91`), ConnectionGeneration: 1,
			AllowedDecisions: testNativeStringDecisions("accept"), Payload: json.RawMessage(`{"command":"go test"}`), OccurredAt: time.Now().UTC(),
		}
		fixture.agent.mu.Lock()
		snapshot := fixture.agent.snapshots[fixture.threadA]
		snapshot.PendingInteractions = []NativeInteraction{interaction}
		fixture.agent.snapshots[fixture.threadA] = snapshot
		fixture.agent.mu.Unlock()
		if _, err := fixture.service.ReadThread(context.Background(), fixture.workspaceA.Ref, fixture.threadA); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		fixture.agent.mu.Lock()
		fixture.agent.afterInteraction = cancel
		fixture.agent.mu.Unlock()
		if err := fixture.service.RespondInteraction(ctx, fixture.workspaceA.Ref, fixture.threadA, interaction.ID, json.RawMessage(`{"decision":"accept"}`)); err != nil {
			t.Fatalf("RespondInteraction() error = %v", err)
		}
		record, ok := fixture.repository.interaction(interaction.ID)
		if !ok || record.Status != "resolved" {
			t.Fatalf("resolved interaction = %#v, exists=%v", record, ok)
		}
	})
}

func TestWorkspaceChatNativeAcceptanceUnknownNeedsRetryWithoutReplayableInput(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	fixture.agent.mu.Lock()
	fixture.agent.startTurnErr = &NativeAcceptanceUnknownError{Operation: "turn/start", Cause: context.DeadlineExceeded}
	fixture.agent.mu.Unlock()

	_, err := fixture.service.StartTurn(context.Background(), "web:admin", "request-acceptance-unknown", fixture.workspaceA.Ref,
		ConversationRef{Kind: ConversationKindThread, ID: fixture.threadA},
		[]NativeUserInput{{Type: "text", Text: "must not be replayed"}}, NativeThreadSettingsPatch{})
	if !IsNativeAcceptanceUnknown(err) {
		t.Fatalf("StartTurn() error = %v, want acceptance unknown", err)
	}
	submission := fixture.repository.submission("request-acceptance-unknown")
	if submission.Status != "needs_retry" || len(submission.InputJSON) != 0 {
		t.Fatalf("uncertain submission = %#v", submission)
	}
}

func TestWorkspaceChatDraftAcceptanceUnknownPersistsUncertainState(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	draft, err := fixture.service.CreateDraft(context.Background(), "web:admin", fixture.workspaceA.Ref)
	if err != nil {
		t.Fatal(err)
	}
	fixture.agent.mu.Lock()
	fixture.agent.startConversationErr = &NativeAcceptanceUnknownError{Operation: "thread/start", Cause: context.DeadlineExceeded}
	fixture.agent.mu.Unlock()
	conversation := ConversationRef{Kind: ConversationKindDraft, ID: draft.ID}

	_, err = fixture.service.StartTurn(context.Background(), "web:admin", "request-draft-unknown", fixture.workspaceA.Ref,
		conversation, []NativeUserInput{{Type: "text", Text: "must not be replayed"}}, NativeThreadSettingsPatch{})
	if !errors.Is(err, ErrWorkspaceDraftMaterializationUncertain) {
		t.Fatalf("StartTurn(draft) error = %v", err)
	}
	persisted, err := fixture.repository.GetDraft(context.Background(), draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted == nil || persisted.State != "materialization_uncertain" {
		t.Fatalf("uncertain draft = %#v", persisted)
	}
	submission := fixture.repository.submission("request-draft-unknown")
	if submission.Status != "needs_retry" || len(submission.InputJSON) != 0 {
		t.Fatalf("uncertain draft submission = %#v", submission)
	}
	_, err = fixture.service.StartTurn(context.Background(), "web:admin", "request-draft-retry", fixture.workspaceA.Ref,
		conversation, []NativeUserInput{{Type: "text", Text: "do not resend"}}, NativeThreadSettingsPatch{})
	if !errors.Is(err, ErrWorkspaceDraftMaterializationUncertain) {
		t.Fatalf("second StartTurn(draft) error = %v", err)
	}
}

func TestWorkspaceChatDraftFailureAfterThreadStartPersistsUncertainState(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	draft, err := fixture.service.CreateDraft(context.Background(), "web:admin", fixture.workspaceA.Ref)
	if err != nil {
		t.Fatal(err)
	}
	fixture.agent.mu.Lock()
	fixture.agent.startTurnErr = errors.New("native turn was explicitly rejected")
	fixture.agent.mu.Unlock()

	_, err = fixture.service.StartTurn(context.Background(), "web:admin", "request-post-thread-start-failure", fixture.workspaceA.Ref,
		ConversationRef{Kind: ConversationKindDraft, ID: draft.ID},
		[]NativeUserInput{{Type: "text", Text: "do not replay after thread creation"}}, NativeThreadSettingsPatch{})
	if !errors.Is(err, ErrWorkspaceDraftMaterializationUncertain) {
		t.Fatalf("StartTurn(draft) error = %v", err)
	}
	persisted, err := fixture.repository.GetDraft(context.Background(), draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted == nil || persisted.State != "materialization_uncertain" {
		t.Fatalf("draft after post-thread-start failure = %#v", persisted)
	}
	submission := fixture.repository.submission("request-post-thread-start-failure")
	if submission.Status != "needs_retry" || len(submission.InputJSON) != 0 {
		t.Fatalf("post-thread-start submission = %#v", submission)
	}
}

func TestWorkspaceChatSnapshotInteractionsArePersistedBeforeSnapshotPublication(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	interaction := NativeInteraction{
		ID: "snapshot-interaction", Kind: "item/tool/requestUserInput", ThreadID: fixture.threadA,
		TurnID: "turn-pending", ItemID: "item-pending", RequestID: json.RawMessage(`81`), ConnectionGeneration: 1,
		Payload: json.RawMessage(`{"questions":[{"id":"q1","question":"Continue?"}]}`), OccurredAt: time.Now().UTC(),
	}
	fixture.agent.mu.Lock()
	snapshot := fixture.agent.snapshots[fixture.threadA]
	snapshot.PendingInteractions = []NativeInteraction{interaction}
	fixture.agent.snapshots[fixture.threadA] = snapshot
	fixture.agent.mu.Unlock()

	read, err := fixture.service.ReadThread(context.Background(), fixture.workspaceA.Ref, fixture.threadA)
	if err != nil {
		t.Fatalf("ReadThread() error = %v", err)
	}
	if len(read.PendingInteractions) != 1 || read.PendingInteractions[0].ID != interaction.ID ||
		read.PendingInteractions[0].Kind != interaction.Kind {
		t.Fatalf("snapshot pending interactions = %#v", read.PendingInteractions)
	}
	record, ok := fixture.repository.interaction(interaction.ID)
	if !ok || record.Status != "pending" || record.ConnectionGeneration != 1 {
		t.Fatalf("persisted snapshot interaction = %#v, exists=%v", record, ok)
	}
}

func TestWorkspaceChatSelectionKeepsTransientFailuresAndDeletesPermanentMissingTargets(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	selection := WorkspaceChatSelection{
		ClientID: "web:admin", WorkspaceRef: fixture.workspaceA.Ref,
		Conversation: ConversationRef{Kind: ConversationKindThread, ID: fixture.threadA}, UpdatedAt: time.Now(),
	}
	if err := fixture.repository.PutSelection(context.Background(), selection); err != nil {
		t.Fatal(err)
	}
	transient := errors.New("codex app state is temporarily unreadable")
	fixture.agent.mu.Lock()
	fixture.agent.resolveWorkspaceErr = transient
	fixture.agent.mu.Unlock()

	got, err := fixture.service.Selection(context.Background(), selection.ClientID)
	if !errors.Is(err, transient) || got == nil || got.Conversation != selection.Conversation {
		t.Fatalf("Selection(transient) = %#v, %v", got, err)
	}
	if persisted, _ := fixture.repository.GetSelection(context.Background(), selection.ClientID); persisted == nil {
		t.Fatal("transient failure deleted the selection")
	}

	fixture.agent.mu.Lock()
	fixture.agent.resolveWorkspaceErr = ErrWorkspaceNotFound
	fixture.agent.mu.Unlock()
	got, err = fixture.service.Selection(context.Background(), selection.ClientID)
	if err != nil || got != nil {
		t.Fatalf("Selection(permanent missing) = %#v, %v", got, err)
	}
	if persisted, _ := fixture.repository.GetSelection(context.Background(), selection.ClientID); persisted != nil {
		t.Fatalf("permanently missing selection remained = %#v", persisted)
	}
}

func TestWorkspaceChatMalformedTurnCompletedNeedsRetry(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	result, err := fixture.service.StartTurn(context.Background(), "web:admin", "request-malformed-terminal", fixture.workspaceA.Ref,
		ConversationRef{Kind: ConversationKindThread, ID: fixture.threadA},
		[]NativeUserInput{{Type: "text", Text: "run"}}, NativeThreadSettingsPatch{})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.agent.emit(fixture.threadA, NativeEventEnvelope{
		Method: "turn/completed", ThreadID: fixture.threadA, TurnID: result.TurnID,
		Payload:    json.RawMessage(`{"threadId":"thread-a","turn":{"id":"` + result.TurnID + `","status":"inProgress"}}`),
		OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if !eventuallyWorkspaceChat(2*time.Second, func() bool {
		return fixture.repository.submission("request-malformed-terminal").Status == "needs_retry"
	}) {
		t.Fatalf("submission after malformed terminal = %#v", fixture.repository.submission("request-malformed-terminal"))
	}
}

func TestNativeTurnTerminalStatusRequiresExactTerminalField(t *testing.T) {
	for status, want := range map[string]string{"completed": "completed", "failed": "failed", "interrupted": "interrupted"} {
		t.Run(status, func(t *testing.T) {
			got, err := nativeTurnTerminalStatus(json.RawMessage(`{"status":"failed","turn":{"status":"` + status + `"}}`))
			if err != nil || got != want {
				t.Fatalf("nativeTurnTerminalStatus() = %q, %v", got, err)
			}
		})
	}
	for name, payload := range map[string]json.RawMessage{
		"missing":     json.RawMessage(`{"status":"completed"}`),
		"in progress": json.RawMessage(`{"turn":{"status":"inProgress"}}`),
		"unknown":     json.RawMessage(`{"turn":{"status":"cancelled"}}`),
	} {
		t.Run(name, func(t *testing.T) {
			if status, err := nativeTurnTerminalStatus(payload); err == nil || status != "" {
				t.Fatalf("nativeTurnTerminalStatus() = %q, %v", status, err)
			}
		})
	}
}

func TestWorkspaceChatThreadTurnRejectsInlineSettings(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	effort := "high"
	_, err := fixture.service.StartTurn(context.Background(), "web:admin", "request-thread-settings", fixture.workspaceA.Ref,
		ConversationRef{Kind: ConversationKindThread, ID: fixture.threadA},
		[]NativeUserInput{{Type: "text", Text: "run"}}, NativeThreadSettingsPatch{Effort: &effort})
	if err == nil || !strings.Contains(err.Error(), "only valid when materializing a draft") {
		t.Fatalf("StartTurn(thread settings) error = %v", err)
	}
	fixture.agent.mu.Lock()
	defer fixture.agent.mu.Unlock()
	if len(fixture.agent.startTurnCalls) != 0 || len(fixture.agent.settingsCalls) != 0 {
		t.Fatalf("rejected thread settings reached native backend: turns=%#v settings=%#v", fixture.agent.startTurnCalls, fixture.agent.settingsCalls)
	}
}

func TestWorkspaceChatRuntimeCatalogNormalizesEmptyCollections(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	fixture.agent.mu.Lock()
	fixture.agent.catalog = NativeRuntimeCatalog{}
	fixture.agent.mu.Unlock()
	catalog, err := fixture.service.RuntimeCatalog(context.Background(), fixture.workspaceA.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Capabilities == nil || catalog.Models == nil || catalog.Modes == nil || catalog.Permissions == nil ||
		catalog.Personalities == nil || catalog.Summaries == nil || catalog.Voices.V1 == nil || catalog.Voices.V2 == nil {
		t.Fatalf("runtime catalog contains nil collections: %#v", catalog)
	}
}

func TestWorkspaceChatFastTerminalNotificationsCannotReviveRuntimeState(t *testing.T) {
	t.Run("turn", func(t *testing.T) {
		fixture := newWorkspaceChatTestFixture(t)
		fixture.agent.mu.Lock()
		fixture.agent.afterStartTurn = func() {
			_ = fixture.agent.emit(fixture.threadA, NativeEventEnvelope{
				Method: "turn/completed", ThreadID: fixture.threadA, TurnID: "turn-new-1",
				Payload: json.RawMessage(`{"turn":{"id":"turn-new-1","status":"completed"}}`), OccurredAt: time.Now().UTC(),
			})
			time.Sleep(20 * time.Millisecond)
		}
		fixture.agent.mu.Unlock()
		result, err := fixture.service.StartTurn(context.Background(), "web:admin", "request-fast-terminal", fixture.workspaceA.Ref,
			ConversationRef{Kind: ConversationKindThread, ID: fixture.threadA}, []NativeUserInput{{Type: "text", Text: "fast"}}, NativeThreadSettingsPatch{})
		if err != nil || result.TurnID != "turn-new-1" {
			t.Fatalf("StartTurn() = %#v, %v", result, err)
		}
		actor := fixture.service.actorForThread(fixture.workspaceA.Ref, fixture.threadA)
		actor.mu.Lock()
		defer actor.mu.Unlock()
		if actor.activeTurnID != "" || actor.snapshot == nil || actor.snapshot.ActiveTurn != nil {
			t.Fatalf("fast completed turn was revived: active=%q snapshot=%#v", actor.activeTurnID, actor.snapshot.ActiveTurn)
		}
	})

	t.Run("realtime", func(t *testing.T) {
		fixture := newWorkspaceChatTestFixture(t)
		fixture.agent.mu.Lock()
		fixture.agent.afterStartRealtime = func() {
			_ = fixture.agent.emit(fixture.threadA, NativeEventEnvelope{
				Method: "thread/realtime/closed", ThreadID: fixture.threadA,
				Payload: json.RawMessage(`{"reason":"closed_before_response"}`), OccurredAt: time.Now().UTC(),
			})
			time.Sleep(20 * time.Millisecond)
		}
		fixture.agent.mu.Unlock()
		if err := fixture.service.StartRealtime(context.Background(), "socket-a", fixture.workspaceA.Ref, fixture.threadA, NativeRealtimeStartRequest{SDP: "v=0"}); err != nil {
			t.Fatal(err)
		}
		actor := fixture.service.actorForThread(fixture.workspaceA.Ref, fixture.threadA)
		actor.mu.Lock()
		defer actor.mu.Unlock()
		if actor.realtime || actor.realtimeOwner != "" {
			t.Fatalf("fast closed realtime was revived: active=%v owner=%q", actor.realtime, actor.realtimeOwner)
		}
	})
}

func TestWorkspaceChatRealtimeOwnershipIsConnectionScoped(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	if err := fixture.service.StartRealtime(context.Background(), "socket-a", fixture.workspaceA.Ref, fixture.threadA, NativeRealtimeStartRequest{SDP: "v=0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.SelectConversation(context.Background(), "wecom:user:user-1", fixture.workspaceB.Ref,
		ConversationRef{Kind: ConversationKindThread, ID: fixture.threadB}); err != nil {
		t.Fatal(err)
	}
	fixture.agent.mu.Lock()
	stopCalls := len(fixture.agent.realtimeStopCalls)
	fixture.agent.mu.Unlock()
	if stopCalls != 0 {
		t.Fatalf("selection change stopped another connection's realtime: calls=%d", stopCalls)
	}
	if err := fixture.service.AppendRealtimeText(context.Background(), "socket-b", fixture.workspaceA.Ref, fixture.threadA, "steal"); err == nil {
		t.Fatal("another connection appended to owned realtime")
	}
	if err := fixture.service.StopRealtime(context.Background(), "socket-b", fixture.workspaceA.Ref, fixture.threadA); err == nil {
		t.Fatal("another connection stopped owned realtime")
	}
	if err := fixture.service.StopRealtime(context.Background(), "socket-a", fixture.workspaceA.Ref, fixture.threadA); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceChatInteractionResponsePublishesResolvedEnvelope(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	interaction := NativeInteraction{
		ID: "interaction-ack", Kind: "item/commandExecution/requestApproval", ThreadID: fixture.threadA,
		TurnID: "turn-pending", ItemID: "item-pending", RequestID: json.RawMessage(`91`), ConnectionGeneration: 1,
		AllowedDecisions: testNativeStringDecisions("accept"), Payload: json.RawMessage(`{"availableDecisions":["accept"]}`), OccurredAt: time.Now().UTC(),
	}
	fixture.agent.mu.Lock()
	snapshot := fixture.agent.snapshots[fixture.threadA]
	snapshot.PendingInteractions = []NativeInteraction{interaction}
	fixture.agent.snapshots[fixture.threadA] = snapshot
	fixture.agent.mu.Unlock()
	subscription, err := fixture.service.Subscribe(context.Background(), "web:admin", fixture.workspaceA.Ref,
		ConversationRef{Kind: ConversationKindThread, ID: fixture.threadA}, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Cancel()
	if err := fixture.service.RespondInteraction(context.Background(), fixture.workspaceA.Ref, fixture.threadA, interaction.ID, json.RawMessage(`{"decision":"accept"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-subscription.Events:
		var envelope NativeEventEnvelope
		if event.Type != "native_event" || json.Unmarshal(event.Payload, &envelope) != nil || envelope.Method != "serverRequest/resolved" || envelope.InteractionID != interaction.ID {
			t.Fatalf("interaction ack = %#v payload=%s", event, event.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for interaction resolution ack")
	}
}

func TestWorkspaceChatIdleThreadActorRetires(t *testing.T) {
	oldTimeout := workspaceChatActorIdleTimeout
	workspaceChatActorIdleTimeout = 20 * time.Millisecond
	t.Cleanup(func() { workspaceChatActorIdleTimeout = oldTimeout })
	fixture := newWorkspaceChatTestFixture(t)
	if _, err := fixture.service.ReadThread(context.Background(), fixture.workspaceA.Ref, fixture.threadA); err != nil {
		t.Fatal(err)
	}
	if !eventuallyWorkspaceChat(2*time.Second, func() bool {
		return fixture.service.actorForThread(fixture.workspaceA.Ref, fixture.threadA) == nil
	}) {
		t.Fatal("idle thread actor and native pump were not retired")
	}
}

func TestWorkspaceChatDeliveryTargetDoesNotLeakAcrossTurns(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	actor := fixture.service.actor(fixture.workspaceA, ConversationRef{Kind: ConversationKindThread, ID: fixture.threadA})
	platform := &workspaceChatRecordingDeliveryPlatform{sent: make(chan string, 1)}
	actor.mu.Lock()
	actor.deliveries["turn-wecom"] = &workspaceChatDeliveryTarget{
		clientID: "wecom:user:user-1", requestID: "request-wecom", platform: platform, destination: "user-1",
	}
	actor.activeTurnID = "turn-wecom"
	actor.mu.Unlock()
	fixture.service.handleNativeEvent(actor, NativeEventEnvelope{
		Method: "turn/completed", ThreadID: fixture.threadA, TurnID: "turn-wecom",
		Payload: json.RawMessage(`{"turn":{"id":"turn-wecom","status":"completed"}}`), OccurredAt: time.Now().UTC(),
	})
	actor.mu.Lock()
	actor.activeTurnID = "turn-web"
	actor.mu.Unlock()
	fixture.service.deliverAssistant(actor, "turn-web", "must stay on web")
	select {
	case content := <-platform.sent:
		t.Fatalf("new Web turn leaked to previous WeCom target: %q", content)
	case <-time.After(100 * time.Millisecond):
	}
	fixture.service.handleNativeEvent(actor, NativeEventEnvelope{
		Method: "turn/completed", ThreadID: fixture.threadA, TurnID: "turn-web",
		Payload: json.RawMessage(`{"turn":{"id":"turn-web","status":"completed"}}`), OccurredAt: time.Now().UTC(),
	})
}
