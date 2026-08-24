package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func testNativeStringDecisions(values ...string) []json.RawMessage {
	result := make([]json.RawMessage, 0, len(values))
	for _, value := range values {
		raw, _ := json.Marshal(value)
		result = append(result, raw)
	}
	return result
}

type workspaceChatMemoryRepository struct {
	mu              sync.Mutex
	selections      map[string]WorkspaceChatSelection
	menus           map[string]WorkspaceChatMenuSnapshot
	drafts          map[string]WorkspaceChatDraft
	submissions     map[string]WorkspaceChatSubmission
	interactions    map[string]WorkspaceChatInteractionRecord
	intents         map[string]WorkspaceChatSettingIntent
	deliveries      map[string]WorkspaceChatDeliveryRecord
	materializeErr  error
	markAcceptedErr error
	markAccepted    int
	closed          bool
}

func newWorkspaceChatMemoryRepository() *workspaceChatMemoryRepository {
	return &workspaceChatMemoryRepository{
		selections:   make(map[string]WorkspaceChatSelection),
		menus:        make(map[string]WorkspaceChatMenuSnapshot),
		drafts:       make(map[string]WorkspaceChatDraft),
		submissions:  make(map[string]WorkspaceChatSubmission),
		interactions: make(map[string]WorkspaceChatInteractionRecord),
		intents:      make(map[string]WorkspaceChatSettingIntent),
		deliveries:   make(map[string]WorkspaceChatDeliveryRecord),
	}
}

func (r *workspaceChatMemoryRepository) GetSelection(_ context.Context, clientID string) (*WorkspaceChatSelection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	selection, ok := r.selections[clientID]
	if !ok {
		return nil, nil
	}
	return &selection, nil
}

func (r *workspaceChatMemoryRepository) PutSelection(_ context.Context, selection WorkspaceChatSelection) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.selections[selection.ClientID] = selection
	return nil
}

func (r *workspaceChatMemoryRepository) DeleteSelection(_ context.Context, clientID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.selections, clientID)
	return nil
}

func (r *workspaceChatMemoryRepository) GetMenu(_ context.Context, clientID, kind string) (*WorkspaceChatMenuSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot, ok := r.menus[clientID+"\x00"+kind]
	if !ok {
		return nil, nil
	}
	snapshot.ItemIDs = append([]string(nil), snapshot.ItemIDs...)
	return &snapshot, nil
}

func (r *workspaceChatMemoryRepository) PutMenu(_ context.Context, snapshot WorkspaceChatMenuSnapshot) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot.ItemIDs = append([]string(nil), snapshot.ItemIDs...)
	r.menus[snapshot.ClientID+"\x00"+snapshot.Kind] = snapshot
	return nil
}

func (r *workspaceChatMemoryRepository) CreateDraftAndSelect(_ context.Context, draft WorkspaceChatDraft, selection WorkspaceChatSelection) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.drafts[draft.ID]; exists {
		return fmt.Errorf("duplicate draft")
	}
	r.drafts[draft.ID] = draft
	r.selections[selection.ClientID] = selection
	return nil
}

func (r *workspaceChatMemoryRepository) GetDraft(_ context.Context, draftID string) (*WorkspaceChatDraft, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	draft, ok := r.drafts[draftID]
	if !ok {
		return nil, nil
	}
	return &draft, nil
}

func (r *workspaceChatMemoryRepository) UpdateDraftSettings(_ context.Context, draftID, ownerClientID, workspaceRef string, patch NativeThreadSettingsPatch, updatedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	draft, ok := r.drafts[draftID]
	if !ok || draft.OwnerClientID != ownerClientID || draft.WorkspaceRef != workspaceRef || draft.State != "draft" {
		return fmt.Errorf("draft not found")
	}
	draft.SettingsPatch = patch
	draft.UpdatedAt = updatedAt
	r.drafts[draftID] = draft
	return nil
}

func (r *workspaceChatMemoryRepository) MarkDraftMaterializationUncertain(_ context.Context, draftID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	draft, ok := r.drafts[draftID]
	if !ok {
		return fmt.Errorf("draft not found")
	}
	if draft.State == "materialization_uncertain" {
		return nil
	}
	if draft.State != "draft" {
		return fmt.Errorf("draft cannot become uncertain from %s", draft.State)
	}
	draft.State = "materialization_uncertain"
	draft.UpdatedAt = time.Now()
	r.drafts[draftID] = draft
	return nil
}

func (r *workspaceChatMemoryRepository) MaterializeDraft(_ context.Context, draftID, requestID, threadID, nativeTurnID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	submission, ok := r.submissions[requestID]
	if !ok {
		return fmt.Errorf("draft submission is missing")
	}
	submission.InputJSON = nil
	submission.UpdatedAt = time.Now()
	r.submissions[requestID] = submission
	if r.materializeErr != nil {
		return r.materializeErr
	}
	draft, ok := r.drafts[draftID]
	if !ok || draft.State != "draft" {
		return fmt.Errorf("draft is not materializable")
	}
	submission = r.submissions[requestID]
	if submission.Conversation != (ConversationRef{Kind: ConversationKindDraft, ID: draftID}) {
		return fmt.Errorf("draft submission is missing")
	}
	now := time.Now()
	draft.State = "materialized"
	draft.ThreadID = threadID
	draft.UpdatedAt = now
	r.drafts[draftID] = draft
	for clientID, selection := range r.selections {
		if selection.Conversation == (ConversationRef{Kind: ConversationKindDraft, ID: draftID}) {
			selection.Conversation = ConversationRef{Kind: ConversationKindThread, ID: threadID}
			selection.UpdatedAt = now
			r.selections[clientID] = selection
		}
	}
	for id, current := range r.submissions {
		if current.Conversation == (ConversationRef{Kind: ConversationKindDraft, ID: draftID}) {
			current.Conversation = ConversationRef{Kind: ConversationKindThread, ID: threadID}
			current.ThreadID = threadID
			current.UpdatedAt = now
			r.submissions[id] = current
		}
	}
	submission = r.submissions[requestID]
	submission.NativeTurnID = nativeTurnID
	submission.InputJSON = nil
	submission.Status = "accepted"
	submission.Error = ""
	submission.UpdatedAt = now
	r.submissions[requestID] = submission
	return nil
}

func (r *workspaceChatMemoryRepository) BeginSubmission(_ context.Context, submission WorkspaceChatSubmission) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.submissions[submission.RequestID]; exists {
		return fmt.Errorf("duplicate submission")
	}
	submission.InputJSON = append(json.RawMessage(nil), submission.InputJSON...)
	r.submissions[submission.RequestID] = submission
	return nil
}

func (r *workspaceChatMemoryRepository) MarkSubmissionAccepted(_ context.Context, requestID, threadID, nativeTurnID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.markAccepted++
	if r.markAcceptedErr != nil {
		return r.markAcceptedErr
	}
	submission, ok := r.submissions[requestID]
	if !ok {
		return fmt.Errorf("submission not found")
	}
	submission.ThreadID = threadID
	submission.NativeTurnID = nativeTurnID
	submission.InputJSON = nil
	submission.Status = "accepted"
	submission.Error = ""
	submission.UpdatedAt = time.Now()
	r.submissions[requestID] = submission
	return nil
}

func (r *workspaceChatMemoryRepository) FinishSubmission(_ context.Context, requestID, status, errorMessage string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	submission, ok := r.submissions[requestID]
	if !ok {
		return fmt.Errorf("submission not found")
	}
	submission.Status = status
	submission.Error = errorMessage
	submission.InputJSON = nil
	submission.UpdatedAt = time.Now()
	r.submissions[requestID] = submission
	return nil
}

func (r *workspaceChatMemoryRepository) ListUnfinishedSubmissions(context.Context) ([]WorkspaceChatSubmission, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []WorkspaceChatSubmission
	for _, submission := range r.submissions {
		if submission.Status == "pending" || submission.Status == "accepted" {
			result = append(result, submission)
		}
	}
	return result, nil
}

func (r *workspaceChatMemoryRepository) PutInteraction(_ context.Context, record WorkspaceChatInteractionRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	record.Interaction.RequestID = append(json.RawMessage(nil), record.Interaction.RequestID...)
	record.Interaction.Payload = append(json.RawMessage(nil), record.Interaction.Payload...)
	r.interactions[record.Interaction.ID] = record
	return nil
}

func (r *workspaceChatMemoryRepository) ResolveInteraction(_ context.Context, interactionID, status string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.interactions[interactionID]
	if !ok {
		return fmt.Errorf("interaction not found")
	}
	record.Status = status
	r.interactions[interactionID] = record
	return nil
}

func (r *workspaceChatMemoryRepository) ListPendingInteractions(_ context.Context, threadID string) ([]WorkspaceChatInteractionRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []WorkspaceChatInteractionRecord
	for _, record := range r.interactions {
		if record.Status == "pending" && record.Interaction.ThreadID == threadID {
			result = append(result, record)
		}
	}
	return result, nil
}

func (r *workspaceChatMemoryRepository) ExpirePendingInteractions(_ context.Context, status string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, record := range r.interactions {
		if record.Status == "pending" {
			record.Status = status
			r.interactions[id] = record
		}
	}
	return nil
}

func (r *workspaceChatMemoryRepository) PutSettingIntent(_ context.Context, intent WorkspaceChatSettingIntent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.intents[intent.ID] = intent
	return nil
}

func (r *workspaceChatMemoryRepository) ResolveSettingIntent(_ context.Context, intentID, status, errorMessage string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	intent, ok := r.intents[intentID]
	if !ok {
		return fmt.Errorf("setting intent not found")
	}
	intent.Status = status
	intent.Error = errorMessage
	intent.UpdatedAt = time.Now()
	r.intents[intentID] = intent
	return nil
}

func (r *workspaceChatMemoryRepository) ListPendingSettingIntents(context.Context) ([]WorkspaceChatSettingIntent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []WorkspaceChatSettingIntent
	for _, intent := range r.intents {
		if intent.Status == "pending" {
			result = append(result, intent)
		}
	}
	return result, nil
}

func (r *workspaceChatMemoryRepository) PutDelivery(_ context.Context, delivery WorkspaceChatDeliveryRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deliveries[delivery.ID] = delivery
	return nil
}

func (r *workspaceChatMemoryRepository) FinishDelivery(_ context.Context, deliveryID, status, errorMessage string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delivery, ok := r.deliveries[deliveryID]
	if !ok {
		return fmt.Errorf("delivery not found")
	}
	delivery.Status = status
	delivery.Error = errorMessage
	delivery.UpdatedAt = time.Now()
	r.deliveries[deliveryID] = delivery
	return nil
}

func (r *workspaceChatMemoryRepository) ListPendingDeliveries(context.Context) ([]WorkspaceChatDeliveryRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []WorkspaceChatDeliveryRecord
	for _, delivery := range r.deliveries {
		if delivery.Status == "pending" {
			result = append(result, delivery)
		}
	}
	return result, nil
}

func (r *workspaceChatMemoryRepository) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return nil
}

func (r *workspaceChatMemoryRepository) submission(requestID string) WorkspaceChatSubmission {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.submissions[requestID]
}

func (r *workspaceChatMemoryRepository) interaction(interactionID string) (WorkspaceChatInteractionRecord, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.interactions[interactionID]
	return record, ok
}

func (r *workspaceChatMemoryRepository) firstPendingInteraction() (WorkspaceChatInteractionRecord, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, record := range r.interactions {
		if record.Status == "pending" {
			return record, true
		}
	}
	return WorkspaceChatInteractionRecord{}, false
}

type workspaceChatNativeTestAgent struct {
	mu sync.Mutex

	workspaces  []Workspace
	snapshots   map[string]NativeConversationSnapshot
	events      map[string]chan NativeEventEnvelope
	generations map[string]uint64
	turns       map[string]NativeTurnPage
	items       map[string]NativeItemPage
	catalog     NativeRuntimeCatalog

	startConversationCalls int
	startTurnCalls         []workspaceChatStartTurnCall
	steerCalls             []workspaceChatSteerCall
	interactionResponses   []workspaceChatInteractionResponseCall
	lastThreadPage         NativePageRequest
	lastTurnPage           NativePageRequest
	lastItemPage           NativePageRequest
	lastItemTurnID         string
	settingsCalls          []NativeThreadSettingsPatch
	interruptCalls         []workspaceChatInterruptCall
	realtimeStopCalls      []workspaceChatRealtimeStopCall
	realtimeStartCalls     []workspaceChatRealtimeStartCall
	realtimeAppendCalls    []workspaceChatRealtimeAppendCall
	readConversationCalls  []workspaceChatReadConversationCall
	resolveWorkspaceCalls  []string
	interruptErr           error
	realtimeStopErr        error
	resolveWorkspaceErr    error
	readConversationErr    error
	startConversationErr   error
	startTurnErr           error
	steerErr               error
	afterStartTurn         func()
	afterInteraction       func()
	afterStartRealtime     func()
	startTurnStarted       chan struct{}
	startTurnRelease       chan struct{}
	nextThread             int
	nextTurn               int
	settingsRevision       int
}

func (a *workspaceChatNativeTestAgent) ValidateWorkspaceAccess(ctx context.Context, workspace Workspace) error {
	resolved, err := a.ResolveWorkspace(ctx, workspace.Ref)
	if err != nil {
		return err
	}
	if !resolved.Available || !sameWorkspacePath(resolved.RootPath, workspace.RootPath) {
		return fmt.Errorf("test workspace is unavailable")
	}
	return nil
}

func (a *workspaceChatNativeTestAgent) ValidateNativeThreadAccess(_ context.Context, workspace Workspace, thread NativeThread) error {
	if strings.TrimSpace(thread.ID) == "" || !sameWorkspacePath(thread.Cwd, workspace.RootPath) {
		return fmt.Errorf("test thread does not belong to workspace")
	}
	return nil
}

type workspaceChatStartTurnCall struct {
	WorkspaceRef string
	ThreadID     string
	Request      NativeTurnStartRequest
}

type workspaceChatSteerCall struct {
	WorkspaceRef   string
	ThreadID       string
	ExpectedTurnID string
	Input          []NativeUserInput
}

type workspaceChatInteractionResponseCall struct {
	WorkspaceRef string
	ThreadID     string
	Generation   uint64
	RequestID    json.RawMessage
	Response     json.RawMessage
}

type workspaceChatInterruptCall struct {
	WorkspaceRef string
	ThreadID     string
	Generation   uint64
	TurnID       string
}

type workspaceChatRealtimeStopCall struct {
	WorkspaceRef string
	ThreadID     string
	Generation   uint64
}

type workspaceChatRealtimeStartCall struct {
	WorkspaceRef string
	ThreadID     string
	Generation   uint64
	Request      NativeRealtimeStartRequest
}

type workspaceChatRealtimeAppendCall struct {
	WorkspaceRef string
	ThreadID     string
	Generation   uint64
	Text         string
}

type workspaceChatReadConversationCall struct {
	WorkspaceRef string
	ThreadID     string
}

func (a *workspaceChatNativeTestAgent) Name() string { return "workspace-native-test" }

func (a *workspaceChatNativeTestAgent) StartSession(context.Context, string) (AgentSession, error) {
	return &workspaceChatUnusedAgentSession{events: make(chan Event)}, nil
}

func (a *workspaceChatNativeTestAgent) ListSessions(context.Context) ([]AgentSessionInfo, error) {
	return nil, nil
}

func (a *workspaceChatNativeTestAgent) Stop() error { return nil }

func (a *workspaceChatNativeTestAgent) ListWorkspaces(context.Context) ([]Workspace, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Workspace(nil), a.workspaces...), nil
}

func (a *workspaceChatNativeTestAgent) ResolveWorkspace(_ context.Context, ref string) (Workspace, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.resolveWorkspaceCalls = append(a.resolveWorkspaceCalls, ref)
	if a.resolveWorkspaceErr != nil {
		return Workspace{}, a.resolveWorkspaceErr
	}
	for _, workspace := range a.workspaces {
		if workspace.Ref == ref {
			return workspace, nil
		}
	}
	return Workspace{}, ErrWorkspaceNotFound
}

func (a *workspaceChatNativeTestAgent) ListNativeConversations(_ context.Context, workspace Workspace, page NativePageRequest) (NativeThreadPage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastThreadPage = page
	result := NativeThreadPage{NextCursor: "threads-next"}
	for _, snapshot := range a.snapshots {
		if sameWorkspacePath(snapshot.Thread.Cwd, workspace.RootPath) {
			result.Data = append(result.Data, snapshot.Thread)
		}
	}
	return result, nil
}

func (a *workspaceChatNativeTestAgent) ReadNativeConversation(_ context.Context, workspace Workspace, threadID string) (NativeConversationSnapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.readConversationCalls = append(a.readConversationCalls, workspaceChatReadConversationCall{
		WorkspaceRef: workspace.Ref,
		ThreadID:     threadID,
	})
	if a.readConversationErr != nil {
		return NativeConversationSnapshot{}, a.readConversationErr
	}
	snapshot, ok := a.snapshots[threadID]
	if !ok {
		return NativeConversationSnapshot{}, ErrNativeThreadNotFound
	}
	return cloneWorkspaceChatSnapshot(snapshot), nil
}

func (a *workspaceChatNativeTestAgent) StartNativeConversation(_ context.Context, workspace Workspace) (NativeConversationSnapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.startConversationCalls++
	if a.startConversationErr != nil {
		return NativeConversationSnapshot{}, a.startConversationErr
	}
	a.nextThread++
	threadID := fmt.Sprintf("thread-new-%d", a.nextThread)
	now := time.Now()
	snapshot := NativeConversationSnapshot{
		Thread:   NativeThread{ID: threadID, Cwd: workspace.RootPath, CreatedAt: now, UpdatedAt: now},
		Settings: NativeThreadSettings{Revision: "settings-1", Model: "gpt-5", Effort: "low"},
		DeepLink: workspaceChatTestDeepLink(threadID),
		Capabilities: map[string]CapabilityStatus{
			"settings": {Supported: true},
			"realtime": {Supported: true},
		},
	}
	a.snapshots[threadID] = snapshot
	a.events[threadID] = make(chan NativeEventEnvelope, 64)
	return cloneWorkspaceChatSnapshot(snapshot), nil
}

func (a *workspaceChatNativeTestAgent) ListNativeTurns(_ context.Context, _ Workspace, threadID string, page NativePageRequest) (NativeTurnPage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastTurnPage = page
	return a.turns[threadID], nil
}

func (a *workspaceChatNativeTestAgent) ListNativeItems(_ context.Context, _ Workspace, threadID, turnID string, page NativePageRequest) (NativeItemPage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastItemPage = page
	a.lastItemTurnID = turnID
	return a.items[threadID+"\x00"+turnID], nil
}

func (a *workspaceChatNativeTestAgent) NativeRuntimeCatalog(context.Context, Workspace) (NativeRuntimeCatalog, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.catalog, nil
}

func (a *workspaceChatNativeTestAgent) SubscribeNativeConversation(_ context.Context, _ Workspace, threadID string) (NativeConversationSubscription, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	events, ok := a.events[threadID]
	if !ok {
		return NativeConversationSubscription{}, ErrNativeThreadNotFound
	}
	generation := a.generations[threadID]
	if generation == 0 {
		generation = 1
	}
	return NativeConversationSubscription{Generation: generation, Events: events, Cancel: func() {}}, nil
}

func (a *workspaceChatNativeTestAgent) UpdateNativeConversationSettings(_ context.Context, _ Workspace, threadID string, _ uint64, patch NativeThreadSettingsPatch) (NativeThreadSettings, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	snapshot, ok := a.snapshots[threadID]
	if !ok {
		return NativeThreadSettings{}, ErrNativeThreadNotFound
	}
	a.settingsCalls = append(a.settingsCalls, patch)
	settings := snapshot.Settings
	if patch.Model != nil {
		settings.Model = *patch.Model
	}
	if patch.Effort != nil {
		settings.Effort = *patch.Effort
	}
	if patch.ServiceTier != nil {
		settings.ServiceTier = *patch.ServiceTier
	}
	if patch.Personality != nil {
		settings.Personality = *patch.Personality
	}
	if patch.Summary != nil {
		settings.Summary = *patch.Summary
	}
	if patch.PermissionProfile != nil {
		settings.PermissionProfile = *patch.PermissionProfile
	}
	if patch.Mode != nil {
		settings.CollaborationMode = &NativeCollaborationMode{Mode: *patch.Mode, Settings: NativeCollaborationSettings{Model: settings.Model}}
	}
	a.settingsRevision++
	settings.Revision = fmt.Sprintf("settings-%d", a.settingsRevision+1)
	snapshot.Settings = settings
	a.snapshots[threadID] = snapshot
	return settings, nil
}

func (a *workspaceChatNativeTestAgent) StartNativeTurn(ctx context.Context, workspace Workspace, threadID string, _ uint64, request NativeTurnStartRequest) (NativeTurnResult, error) {
	a.mu.Lock()
	started := a.startTurnStarted
	release := a.startTurnRelease
	a.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return NativeTurnResult{}, ctx.Err()
		}
	}
	a.mu.Lock()
	snapshot, ok := a.snapshots[threadID]
	if !ok {
		a.mu.Unlock()
		return NativeTurnResult{}, ErrNativeThreadNotFound
	}
	if a.startTurnErr != nil {
		err := a.startTurnErr
		a.mu.Unlock()
		return NativeTurnResult{}, err
	}
	a.nextTurn++
	turnID := fmt.Sprintf("turn-new-%d", a.nextTurn)
	a.startTurnCalls = append(a.startTurnCalls, workspaceChatStartTurnCall{WorkspaceRef: workspace.Ref, ThreadID: threadID, Request: request})
	snapshot.ActiveTurn = &NativeActiveTurn{ID: turnID, RequestID: request.ClientMessageID, StartedAt: time.Now()}
	a.snapshots[threadID] = snapshot
	afterStartTurn := a.afterStartTurn
	a.mu.Unlock()
	if afterStartTurn != nil {
		afterStartTurn()
	}
	return NativeTurnResult{TurnID: turnID}, nil
}

func (a *workspaceChatNativeTestAgent) SteerNativeTurn(_ context.Context, workspace Workspace, threadID string, _ uint64, expectedTurnID string, input []NativeUserInput) (NativeTurnResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.steerErr != nil {
		return NativeTurnResult{}, a.steerErr
	}
	snapshot, ok := a.snapshots[threadID]
	if !ok || snapshot.ActiveTurn == nil || snapshot.ActiveTurn.ID != expectedTurnID {
		return NativeTurnResult{}, ErrWorkspaceStaleTurn
	}
	a.steerCalls = append(a.steerCalls, workspaceChatSteerCall{
		WorkspaceRef: workspace.Ref, ThreadID: threadID, ExpectedTurnID: expectedTurnID, Input: append([]NativeUserInput(nil), input...),
	})
	return NativeTurnResult{TurnID: expectedTurnID}, nil
}

func (a *workspaceChatNativeTestAgent) InterruptNativeTurn(_ context.Context, workspace Workspace, threadID string, generation uint64, turnID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.interruptCalls = append(a.interruptCalls, workspaceChatInterruptCall{
		WorkspaceRef: workspace.Ref, ThreadID: threadID, Generation: generation, TurnID: turnID,
	})
	if a.interruptErr != nil {
		return a.interruptErr
	}
	snapshot, ok := a.snapshots[threadID]
	if !ok || snapshot.ActiveTurn == nil || snapshot.ActiveTurn.ID != turnID {
		return ErrWorkspaceStaleTurn
	}
	snapshot.ActiveTurn = nil
	a.snapshots[threadID] = snapshot
	return nil
}

func (a *workspaceChatNativeTestAgent) RespondNativeInteraction(_ context.Context, workspace Workspace, threadID string, generation uint64, requestID, response json.RawMessage) error {
	a.mu.Lock()
	a.interactionResponses = append(a.interactionResponses, workspaceChatInteractionResponseCall{
		WorkspaceRef: workspace.Ref,
		ThreadID:     threadID,
		Generation:   generation,
		RequestID:    append(json.RawMessage(nil), requestID...),
		Response:     append(json.RawMessage(nil), response...),
	})
	afterInteraction := a.afterInteraction
	a.mu.Unlock()
	if afterInteraction != nil {
		afterInteraction()
	}
	return nil
}

func (a *workspaceChatNativeTestAgent) StartNativeRealtime(_ context.Context, workspace Workspace, threadID string, generation uint64, request NativeRealtimeStartRequest) error {
	a.mu.Lock()
	a.realtimeStartCalls = append(a.realtimeStartCalls, workspaceChatRealtimeStartCall{
		WorkspaceRef: workspace.Ref, ThreadID: threadID, Generation: generation, Request: request,
	})
	afterStartRealtime := a.afterStartRealtime
	a.mu.Unlock()
	if afterStartRealtime != nil {
		afterStartRealtime()
	}
	return nil
}

func (a *workspaceChatNativeTestAgent) AppendNativeRealtimeText(_ context.Context, workspace Workspace, threadID string, generation uint64, text string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.realtimeAppendCalls = append(a.realtimeAppendCalls, workspaceChatRealtimeAppendCall{
		WorkspaceRef: workspace.Ref, ThreadID: threadID, Generation: generation, Text: text,
	})
	return nil
}

func (a *workspaceChatNativeTestAgent) StopNativeRealtime(_ context.Context, workspace Workspace, threadID string, generation uint64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.realtimeStopCalls = append(a.realtimeStopCalls, workspaceChatRealtimeStopCall{
		WorkspaceRef: workspace.Ref, ThreadID: threadID, Generation: generation,
	})
	return a.realtimeStopErr
}

func (a *workspaceChatNativeTestAgent) emit(threadID string, event NativeEventEnvelope) error {
	a.mu.Lock()
	events := a.events[threadID]
	a.mu.Unlock()
	if events == nil {
		return ErrNativeThreadNotFound
	}
	if event.ConnectionGeneration == 0 {
		a.mu.Lock()
		event.ConnectionGeneration = a.generations[threadID]
		a.mu.Unlock()
		if event.ConnectionGeneration == 0 {
			event.ConnectionGeneration = 1
		}
	}
	select {
	case events <- event:
		return nil
	case <-time.After(2 * time.Second):
		return fmt.Errorf("timed out emitting native event")
	}
}

func (a *workspaceChatNativeTestAgent) setActiveTurn(threadID, turnID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	snapshot := a.snapshots[threadID]
	if turnID == "" {
		snapshot.ActiveTurn = nil
	} else {
		snapshot.ActiveTurn = &NativeActiveTurn{ID: turnID, StartedAt: time.Now()}
	}
	a.snapshots[threadID] = snapshot
}

type workspaceChatUnusedAgentSession struct {
	events chan Event
}

func (s *workspaceChatUnusedAgentSession) Send(string, string, []ImageAttachment, []FileAttachment) error {
	return nil
}

func (s *workspaceChatUnusedAgentSession) RespondPermission(string, PermissionResult) error {
	return nil
}
func (s *workspaceChatUnusedAgentSession) Events() <-chan Event     { return s.events }
func (s *workspaceChatUnusedAgentSession) CurrentSessionID() string { return "unused" }
func (s *workspaceChatUnusedAgentSession) Alive() bool              { return true }
func (s *workspaceChatUnusedAgentSession) Close() error             { return nil }

func cloneWorkspaceChatSnapshot(snapshot NativeConversationSnapshot) NativeConversationSnapshot {
	clone := snapshot
	clone.Status = append(json.RawMessage(nil), snapshot.Status...)
	clone.Usage = append(json.RawMessage(nil), snapshot.Usage...)
	clone.Capabilities = make(map[string]CapabilityStatus, len(snapshot.Capabilities))
	for key, value := range snapshot.Capabilities {
		clone.Capabilities[key] = value
	}
	if snapshot.ActiveTurn != nil {
		active := *snapshot.ActiveTurn
		clone.ActiveTurn = &active
	}
	clone.PendingInteractions = append([]NativeInteraction(nil), snapshot.PendingInteractions...)
	return clone
}

type workspaceChatTestFixture struct {
	service    *WorkspaceChatService
	repository *workspaceChatMemoryRepository
	agent      *workspaceChatNativeTestAgent
	workspaceA Workspace
	workspaceB Workspace
	threadA    string
	threadB    string
}

func newWorkspaceChatTestFixture(t *testing.T) *workspaceChatTestFixture {
	t.Helper()
	workspaceA := Workspace{
		Ref: "workspace-a", DeviceID: "device-a", DeviceName: "Mac A", ProjectID: "project-a", ProjectName: "Project A", RootName: "Root A",
		RootPath: t.TempDir(), Available: true, Order: 2,
	}
	workspaceB := Workspace{
		Ref: "workspace-b", DeviceID: "device-a", DeviceName: "Mac A", ProjectID: "project-b", ProjectName: "Project B", RootName: "Root B",
		RootPath: t.TempDir(), Available: true, Order: 1,
	}
	now := time.Now()
	threadA := "thread-a"
	threadB := "thread-b"
	modeDefault := "default"
	modePlan := "plan"
	agent := &workspaceChatNativeTestAgent{
		workspaces: []Workspace{workspaceA, workspaceB},
		snapshots: map[string]NativeConversationSnapshot{
			threadA: {
				Thread:       NativeThread{ID: threadA, Cwd: workspaceA.RootPath, Preview: "first thread", CreatedAt: now.Add(-time.Hour), UpdatedAt: now},
				Settings:     NativeThreadSettings{Revision: "settings-1", Model: "gpt-5", Effort: "low", PermissionProfile: "workspace-write"},
				Capabilities: map[string]CapabilityStatus{"settings": {Supported: true}, "realtime": {Supported: true}},
				DeepLink:     workspaceChatTestDeepLink(threadA),
			},
			threadB: {
				Thread:       NativeThread{ID: threadB, Cwd: workspaceB.RootPath, Preview: "other workspace", CreatedAt: now.Add(-time.Hour), UpdatedAt: now},
				Settings:     NativeThreadSettings{Revision: "settings-1", Model: "gpt-5", Effort: "low"},
				Capabilities: map[string]CapabilityStatus{"settings": {Supported: true}, "realtime": {Supported: true}},
				DeepLink:     workspaceChatTestDeepLink(threadB),
			},
		},
		events: map[string]chan NativeEventEnvelope{
			threadA: make(chan NativeEventEnvelope, 64),
			threadB: make(chan NativeEventEnvelope, 64),
		},
		generations: map[string]uint64{threadA: 1, threadB: 1},
		turns: map[string]NativeTurnPage{
			threadA: {Data: []NativeTurn{{ID: "turn-history", Status: "completed"}}, NextCursor: "turns-next"},
		},
		items: map[string]NativeItemPage{
			threadA + "\x00": {
				Data: []NativeItem{
					{TurnID: "turn-history", Item: json.RawMessage(`{"type":"agentMessage","text":"hello"}`)},
					{TurnID: "turn-other", Item: json.RawMessage(`{"type":"reasoning","summary":"all turns"}`)},
				},
				NextCursor: "all-items-next",
			},
			threadA + "\x00turn-history": {
				Data:       []NativeItem{{TurnID: "turn-history", Item: json.RawMessage(`{"type":"agentMessage","text":"hello"}`)}},
				NextCursor: "items-next",
			},
		},
		catalog: NativeRuntimeCatalog{
			Capabilities: map[string]CapabilityStatus{"settings": {Supported: true}, "realtime": {Supported: true}},
			Models: []NativeModelOption{{
				ID: "gpt-5", Model: "gpt-5", DisplayName: "GPT-5", Default: true,
				ReasoningEfforts: []ReasoningEffortOption{{Effort: "low"}, {Effort: "high"}},
				ServiceTiers:     []ServiceTierOption{{ID: "priority", Name: "Priority"}},
			}},
			Modes:         []NativeCollaborationModeOption{{Name: "Default", Mode: &modeDefault}, {Name: "Plan", Mode: &modePlan}},
			Permissions:   []NativePermissionProfile{{ID: "workspace-write", Allowed: true}},
			Personalities: []string{"friendly"},
			Summaries:     []string{"concise"},
		},
	}
	repository := newWorkspaceChatMemoryRepository()
	service, err := NewWorkspaceChatService(workspaceChatTestDependencies(agent), repository, []string{"web", "wecom"})
	if err != nil {
		t.Fatalf("NewWorkspaceChatService() error = %v", err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Errorf("WorkspaceChatService.Close() error = %v", err)
		}
	})
	return &workspaceChatTestFixture{
		service: service, repository: repository, agent: agent,
		workspaceA: workspaceA, workspaceB: workspaceB, threadA: threadA, threadB: threadB,
	}
}

func workspaceChatTestDependencies(agent *workspaceChatNativeTestAgent) WorkspaceChatDependencies {
	return WorkspaceChatDependencies{Catalog: agent, Validator: agent, Backend: agent, Settings: agent, Turns: agent, Realtime: agent, I18n: NewI18n(LangEnglish)}
}

func workspaceChatTestDeepLink(threadID string) string {
	return "native-test://conversation/" + threadID + "?signed=fixture"
}

type workspaceChatBlockingPlatform struct {
	started chan struct{}
	release chan struct{}
}

func (p *workspaceChatBlockingPlatform) Name() string               { return "blocking-test" }
func (p *workspaceChatBlockingPlatform) Start(MessageHandler) error { return nil }
func (p *workspaceChatBlockingPlatform) Send(context.Context, any, string) error {
	select {
	case <-p.started:
	default:
		close(p.started)
	}
	<-p.release
	return nil
}
func (p *workspaceChatBlockingPlatform) Stop() error { return nil }
func (p *workspaceChatBlockingPlatform) Reply(context.Context, any, string) error {
	return nil
}

type workspaceChatDeliveryOrderRepository struct {
	*workspaceChatMemoryRepository
	mu                   sync.Mutex
	closedBeforeTerminal bool
}

func (r *workspaceChatDeliveryOrderRepository) Close() error {
	r.workspaceChatMemoryRepository.mu.Lock()
	for _, delivery := range r.deliveries {
		if delivery.Status == "pending" {
			r.mu.Lock()
			r.closedBeforeTerminal = true
			r.mu.Unlock()
		}
	}
	r.workspaceChatMemoryRepository.mu.Unlock()
	return r.workspaceChatMemoryRepository.Close()
}

func (r *workspaceChatDeliveryOrderRepository) wasClosedBeforeTerminal() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closedBeforeTerminal
}

func TestWorkspaceChatDraftMaterializesOnFirstTurn(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	ctx := context.Background()
	draft, err := fixture.service.CreateDraft(ctx, "web:admin", fixture.workspaceA.Ref)
	if err != nil {
		t.Fatalf("CreateDraft() error = %v", err)
	}
	fixture.agent.mu.Lock()
	startConversationCalls := fixture.agent.startConversationCalls
	fixture.agent.mu.Unlock()
	if startConversationCalls != 0 {
		t.Fatalf("StartNativeConversation calls before first turn = %d, want 0", startConversationCalls)
	}

	conversation := ConversationRef{Kind: ConversationKindDraft, ID: draft.ID}
	subscription, err := fixture.service.Subscribe(ctx, "web:admin", fixture.workspaceA.Ref, conversation, "", 0)
	if err != nil {
		t.Fatalf("Subscribe(draft) error = %v", err)
	}
	defer subscription.Cancel()
	if len(subscription.Initial) != 2 || subscription.Initial[0].Type != "subscribed" || subscription.Initial[1].Type != "snapshot" {
		t.Fatalf("draft initial events = %#v", subscription.Initial)
	}
	if strings.Contains(string(subscription.Initial[1].Payload), "codex://threads/") {
		t.Fatalf("draft snapshot exposed a deep link before materialization: %s", subscription.Initial[1].Payload)
	}

	result, err := fixture.service.StartTurn(ctx, "web:admin", "request-first", fixture.workspaceA.Ref, conversation,
		[]NativeUserInput{{Type: "text", Text: "first prompt"}}, NativeThreadSettingsPatch{})
	if err != nil {
		t.Fatalf("StartTurn(draft) error = %v", err)
	}
	if result.TurnID == "" {
		t.Fatal("StartTurn(draft) returned an empty turn id")
	}

	select {
	case event := <-subscription.Events:
		if event.Type != "thread_materialized" || event.ThreadID == "" || event.TurnID != result.TurnID {
			t.Fatalf("materialization event = %#v", event)
		}
		if event.Epoch != subscription.Initial[0].Epoch || event.Sequence != 1 {
			t.Fatalf("materialization stream position = %s/%d", event.Epoch, event.Sequence)
		}
		if event.Conversation != (ConversationRef{Kind: ConversationKindThread, ID: event.ThreadID}) {
			t.Fatalf("materialization conversation = %#v", event.Conversation)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for thread_materialized")
	}

	persistedDraft, err := fixture.repository.GetDraft(ctx, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persistedDraft == nil || persistedDraft.State != "materialized" || persistedDraft.ThreadID == "" {
		t.Fatalf("persisted draft = %#v", persistedDraft)
	}
	selection, err := fixture.repository.GetSelection(ctx, "web:admin")
	if err != nil {
		t.Fatal(err)
	}
	wantConversation := ConversationRef{Kind: ConversationKindThread, ID: persistedDraft.ThreadID}
	if selection == nil || selection.Conversation != wantConversation {
		t.Fatalf("selection after materialization = %#v, want %#v", selection, wantConversation)
	}
	submission := fixture.repository.submission("request-first")
	if submission.Status != "accepted" || submission.NativeTurnID != result.TurnID || len(submission.InputJSON) != 0 {
		t.Fatalf("materialized submission = %#v", submission)
	}
	fixture.repository.mu.Lock()
	markAccepted := fixture.repository.markAccepted
	fixture.repository.mu.Unlock()
	if markAccepted != 0 {
		t.Fatalf("draft materialization used a separate submission commit: %d calls", markAccepted)
	}
}

func TestWorkspaceChatDraftMaterializationFailureDoesNotStartTwice(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	draft, err := fixture.service.CreateDraft(context.Background(), "web:admin", fixture.workspaceA.Ref)
	if err != nil {
		t.Fatal(err)
	}
	fixture.repository.mu.Lock()
	fixture.repository.materializeErr = errors.New("injected materialization commit failure")
	fixture.repository.mu.Unlock()
	conversation := ConversationRef{Kind: ConversationKindDraft, ID: draft.ID}
	input := []NativeUserInput{{Type: "text", Text: "must not be replayed"}}

	_, err = fixture.service.StartTurn(context.Background(), "web:admin", "request-uncertain", fixture.workspaceA.Ref, conversation, input, NativeThreadSettingsPatch{})
	if !errors.Is(err, ErrWorkspaceDraftMaterializationUncertain) {
		t.Fatalf("StartTurn(materialization failure) error = %v", err)
	}
	submission := fixture.repository.submission("request-uncertain")
	if submission.Status != "needs_retry" || len(submission.InputJSON) != 0 || submission.ThreadID != "" || submission.NativeTurnID != "" {
		t.Fatalf("uncertain materialization submission = %#v", submission)
	}
	fixture.repository.mu.Lock()
	markAccepted := fixture.repository.markAccepted
	fixture.repository.mu.Unlock()
	if markAccepted != 0 {
		t.Fatalf("failed draft materialization committed submission separately: %d calls", markAccepted)
	}

	_, err = fixture.service.StartTurn(context.Background(), "web:admin", "request-second", fixture.workspaceA.Ref, conversation, input, NativeThreadSettingsPatch{})
	if !errors.Is(err, ErrWorkspaceDraftMaterializationUncertain) {
		t.Fatalf("second StartTurn() error = %v", err)
	}
	fixture.agent.mu.Lock()
	startConversationCalls := fixture.agent.startConversationCalls
	startTurnCalls := len(fixture.agent.startTurnCalls)
	fixture.agent.mu.Unlock()
	if startConversationCalls != 1 || startTurnCalls != 1 {
		t.Fatalf("native starts after uncertain materialization = conversations:%d turns:%d", startConversationCalls, startTurnCalls)
	}
	if second := fixture.repository.submission("request-second"); second.RequestID != "" {
		t.Fatalf("second submission was persisted: %#v", second)
	}
}

func TestWorkspaceChatListItemsAllowsThreadWidePagination(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	page, err := fixture.service.ListItems(context.Background(), fixture.workspaceA.Ref, fixture.threadA, "", NativePageRequest{Cursor: "all-items", Limit: 11, SortDirection: "asc"})
	if err != nil {
		t.Fatalf("ListItems(thread-wide) error = %v", err)
	}
	if len(page.Data) != 2 || page.NextCursor != "all-items-next" {
		t.Fatalf("ListItems(thread-wide) = %#v", page)
	}
	fixture.agent.mu.Lock()
	turnID := fixture.agent.lastItemTurnID
	request := fixture.agent.lastItemPage
	fixture.agent.mu.Unlock()
	if turnID != "" || request != (NativePageRequest{Cursor: "all-items", Limit: 11, SortDirection: "asc"}) {
		t.Fatalf("backend thread-wide item request = turn:%q page:%#v", turnID, request)
	}
}

func TestWorkspaceChatAcceptedTurnPersistenceFailureClearsInput(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	fixture.repository.mu.Lock()
	fixture.repository.markAcceptedErr = errors.New("injected accepted turn persistence failure")
	fixture.repository.mu.Unlock()

	conversation := ConversationRef{Kind: ConversationKindThread, ID: fixture.threadA}
	_, err := fixture.service.StartTurn(context.Background(), "web:admin", "request-accepted-error", fixture.workspaceA.Ref,
		conversation, []NativeUserInput{{Type: "text", Text: "must be cleared after acceptance"}}, NativeThreadSettingsPatch{})
	if err == nil || !strings.Contains(err.Error(), "persist accepted turn") {
		t.Fatalf("StartTurn() error = %v", err)
	}
	submission := fixture.repository.submission("request-accepted-error")
	if submission.Status != "needs_retry" || len(submission.InputJSON) != 0 {
		t.Fatalf("accepted submission after persistence failure = %#v", submission)
	}
}

func TestWorkspaceChatRunningTurnRequiresExplicitSteer(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	fixture.agent.setActiveTurn(fixture.threadA, "turn-active")
	conversation := ConversationRef{Kind: ConversationKindThread, ID: fixture.threadA}
	input := []NativeUserInput{{Type: "text", Text: "continue"}}

	_, err := fixture.service.StartTurn(context.Background(), "web:admin", "request-start", fixture.workspaceA.Ref, conversation, input, NativeThreadSettingsPatch{})
	if !errors.Is(err, ErrWorkspaceTurnRunning) {
		t.Fatalf("StartTurn(active) error = %v, want ErrWorkspaceTurnRunning", err)
	}
	fixture.agent.mu.Lock()
	startCalls := len(fixture.agent.startTurnCalls)
	fixture.agent.mu.Unlock()
	if startCalls != 0 {
		t.Fatalf("native start calls = %d, want 0", startCalls)
	}

	_, err = fixture.service.SteerTurn(context.Background(), "web:admin", "request-stale", fixture.workspaceA.Ref, fixture.threadA, "turn-old", input)
	if !errors.Is(err, ErrWorkspaceStaleTurn) {
		t.Fatalf("SteerTurn(stale) error = %v, want ErrWorkspaceStaleTurn", err)
	}
	_, err = fixture.service.SteerTurn(context.Background(), "web:admin", "request-missing", fixture.workspaceA.Ref, fixture.threadA, "", input)
	if !errors.Is(err, ErrWorkspaceStaleTurn) {
		t.Fatalf("SteerTurn(missing expected id) error = %v, want ErrWorkspaceStaleTurn", err)
	}

	result, err := fixture.service.SteerTurn(context.Background(), "web:admin", "request-steer", fixture.workspaceA.Ref, fixture.threadA, "turn-active", input)
	if err != nil {
		t.Fatalf("SteerTurn(valid) error = %v", err)
	}
	if result.TurnID != "turn-active" {
		t.Fatalf("SteerTurn(valid) turn = %q", result.TurnID)
	}
	fixture.agent.mu.Lock()
	steerCalls := append([]workspaceChatSteerCall(nil), fixture.agent.steerCalls...)
	fixture.agent.mu.Unlock()
	if len(steerCalls) != 1 || steerCalls[0].ExpectedTurnID != "turn-active" {
		t.Fatalf("native steer calls = %#v", steerCalls)
	}
}

func TestWorkspaceChatRejectsThreadFromAnotherWorkspace(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	readResult := make(chan error, 1)
	go func() {
		_, err := fixture.service.ReadThread(context.Background(), fixture.workspaceA.Ref, fixture.threadB)
		readResult <- err
	}()
	var err error
	select {
	case err = <-readResult:
	case <-time.After(2 * time.Second):
		t.Fatal("ReadThread(cross workspace) hung waiting for a native pump attempt")
	}
	if !errors.Is(err, ErrNativeThreadNotFound) || !strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("ReadThread(cross workspace) error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = fixture.service.SelectConversation(ctx, "web:admin", fixture.workspaceA.Ref,
		ConversationRef{Kind: ConversationKindThread, ID: fixture.threadB})
	if !errors.Is(err, ErrNativeThreadNotFound) {
		t.Fatalf("SelectConversation(cross workspace) error = %v", err)
	}
}

func TestWorkspaceChatRejectedThreadRetiresProvisionalActor(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	conversation := ConversationRef{Kind: ConversationKindThread, ID: fixture.threadB}
	rejectedActor := fixture.service.actor(fixture.workspaceA, conversation)
	if rejectedActor == nil {
		t.Fatal("provisional actor was not created")
	}

	if _, err := fixture.service.ReadThread(context.Background(), fixture.workspaceA.Ref, fixture.threadB); !errors.Is(err, ErrNativeThreadNotFound) {
		t.Fatalf("ReadThread(cross workspace) error = %v, want ErrNativeThreadNotFound", err)
	}
	if actor := fixture.service.actorForThread(fixture.workspaceA.Ref, fixture.threadB); actor != nil {
		t.Fatalf("rejected actor remained registered: %p", actor)
	}
	if done := rejectedActor.nativePump.activeDone(); done != nil {
		t.Fatal("rejected actor native pump remained active")
	}
	if err := rejectedActor.nativePump.Start(context.Background(), func(context.Context) {}); !errors.Is(err, errTurnLifecyclePumpClosed) {
		t.Fatalf("rejected actor native pump restart error = %v, want %v", err, errTurnLifecyclePumpClosed)
	}

	fixture.agent.mu.Lock()
	snapshot := fixture.agent.snapshots[fixture.threadB]
	snapshot.Thread.Cwd = fixture.workspaceA.RootPath
	fixture.agent.snapshots[fixture.threadB] = snapshot
	fixture.agent.mu.Unlock()
	if _, err := fixture.service.ReadThread(context.Background(), fixture.workspaceA.Ref, fixture.threadB); err != nil {
		t.Fatalf("ReadThread(valid retry) error = %v", err)
	}
	if actor := fixture.service.actorForThread(fixture.workspaceA.Ref, fixture.threadB); actor == nil || actor == rejectedActor {
		t.Fatalf("valid retry actor = %p, rejected actor = %p", actor, rejectedActor)
	}
}

func TestWorkspaceChatRealtimeAppendAndStopRevalidateResolvedWorkspace(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	if err := fixture.service.StartRealtime(context.Background(), "owner-a", fixture.workspaceA.Ref, fixture.threadA, NativeRealtimeStartRequest{SDP: "v=0"}); err != nil {
		t.Fatalf("StartRealtime() error = %v", err)
	}

	fixture.agent.mu.Lock()
	for index := range fixture.agent.workspaces {
		if fixture.agent.workspaces[index].Ref == fixture.workspaceA.Ref {
			fixture.agent.workspaces[index].RootPath = fixture.workspaceB.RootPath
		}
	}
	resolveCallsBefore := len(fixture.agent.resolveWorkspaceCalls)
	fixture.agent.mu.Unlock()

	if err := fixture.service.AppendRealtimeText(context.Background(), "owner-a", fixture.workspaceA.Ref, fixture.threadA, "more context"); !errors.Is(err, ErrNativeThreadNotFound) {
		t.Fatalf("AppendRealtimeText(remapped workspace) error = %v, want ErrNativeThreadNotFound", err)
	}
	if err := fixture.service.StopRealtime(context.Background(), "owner-a", fixture.workspaceA.Ref, fixture.threadA); !errors.Is(err, ErrNativeThreadNotFound) {
		t.Fatalf("StopRealtime(remapped workspace) error = %v, want ErrNativeThreadNotFound", err)
	}

	fixture.agent.mu.Lock()
	resolveCallsAfter := len(fixture.agent.resolveWorkspaceCalls)
	appendCalls := len(fixture.agent.realtimeAppendCalls)
	stopCalls := len(fixture.agent.realtimeStopCalls)
	fixture.agent.mu.Unlock()
	if resolveCallsAfter-resolveCallsBefore != 4 {
		t.Fatalf("realtime operations resolved and authoritatively revalidated workspace %d times, want 4", resolveCallsAfter-resolveCallsBefore)
	}
	if appendCalls != 0 || stopCalls != 0 {
		t.Fatalf("realtime mutations after workspace remap = append %d, stop %d; want 0, 0", appendCalls, stopCalls)
	}
}

func TestWorkspaceChatRealtimeRejectsForgedWorkspaceAndCrossDirectoryThread(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	fixture.agent.mu.Lock()
	resolveCallsBefore := len(fixture.agent.resolveWorkspaceCalls)
	fixture.agent.mu.Unlock()

	if err := fixture.service.AppendRealtimeText(context.Background(), "owner-a", "forged-workspace", fixture.threadA, "more context"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("AppendRealtimeText(forged workspace) error = %v, want ErrWorkspaceNotFound", err)
	}
	if err := fixture.service.StopRealtime(context.Background(), "owner-a", "forged-workspace", fixture.threadA); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("StopRealtime(forged workspace) error = %v, want ErrWorkspaceNotFound", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := fixture.service.AppendRealtimeText(ctx, "owner-a", fixture.workspaceA.Ref, fixture.threadB, "more context"); !errors.Is(err, ErrNativeThreadNotFound) {
		t.Fatalf("AppendRealtimeText(cross workspace thread) error = %v, want ErrNativeThreadNotFound", err)
	}
	if err := fixture.service.StopRealtime(ctx, "owner-a", fixture.workspaceA.Ref, fixture.threadB); !errors.Is(err, ErrNativeThreadNotFound) {
		t.Fatalf("StopRealtime(cross workspace thread) error = %v, want ErrNativeThreadNotFound", err)
	}

	fixture.agent.mu.Lock()
	resolveCallsAfter := len(fixture.agent.resolveWorkspaceCalls)
	appendCalls := len(fixture.agent.realtimeAppendCalls)
	stopCalls := len(fixture.agent.realtimeStopCalls)
	fixture.agent.mu.Unlock()
	if resolveCallsAfter-resolveCallsBefore != 6 {
		t.Fatalf("realtime rejection resolved and authoritatively revalidated workspace %d times, want 6", resolveCallsAfter-resolveCallsBefore)
	}
	if appendCalls != 0 || stopCalls != 0 {
		t.Fatalf("realtime mutations for forged references = append %d, stop %d; want 0, 0", appendCalls, stopCalls)
	}
}

func TestWorkspaceChatRealtimeStartRejectsForgedWorkspaceAndCrossDirectoryThread(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	request := NativeRealtimeStartRequest{SDP: "v=0"}
	if err := fixture.service.StartRealtime(context.Background(), "owner-a", "forged-workspace", fixture.threadA, request); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("StartRealtime(forged workspace) error = %v, want ErrWorkspaceNotFound", err)
	}
	if err := fixture.service.StartRealtime(context.Background(), "owner-a", fixture.workspaceA.Ref, fixture.threadB, request); !errors.Is(err, ErrNativeThreadNotFound) {
		t.Fatalf("StartRealtime(cross workspace thread) error = %v, want ErrNativeThreadNotFound", err)
	}
	fixture.agent.mu.Lock()
	startCalls := len(fixture.agent.realtimeStartCalls)
	fixture.agent.mu.Unlock()
	if startCalls != 0 {
		t.Fatalf("native realtime starts for forged references = %d, want 0", startCalls)
	}
}

func TestWorkspaceChatNativePumpReconnectRestoresGeneration(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	if _, err := fixture.service.ReadThread(context.Background(), fixture.workspaceA.Ref, fixture.threadA); err != nil {
		t.Fatalf("ReadThread() error = %v", err)
	}
	actor := fixture.service.actorForThread(fixture.workspaceA.Ref, fixture.threadA)
	if actor == nil {
		t.Fatal("thread actor was not created")
	}

	fixture.agent.mu.Lock()
	oldEvents := fixture.agent.events[fixture.threadA]
	fixture.agent.events[fixture.threadA] = make(chan NativeEventEnvelope, 64)
	fixture.agent.generations[fixture.threadA] = 2
	fixture.agent.mu.Unlock()
	close(oldEvents)

	deadline := time.Now().Add(2 * time.Second)
	for {
		actor.mu.Lock()
		active := actor.nativeActive
		actor.mu.Unlock()
		if !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native pump did not enter reconnect state")
		}
		time.Sleep(10 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	generation, err := fixture.service.waitNativePump(ctx, actor)
	if err != nil {
		t.Fatalf("waitNativePump(reconnect) error = %v", err)
	}
	if generation != 2 {
		t.Fatalf("reconnected generation = %d, want 2", generation)
	}
}

func TestWorkspaceChatCloseStopsNativePumpBeforeRepository(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	if _, err := fixture.service.ReadThread(context.Background(), fixture.workspaceA.Ref, fixture.threadA); err != nil {
		t.Fatalf("ReadThread() error = %v", err)
	}
	actor := fixture.service.actorForThread(fixture.workspaceA.Ref, fixture.threadA)
	done := actor.nativePump.activeDone()
	if done == nil {
		t.Fatal("native pump was not active")
	}

	if err := fixture.service.Close(); err != nil {
		t.Fatalf("WorkspaceChatService.Close() error = %v", err)
	}
	select {
	case <-done:
	default:
		t.Fatal("service close returned before native pump exited")
	}
	fixture.repository.mu.Lock()
	repositoryClosed := fixture.repository.closed
	fixture.repository.mu.Unlock()
	if !repositoryClosed {
		t.Fatal("repository remained open after native pump cleanup")
	}
	if err := actor.nativePump.Start(context.Background(), func(context.Context) {}); !errors.Is(err, errTurnLifecyclePumpClosed) {
		t.Fatalf("native pump restart after service close error = %v, want %v", err, errTurnLifecyclePumpClosed)
	}
}

func TestWorkspaceChatCloseFinalizesActorState(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	conversation := ConversationRef{Kind: ConversationKindThread, ID: fixture.threadA}
	result, err := fixture.service.StartTurn(context.Background(), "web:admin", "request-active", fixture.workspaceA.Ref, conversation,
		[]NativeUserInput{{Type: "text", Text: "run"}}, NativeThreadSettingsPatch{})
	if err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}
	if err := fixture.service.StartRealtime(context.Background(), "owner-a", fixture.workspaceA.Ref, fixture.threadA, NativeRealtimeStartRequest{SDP: "v=0"}); err != nil {
		t.Fatalf("StartRealtime() error = %v", err)
	}
	interaction := NativeInteraction{ID: "interaction-close", Kind: "item/commandExecution/requestApproval", ThreadID: fixture.threadA, TurnID: result.TurnID, RequestID: json.RawMessage(`42`), ConnectionGeneration: 1, Payload: json.RawMessage(`{"command":"go test"}`)}
	if err := fixture.repository.PutInteraction(context.Background(), WorkspaceChatInteractionRecord{Interaction: interaction, ConnectionGeneration: 1, Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	actor := fixture.service.actorForThread(fixture.workspaceA.Ref, fixture.threadA)
	actor.mu.Lock()
	actor.pending[interaction.ID] = interaction
	actor.pendingStart = &workspaceChatPendingStart{requestID: "request-unconfirmed"}
	actor.mu.Unlock()
	if err := fixture.repository.BeginSubmission(context.Background(), WorkspaceChatSubmission{
		RequestID: "request-unconfirmed", ClientID: "web:admin", WorkspaceRef: fixture.workspaceA.Ref,
		Conversation: conversation, ThreadID: fixture.threadA, Kind: "start", InputJSON: json.RawMessage(`[{"type":"text","text":"pending"}]`),
		Status: "pending", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	if err := fixture.service.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	fixture.agent.mu.Lock()
	interruptCalls := append([]workspaceChatInterruptCall(nil), fixture.agent.interruptCalls...)
	realtimeCalls := append([]workspaceChatRealtimeStopCall(nil), fixture.agent.realtimeStopCalls...)
	fixture.agent.mu.Unlock()
	if len(interruptCalls) != 1 || interruptCalls[0].TurnID != result.TurnID || interruptCalls[0].Generation != 1 {
		t.Fatalf("interrupt calls = %#v", interruptCalls)
	}
	if len(realtimeCalls) != 1 || realtimeCalls[0].ThreadID != fixture.threadA || realtimeCalls[0].Generation != 1 {
		t.Fatalf("realtime stop calls = %#v", realtimeCalls)
	}
	if submission := fixture.repository.submission("request-active"); submission.Status != "interrupted" {
		t.Fatalf("active submission status = %#v", submission)
	}
	if submission := fixture.repository.submission("request-unconfirmed"); submission.Status != "needs_retry" {
		t.Fatalf("unconfirmed submission status = %#v", submission)
	}
	if record, ok := fixture.repository.interaction(interaction.ID); !ok || record.Status != "cancelled" {
		t.Fatalf("closed interaction = %#v, exists=%v", record, ok)
	}
	if _, err := fixture.service.CreateDraft(context.Background(), "web:admin", fixture.workspaceA.Ref); !errors.Is(err, ErrWorkspaceChatClosed) {
		t.Fatalf("CreateDraft(after close) error = %v", err)
	}
}

func TestWorkspaceChatCloseWaitsForDeliveryTerminalWrite(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	repository := &workspaceChatDeliveryOrderRepository{workspaceChatMemoryRepository: fixture.repository}
	fixture.service.repo = repository
	platform := &workspaceChatBlockingPlatform{started: make(chan struct{}), release: make(chan struct{})}
	target := &workspaceChatDeliveryTarget{clientID: "wecom:user:user-1", requestID: "request-delivery", platform: platform, destination: "user-1"}
	fixture.service.deliverPlatform(fixture.workspaceA.Ref, ConversationRef{Kind: ConversationKindThread, ID: fixture.threadA}, target, "reply", "assistant")
	select {
	case <-platform.started:
	case <-time.After(2 * time.Second):
		t.Fatal("delivery worker did not start")
	}
	closeResult := make(chan error, 1)
	go func() { closeResult <- fixture.service.Close() }()
	select {
	case err := <-closeResult:
		t.Fatalf("Close() returned before delivery completed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	fixture.repository.mu.Lock()
	closedWhileBlocked := fixture.repository.closed
	fixture.repository.mu.Unlock()
	if closedWhileBlocked {
		t.Fatal("repository closed while delivery was blocked")
	}
	close(platform.release)
	if err := <-closeResult; err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if repository.wasClosedBeforeTerminal() {
		t.Fatal("repository Close ran before delivery terminal state was persisted")
	}
	fixture.repository.mu.Lock()
	before := len(fixture.repository.deliveries)
	var status string
	for _, delivery := range fixture.repository.deliveries {
		status = delivery.Status
	}
	fixture.repository.mu.Unlock()
	if status != "delivered" {
		t.Fatalf("delivery terminal status = %q", status)
	}
	fixture.service.deliverPlatform(fixture.workspaceA.Ref, ConversationRef{Kind: ConversationKindThread, ID: fixture.threadA}, target, "late", "assistant")
	fixture.repository.mu.Lock()
	after := len(fixture.repository.deliveries)
	fixture.repository.mu.Unlock()
	if after != before {
		t.Fatalf("deliveries after close = %d, want %d", after, before)
	}
}

func TestWorkspaceChatCloseCancelsBlockedActorOperation(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release)
	fixture.agent.mu.Lock()
	fixture.agent.startTurnStarted = started
	fixture.agent.startTurnRelease = release
	fixture.agent.mu.Unlock()

	turnResult := make(chan error, 1)
	go func() {
		_, err := fixture.service.StartTurn(context.Background(), "web:admin", "request-blocked-close", fixture.workspaceA.Ref,
			ConversationRef{Kind: ConversationKindThread, ID: fixture.threadA},
			[]NativeUserInput{{Type: "text", Text: "wait for shutdown"}}, NativeThreadSettingsPatch{})
		turnResult <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("native turn operation did not block")
	}

	closeResult := make(chan error, 1)
	go func() { closeResult <- fixture.service.Close() }()
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close() remained blocked behind an active actor operation")
	}
	select {
	case err := <-turnResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked StartTurn() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked actor operation was not cancelled")
	}
}

func TestWorkspaceChatCloseAggregatesNativeErrorsAndContinuesCleanup(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	interruptErr := errors.New("injected interrupt failure")
	realtimeErr := errors.New("injected realtime stop failure")
	fixture.agent.setActiveTurn(fixture.threadA, "turn-close-error")
	if _, err := fixture.service.ReadThread(context.Background(), fixture.workspaceA.Ref, fixture.threadA); err != nil {
		t.Fatal(err)
	}
	actor := fixture.service.actorForThread(fixture.workspaceA.Ref, fixture.threadA)
	actor.mu.Lock()
	actor.realtime = true
	actor.submissions["turn-close-error"] = []string{"request-close-error"}
	actor.mu.Unlock()
	conversation := ConversationRef{Kind: ConversationKindThread, ID: fixture.threadA}
	if err := fixture.repository.BeginSubmission(context.Background(), WorkspaceChatSubmission{
		RequestID: "request-close-error", ClientID: "web:admin", WorkspaceRef: fixture.workspaceA.Ref, Conversation: conversation,
		ThreadID: fixture.threadA, Kind: "start", InputJSON: json.RawMessage(`[{"type":"text","text":"accepted"}]`), Status: "pending", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repository.MarkSubmissionAccepted(context.Background(), "request-close-error", fixture.threadA, "turn-close-error"); err != nil {
		t.Fatal(err)
	}
	fixture.agent.mu.Lock()
	fixture.agent.interruptErr = interruptErr
	fixture.agent.realtimeStopErr = realtimeErr
	fixture.agent.mu.Unlock()

	err := fixture.service.Close()
	if !errors.Is(err, interruptErr) || !errors.Is(err, realtimeErr) {
		t.Fatalf("Close() error = %v", err)
	}
	if submission := fixture.repository.submission("request-close-error"); submission.Status != "needs_retry" {
		t.Fatalf("submission after failed interrupt = %#v", submission)
	}
	fixture.repository.mu.Lock()
	repositoryClosed := fixture.repository.closed
	fixture.repository.mu.Unlock()
	if !repositoryClosed {
		t.Fatal("repository was not closed after native cleanup errors")
	}
}

func TestWorkspaceChatEventReplayAndGapResync(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	draft, err := fixture.service.CreateDraft(context.Background(), "web:admin", fixture.workspaceA.Ref)
	if err != nil {
		t.Fatal(err)
	}
	conversation := ConversationRef{Kind: ConversationKindDraft, ID: draft.ID}
	initial, err := fixture.service.Subscribe(context.Background(), "web:admin", fixture.workspaceA.Ref, conversation, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	epoch := initial.Initial[0].Epoch
	initial.Cancel()
	actor := fixture.service.actor(fixture.workspaceA, conversation)
	fixture.service.publish(actor, WorkspaceChatEvent{Type: "marker", Payload: json.RawMessage(`{"index":1}`)})

	replayed, err := fixture.service.Subscribe(context.Background(), "web:admin", fixture.workspaceA.Ref, conversation, epoch, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer replayed.Cancel()
	if len(replayed.Initial) != 2 || replayed.Initial[1].Type != "marker" || replayed.Initial[1].Sequence != 1 {
		t.Fatalf("replayed events = %#v", replayed.Initial)
	}
	future, err := fixture.service.Subscribe(context.Background(), "web:admin", fixture.workspaceA.Ref, conversation, epoch, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(future.Initial) != 3 || future.Initial[0].Sequence != 1 || future.Initial[1].Type != "resync_required" || future.Initial[2].Type != "snapshot" {
		t.Fatalf("future cursor resync events = %#v", future.Initial)
	}
	future.Cancel()

	for index := 2; index <= workspaceChatReplayLimit+2; index++ {
		fixture.service.publish(actor, WorkspaceChatEvent{Type: "marker"})
	}
	resynced, err := fixture.service.Subscribe(context.Background(), "web:admin", fixture.workspaceA.Ref, conversation, epoch, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer resynced.Cancel()
	if len(resynced.Initial) != 3 || resynced.Initial[1].Type != "resync_required" || resynced.Initial[2].Type != "snapshot" {
		t.Fatalf("resync events = %#v", resynced.Initial)
	}
	if resynced.Initial[1].Sequence != workspaceChatReplayLimit+2 || resynced.Initial[2].Sequence != workspaceChatReplayLimit+2 {
		t.Fatalf("resync sequence = %d/%d", resynced.Initial[1].Sequence, resynced.Initial[2].Sequence)
	}
	if resynced.Initial[0].Sequence != workspaceChatReplayLimit+2 {
		t.Fatalf("resync subscribed baseline = %d", resynced.Initial[0].Sequence)
	}

	newEpoch, err := fixture.service.Subscribe(context.Background(), "web:admin", fixture.workspaceA.Ref, conversation, "old-epoch", 99_999)
	if err != nil {
		t.Fatal(err)
	}
	defer newEpoch.Cancel()
	if len(newEpoch.Initial) != 3 || newEpoch.Initial[0].Sequence != workspaceChatReplayLimit+2 || newEpoch.Initial[1].Sequence != workspaceChatReplayLimit+2 || newEpoch.Initial[2].Sequence != workspaceChatReplayLimit+2 {
		t.Fatalf("new epoch baseline events = %#v", newEpoch.Initial)
	}
	fixture.service.publish(actor, WorkspaceChatEvent{Type: "marker"})
	select {
	case event := <-newEpoch.Events:
		if event.Sequence != workspaceChatReplayLimit+3 {
			t.Fatalf("event after epoch reset sequence = %d", event.Sequence)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event after epoch reset")
	}
}

func TestWorkspaceChatInteractionOnlyAcceptsDeclaredDecision(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	fixture.agent.mu.Lock()
	fixture.agent.generations[fixture.threadA] = 9
	fixture.agent.mu.Unlock()
	conversation := ConversationRef{Kind: ConversationKindThread, ID: fixture.threadA}
	subscription, err := fixture.service.Subscribe(context.Background(), "web:admin", fixture.workspaceA.Ref, conversation, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Cancel()
	event := NativeEventEnvelope{
		Method: "item/commandExecution/requestApproval", ThreadID: fixture.threadA, TurnID: "turn-approval", ItemID: "item-approval",
		RequestID: json.RawMessage(`77`), ConnectionGeneration: 9, AllowedDecisions: testNativeStringDecisions("allow", "deny"),
		Payload: json.RawMessage(`{"command":"go test ./core"}`), OccurredAt: time.Now(),
	}
	if err := fixture.agent.emit(fixture.threadA, event); err != nil {
		t.Fatal(err)
	}

	var record WorkspaceChatInteractionRecord
	if !eventuallyWorkspaceChat(2*time.Second, func() bool {
		var ok bool
		record, ok = fixture.repository.firstPendingInteraction()
		return ok
	}) {
		t.Fatal("native interaction was not persisted")
	}
	if record.Interaction.ConnectionGeneration != 9 || string(record.Interaction.RequestID) != "77" {
		t.Fatalf("persisted interaction routing = %#v", record.Interaction)
	}

	err = fixture.service.RespondInteraction(context.Background(), fixture.workspaceA.Ref, fixture.threadA, record.Interaction.ID, json.RawMessage(`{"decision":"later"}`))
	if err == nil || !strings.Contains(err.Error(), "not declared") {
		t.Fatalf("RespondInteraction(undeclared) error = %v", err)
	}
	fixture.agent.mu.Lock()
	responseCalls := len(fixture.agent.interactionResponses)
	fixture.agent.mu.Unlock()
	if responseCalls != 0 {
		t.Fatalf("native interaction responses after rejection = %d", responseCalls)
	}

	err = fixture.service.RespondInteraction(context.Background(), fixture.workspaceA.Ref, fixture.threadA, record.Interaction.ID, json.RawMessage(`{"decision":"allow"}`))
	if err != nil {
		t.Fatalf("RespondInteraction(allowed) error = %v", err)
	}
	fixture.agent.mu.Lock()
	calls := append([]workspaceChatInteractionResponseCall(nil), fixture.agent.interactionResponses...)
	fixture.agent.mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("native interaction response calls = %d", len(calls))
	}
	call := calls[0]
	if call.Generation != 9 || string(call.RequestID) != "77" || string(call.Response) != `{"decision":"allow"}` {
		t.Fatalf("native interaction response = %#v", call)
	}
	resolved, ok := fixture.repository.interaction(record.Interaction.ID)
	if !ok || resolved.Status != "resolved" {
		t.Fatalf("resolved interaction = %#v, exists=%v", resolved, ok)
	}
}

func TestWorkspaceChatInteractionAcceptsOnlyExactStructuredDecision(t *testing.T) {
	allowed := json.RawMessage(`{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["git","status"]}}`)
	interaction := NativeInteraction{AllowedDecisions: []json.RawMessage{allowed}}
	if err := validateNativeInteractionResponse(interaction, json.RawMessage(`{"decision":{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["git","status"]}}}`)); err != nil {
		t.Fatalf("matching structured decision error = %v", err)
	}
	if err := validateNativeInteractionResponse(interaction, json.RawMessage(`{"decision":{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["git","push"]}}}`)); err == nil {
		t.Fatal("undeclared structured decision was accepted")
	}
}

func TestNativeInteractionFromEventPreservesPermissionsMethod(t *testing.T) {
	interaction := nativeInteractionFromEvent(NativeEventEnvelope{
		Method: "item/permissions/requestApproval", ThreadID: "thread-permissions", TurnID: "turn-permissions",
		RequestID: json.RawMessage(`88`), ConnectionGeneration: 3,
		Payload: json.RawMessage(`{"permissions":[{"type":"filesystem","path":"/project"}]}`), OccurredAt: time.Now(),
	})
	if interaction == nil || interaction.Kind != "item/permissions/requestApproval" {
		t.Fatalf("permissions interaction = %#v", interaction)
	}
	if err := validateNativeInteractionResponse(*interaction, json.RawMessage(`{"permissions":[{"type":"filesystem","path":"/project"}]}`)); err != nil {
		t.Fatalf("validate permissions response error = %v", err)
	}
}

func TestNativeInteractionFromEventRejectsSimilarUnknownMethod(t *testing.T) {
	interaction := nativeInteractionFromEvent(NativeEventEnvelope{
		Method:    "vendor/requestApprovalHint",
		ThreadID:  "thread-unknown",
		RequestID: json.RawMessage(`"request-unknown"`),
	})
	if interaction != nil {
		t.Fatalf("unknown method produced interaction: %#v", interaction)
	}
}

func TestWorkspaceChatNativeInteractionKeepsBackendIDThroughResolution(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	subscription, err := fixture.service.Subscribe(context.Background(), "web:admin", fixture.workspaceA.Ref,
		ConversationRef{Kind: ConversationKindThread, ID: fixture.threadA}, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Cancel()

	const interactionID = "ni_backend_1"
	request := NativeEventEnvelope{
		Method: "item/commandExecution/requestApproval", ThreadID: fixture.threadA, TurnID: "turn-backend-id",
		ItemID: "item-backend-id", InteractionID: interactionID, RequestID: json.RawMessage(`91`),
		ConnectionGeneration: 1, AllowedDecisions: testNativeStringDecisions("allow", "deny"),
		Payload: json.RawMessage(`{"command":"go test ./core"}`), OccurredAt: time.Now(),
	}
	if err := fixture.agent.emit(fixture.threadA, request); err != nil {
		t.Fatal(err)
	}
	if !eventuallyWorkspaceChat(2*time.Second, func() bool {
		record, ok := fixture.repository.interaction(interactionID)
		return ok && record.Status == "pending"
	}) {
		t.Fatalf("backend interaction ID %q was not persisted", interactionID)
	}
	actor := fixture.service.actorForThread(fixture.workspaceA.Ref, fixture.threadA)
	actor.mu.Lock()
	_, pending := actor.pending[interactionID]
	actor.mu.Unlock()
	if !pending {
		t.Fatalf("backend interaction ID %q was not registered on actor", interactionID)
	}

	if err := fixture.agent.emit(fixture.threadA, NativeEventEnvelope{
		Method: "serverRequest/resolved", ThreadID: fixture.threadA, TurnID: "turn-backend-id",
		InteractionID: interactionID, RequestID: json.RawMessage(`91`), ConnectionGeneration: 1,
		Payload: json.RawMessage(`{"requestId":91}`), OccurredAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if !eventuallyWorkspaceChat(2*time.Second, func() bool {
		record, ok := fixture.repository.interaction(interactionID)
		if !ok || record.Status != "resolved" {
			return false
		}
		actor.mu.Lock()
		_, stillPending := actor.pending[interactionID]
		actor.mu.Unlock()
		return !stillPending
	}) {
		record, _ := fixture.repository.interaction(interactionID)
		t.Fatalf("resolved backend interaction = %#v", record)
	}
}

func eventuallyWorkspaceChat(timeout time.Duration, condition func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return condition()
}

func TestWorkspaceChatService_RuntimeActivityReportsDeploymentBlockers(t *testing.T) {
	service := &WorkspaceChatService{actors: make(map[string]*workspaceChatActor)}
	service.actors["workspace\x00thread"] = &workspaceChatActor{
		activeTurnID: "turn-1", realtime: true,
		pending: map[string]NativeInteraction{"request-1": {ID: "request-1"}},
	}
	activity := service.RuntimeActivity()
	if activity.ActiveTurns != 1 || activity.PendingInteractions != 1 || activity.RealtimeSessions != 1 || !activity.Busy() {
		t.Fatalf("RuntimeActivity() = %#v", activity)
	}
}
