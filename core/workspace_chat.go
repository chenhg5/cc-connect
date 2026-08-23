package core

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	workspaceChatReplayLimit                   = 512
	workspaceChatSubscriberDepth               = 128
	workspaceChatPumpCloseTimeout              = 5 * time.Second
	workspaceChatCommitTimeout                 = 5 * time.Second
	workspaceChatErrorOperationFailed          = "operation_failed"
	workspaceChatErrorTerminalStateUnconfirmed = "terminal_state_unconfirmed"
	workspaceChatErrorServerRestarted          = "server_restarted"
	workspaceChatErrorServiceClosed            = "service_closed"
	workspaceChatErrorTransportSendFailed      = "transport_send_failed"
)

var workspaceChatActorIdleTimeout = 30 * time.Second

var (
	errWorkspaceMenuInvalidIndex = errors.New("workspace menu index is invalid")
	errWorkspacePositiveNumber   = errors.New("workspace positive number is invalid")
	errWorkspacePageOutOfRange   = errors.New("workspace menu page is out of range")
	errWorkspaceMenuItemNotFound = errors.New("workspace menu item is not found")
)

type workspaceChatDeliveryTarget struct {
	clientID    string
	requestID   string
	platform    Platform
	replyCtx    any
	destination string
}

type workspaceChatPendingStart struct {
	requestID string
	delivery  *workspaceChatDeliveryTarget
}

type workspaceChatNativeAttempt struct {
	ready      chan struct{}
	generation uint64
	err        error
	completed  bool
}

func newWorkspaceChatNativeAttempt() *workspaceChatNativeAttempt {
	return &workspaceChatNativeAttempt{ready: make(chan struct{})}
}

type workspaceChatActor struct {
	opMu          sync.Mutex
	mu            sync.Mutex
	nativeEventMu sync.Mutex

	workspace                Workspace
	conversation             ConversationRef
	threadID                 string
	epoch                    string
	sequence                 uint64
	replay                   []WorkspaceChatEvent
	subscribers              map[uint64]chan WorkspaceChatEvent
	nextSubID                uint64
	snapshot                 *NativeConversationSnapshot
	activeTurnID             string
	pendingStart             *workspaceChatPendingStart
	submissions              map[string][]string
	terminal                 map[string]string
	deliveries               map[string]*workspaceChatDeliveryTarget
	pending                  map[string]NativeInteraction
	realtime                 bool
	realtimeOwner            string
	realtimeTerminalSequence uint64
	nativeEventsReady        bool
	nativeStagedEvents       []NativeEventEnvelope
	materializationUncertain bool

	nativePump       turnLifecyclePumpOwner
	nativeAttempt    *workspaceChatNativeAttempt
	nativeActive     bool
	nativeGeneration uint64
	retired          bool
	idleTimer        *time.Timer
	idleToken        uint64
}

// WorkspaceChatService 是工作区 conversation actor、活动 Turn、交互、实时会话、
// 持久状态和事件顺序的唯一生命周期所有者。
type WorkspaceChatService struct {
	engine   *Engine
	catalog  WorkspaceCatalogProvider
	backend  NativeConversationBackend
	settings NativeConversationSettingsController
	turns    NativeConversationTurnController
	realtime NativeConversationRealtimeController
	repo     WorkspaceChatRepository

	transports map[string]struct{}
	ctx        context.Context
	cancel     context.CancelFunc
	operations context.Context
	cancelOps  context.CancelFunc

	actorsMu       sync.Mutex
	actors         map[string]*workspaceChatActor
	closed         bool
	closeMu        sync.Mutex
	repoClosed     bool
	deliveryMu     sync.Mutex
	deliveryClosed bool
	workers        sync.WaitGroup
}

func NewWorkspaceChatService(engine *Engine, repo WorkspaceChatRepository, transports []string) (*WorkspaceChatService, error) {
	if engine == nil || repo == nil || engine.agent == nil {
		return nil, fmt.Errorf("workspace chat: template engine and repository are required")
	}
	catalog, ok := engine.agent.(WorkspaceCatalogProvider)
	if !ok {
		return nil, fmt.Errorf("workspace chat: template agent does not provide a workspace catalog")
	}
	backend, ok := engine.agent.(NativeConversationBackend)
	if !ok {
		return nil, fmt.Errorf("workspace chat: template agent does not provide native conversations")
	}
	settings, ok := engine.agent.(NativeConversationSettingsController)
	if !ok {
		return nil, fmt.Errorf("workspace chat: template agent does not provide native settings")
	}
	turns, ok := engine.agent.(NativeConversationTurnController)
	if !ok {
		return nil, fmt.Errorf("workspace chat: template agent does not provide native turns")
	}
	realtime, _ := engine.agent.(NativeConversationRealtimeController)
	allowed := make(map[string]struct{}, len(transports))
	for _, value := range transports {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			allowed[value] = struct{}{}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	operations, cancelOps := context.WithCancel(context.Background())
	service := &WorkspaceChatService{
		engine: engine, catalog: catalog, backend: backend, settings: settings, turns: turns, realtime: realtime,
		repo: repo, transports: allowed, ctx: ctx, cancel: cancel, operations: operations, cancelOps: cancelOps,
		actors: make(map[string]*workspaceChatActor),
	}
	if err := service.reconcileUnfinished(ctx); err != nil {
		cancelOps()
		cancel()
		return nil, err
	}
	return service, nil
}

func (s *WorkspaceChatService) reconcileUnfinished(ctx context.Context) error {
	if err := s.repo.ExpirePendingInteractions(ctx, "connection_lost"); err != nil {
		return fmt.Errorf("workspace chat: expire interactions from prior connection: %w", err)
	}
	submissions, err := s.repo.ListUnfinishedSubmissions(ctx)
	if err != nil {
		return fmt.Errorf("workspace chat: list unfinished submissions: %w", err)
	}
	for _, submission := range submissions {
		if err := s.repo.FinishSubmission(ctx, submission.RequestID, "needs_retry", workspaceChatErrorServerRestarted); err != nil {
			return fmt.Errorf("workspace chat: mark submission %s for retry: %w", submission.RequestID, err)
		}
	}
	intents, err := s.repo.ListPendingSettingIntents(ctx)
	if err != nil {
		return fmt.Errorf("workspace chat: list pending setting intents: %w", err)
	}
	for _, intent := range intents {
		if err := s.repo.ResolveSettingIntent(ctx, intent.ID, "needs_retry", workspaceChatErrorServerRestarted); err != nil {
			return fmt.Errorf("workspace chat: mark setting intent %s for retry: %w", intent.ID, err)
		}
	}
	deliveries, err := s.repo.ListPendingDeliveries(ctx)
	if err != nil {
		return fmt.Errorf("workspace chat: list unfinished deliveries: %w", err)
	}
	for _, delivery := range deliveries {
		if err := s.repo.FinishDelivery(ctx, delivery.ID, "failed", workspaceChatErrorServerRestarted); err != nil {
			return fmt.Errorf("workspace chat: mark delivery %s failed after restart: %w", delivery.ID, err)
		}
	}
	return nil
}

func (s *WorkspaceChatService) Close() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.repoClosed {
		return nil
	}

	s.actorsMu.Lock()
	if !s.closed {
		s.closed = true
	}
	actors := make([]*workspaceChatActor, 0, len(s.actors))
	seen := make(map[*workspaceChatActor]struct{})
	for _, actor := range s.actors {
		if _, exists := seen[actor]; !exists {
			seen[actor] = struct{}{}
			actors = append(actors, actor)
		}
	}
	s.actorsMu.Unlock()
	s.deliveryMu.Lock()
	s.deliveryClosed = true
	s.deliveryMu.Unlock()
	s.cancelOps()

	var closeErrors []error
	for _, actor := range actors {
		closeErrors = append(closeErrors, s.closeActor(actor)...)
	}
	s.cancel()
	if err := s.repo.ExpirePendingInteractions(context.Background(), "cancelled"); err != nil {
		closeErrors = append(closeErrors, fmt.Errorf("cancel remaining workspace interactions: %w", err))
	}
	unfinished, err := s.repo.ListUnfinishedSubmissions(context.Background())
	if err != nil {
		closeErrors = append(closeErrors, fmt.Errorf("list unfinished workspace submissions during close: %w", err))
	} else {
		for _, submission := range unfinished {
			if err := s.repo.FinishSubmission(context.Background(), submission.RequestID, "needs_retry", workspaceChatErrorServiceClosed); err != nil {
				closeErrors = append(closeErrors, fmt.Errorf("mark submission %s for retry during close: %w", submission.RequestID, err))
			}
		}
	}
	s.workers.Wait()
	if err := s.repo.Close(); err != nil {
		closeErrors = append(closeErrors, fmt.Errorf("close workspace chat repository: %w", err))
	} else {
		s.repoClosed = true
	}
	return errors.Join(closeErrors...)
}

func (s *WorkspaceChatService) closeActor(actor *workspaceChatActor) []error {
	actor.opMu.Lock()
	defer actor.opMu.Unlock()

	actor.mu.Lock()
	realtimeActive := actor.realtime
	generation := actor.nativeGeneration
	threadID := actor.threadID
	activeTurnID := actor.activeTurnID
	if activeTurnID == "" && actor.snapshot != nil && actor.snapshot.ActiveTurn != nil {
		activeTurnID = actor.snapshot.ActiveTurn.ID
	}
	actor.mu.Unlock()

	var closeErrors []error
	if realtimeActive && threadID != "" && generation != 0 && s.realtime != nil {
		ctx, cancel := context.WithTimeout(context.Background(), workspaceChatPumpCloseTimeout)
		err := s.realtime.StopNativeRealtime(ctx, actor.workspace, threadID, generation)
		cancel()
		if err != nil && !errors.Is(err, ErrNativeConnectionStale) {
			closeErrors = append(closeErrors, fmt.Errorf("stop native realtime for %s: %w", threadID, err))
		}
	}
	interruptConfirmed := activeTurnID == ""
	if activeTurnID != "" && threadID != "" && generation != 0 {
		ctx, cancel := context.WithTimeout(context.Background(), workspaceChatPumpCloseTimeout)
		err := s.turns.InterruptNativeTurn(ctx, actor.workspace, threadID, generation, activeTurnID)
		cancel()
		if err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("interrupt native turn %s: %w", activeTurnID, err))
		} else {
			interruptConfirmed = true
		}
	} else if activeTurnID != "" {
		closeErrors = append(closeErrors, fmt.Errorf("interrupt native turn %s: %w", activeTurnID, ErrNativeConnectionStale))
	}
	if err := actor.nativePump.Close(workspaceChatPumpCloseTimeout); err != nil {
		closeErrors = append(closeErrors, fmt.Errorf("close native pump for %s: %w", actor.conversation.ID, err))
	}

	actor.mu.Lock()
	actor.idleToken++
	if actor.idleTimer != nil {
		actor.idleTimer.Stop()
		actor.idleTimer = nil
	}
	interactionIDs := make([]string, 0, len(actor.pending))
	for interactionID := range actor.pending {
		interactionIDs = append(interactionIDs, interactionID)
	}
	submissionStatuses := make(map[string]string)
	for turnID, requestIDs := range actor.submissions {
		status := "needs_retry"
		if turnID == activeTurnID && interruptConfirmed {
			status = "interrupted"
		}
		for _, requestID := range requestIDs {
			submissionStatuses[requestID] = status
		}
	}
	if actor.pendingStart != nil {
		submissionStatuses[actor.pendingStart.requestID] = "needs_retry"
	}
	actor.pending = make(map[string]NativeInteraction)
	actor.submissions = make(map[string][]string)
	actor.pendingStart = nil
	actor.deliveries = make(map[string]*workspaceChatDeliveryTarget)
	actor.realtime = false
	actor.realtimeOwner = ""
	actor.activeTurnID = ""
	if interruptConfirmed && actor.snapshot != nil {
		actor.snapshot.ActiveTurn = nil
	}
	for id, ch := range actor.subscribers {
		delete(actor.subscribers, id)
		close(ch)
	}
	actor.mu.Unlock()

	for _, interactionID := range interactionIDs {
		if err := s.repo.ResolveInteraction(context.Background(), interactionID, "cancelled"); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("cancel interaction %s: %w", interactionID, err))
		}
	}
	for requestID, status := range submissionStatuses {
		if err := s.repo.FinishSubmission(context.Background(), requestID, status, workspaceChatErrorServiceClosed); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("finish submission %s during close: %w", requestID, err))
		}
	}
	return closeErrors
}

func (s *WorkspaceChatService) TransportEnabled(name string) bool {
	_, ok := s.transports[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

func (s *WorkspaceChatService) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	items, err := s.catalog.ListWorkspaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("workspace chat: list workspaces: %w", err)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Order < items[j].Order })
	return items, nil
}

func (s *WorkspaceChatService) resolveWorkspace(ctx context.Context, ref string) (Workspace, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Workspace{}, ErrWorkspaceNotFound
	}
	workspace, err := s.catalog.ResolveWorkspace(ctx, ref)
	if err != nil {
		return Workspace{}, fmt.Errorf("workspace chat: resolve workspace: %w", err)
	}
	if workspace.Ref != ref {
		return Workspace{}, fmt.Errorf("workspace chat: catalog returned a different workspace reference")
	}
	if !workspace.Available {
		return Workspace{}, fmt.Errorf("workspace chat: workspace is unavailable: %s", workspace.Error)
	}
	info, err := os.Stat(workspace.RootPath)
	if err != nil {
		return Workspace{}, fmt.Errorf("workspace chat: inspect workspace root: %w", err)
	}
	if !info.IsDir() {
		return Workspace{}, fmt.Errorf("workspace chat: workspace root is not a directory")
	}
	return workspace, nil
}

func normalizeNativePage(page NativePageRequest) (NativePageRequest, error) {
	if page.Limit == 0 {
		page.Limit = 50
	}
	if page.Limit < 1 || page.Limit > 100 {
		return NativePageRequest{}, fmt.Errorf("page limit must be between 1 and 100")
	}
	page.SortDirection = strings.ToLower(strings.TrimSpace(page.SortDirection))
	if page.SortDirection == "" {
		page.SortDirection = "desc"
	}
	if page.SortDirection != "asc" && page.SortDirection != "desc" {
		return NativePageRequest{}, fmt.Errorf("sort_direction must be asc or desc")
	}
	page.Cursor = strings.TrimSpace(page.Cursor)
	return page, nil
}

func (s *WorkspaceChatService) ListThreads(ctx context.Context, workspaceRef string, page NativePageRequest) (NativeThreadPage, error) {
	workspace, err := s.resolveWorkspace(ctx, workspaceRef)
	if err != nil {
		return NativeThreadPage{}, err
	}
	page, err = normalizeNativePage(page)
	if err != nil {
		return NativeThreadPage{}, err
	}
	result, err := s.backend.ListNativeConversations(ctx, workspace, page)
	if err != nil {
		return NativeThreadPage{}, fmt.Errorf("workspace chat: list native conversations: %w", err)
	}
	for _, thread := range result.Data {
		if thread.ID == "" || !sameWorkspacePath(thread.Cwd, workspace.RootPath) {
			return NativeThreadPage{}, fmt.Errorf("workspace chat: backend returned a thread outside the requested workspace")
		}
	}
	return result, nil
}

func (s *WorkspaceChatService) RuntimeCatalog(ctx context.Context, workspaceRef string) (NativeRuntimeCatalog, error) {
	workspace, err := s.resolveWorkspace(ctx, workspaceRef)
	if err != nil {
		return NativeRuntimeCatalog{}, err
	}
	catalog, err := s.backend.NativeRuntimeCatalog(ctx, workspace)
	if err != nil {
		return NativeRuntimeCatalog{}, fmt.Errorf("workspace chat: native runtime catalog: %w", err)
	}
	normalizeNativeRuntimeCatalogCollections(&catalog)
	return catalog, nil
}

func normalizeNativeRuntimeCatalogCollections(catalog *NativeRuntimeCatalog) {
	if catalog.Capabilities == nil {
		catalog.Capabilities = make(map[string]CapabilityStatus)
	}
	if catalog.Models == nil {
		catalog.Models = []NativeModelOption{}
	}
	if catalog.Modes == nil {
		catalog.Modes = []NativeCollaborationModeOption{}
	}
	if catalog.Permissions == nil {
		catalog.Permissions = []NativePermissionProfile{}
	}
	if catalog.Personalities == nil {
		catalog.Personalities = []string{}
	}
	if catalog.Summaries == nil {
		catalog.Summaries = []string{}
	}
	if catalog.Voices.V1 == nil {
		catalog.Voices.V1 = []string{}
	}
	if catalog.Voices.V2 == nil {
		catalog.Voices.V2 = []string{}
	}
	for index := range catalog.Models {
		if catalog.Models[index].ReasoningEfforts == nil {
			catalog.Models[index].ReasoningEfforts = []ReasoningEffortOption{}
		}
		if catalog.Models[index].InputModalities == nil {
			catalog.Models[index].InputModalities = []string{}
		}
		if catalog.Models[index].ServiceTiers == nil {
			catalog.Models[index].ServiceTiers = []ServiceTierOption{}
		}
	}
}

func (s *WorkspaceChatService) ReadThread(ctx context.Context, workspaceRef, threadID string) (NativeConversationSnapshot, error) {
	workspace, err := s.resolveWorkspace(ctx, workspaceRef)
	if err != nil {
		return NativeConversationSnapshot{}, err
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return NativeConversationSnapshot{}, ErrNativeThreadNotFound
	}
	actor := s.actor(workspace, ConversationRef{Kind: ConversationKindThread, ID: threadID})
	operationCtx, endOperation, err := s.beginActorOperation(ctx, actor)
	if err != nil {
		return NativeConversationSnapshot{}, err
	}
	defer endOperation()
	ctx = operationCtx
	if _, err := s.waitNativePump(ctx, actor); err != nil {
		readErr := fmt.Errorf("workspace chat: read native thread: %w", err)
		if errors.Is(err, ErrNativeThreadNotFound) {
			if retireErr := s.retireProvisionalActor(actor); retireErr != nil {
				readErr = errors.Join(readErr, retireErr)
			}
		}
		return NativeConversationSnapshot{}, readErr
	}
	actor.mu.Lock()
	if actor.snapshot == nil {
		actor.mu.Unlock()
		return NativeConversationSnapshot{}, fmt.Errorf("%w: native snapshot is unavailable", ErrNativeThreadNotFound)
	}
	snapshot := cloneNativeConversationSnapshot(*actor.snapshot)
	actor.mu.Unlock()
	if snapshot.Thread.ID != threadID || !sameWorkspacePath(snapshot.Thread.Cwd, workspace.RootPath) {
		return NativeConversationSnapshot{}, fmt.Errorf("%w: thread does not belong to workspace", ErrNativeThreadNotFound)
	}
	if err := validateNativeSnapshot(workspace, threadID, snapshot); err != nil {
		return NativeConversationSnapshot{}, fmt.Errorf("%w: %v", ErrNativeThreadNotFound, err)
	}
	actor.mu.Lock()
	actor.threadID = threadID
	actor.snapshot = &snapshot
	actor.activeTurnID = ""
	if snapshot.ActiveTurn != nil {
		actor.activeTurnID = snapshot.ActiveTurn.ID
	}
	actor.mu.Unlock()
	return snapshot, nil
}

func (s *WorkspaceChatService) resolveThread(ctx context.Context, workspaceRef, threadID string) (Workspace, error) {
	workspace, err := s.resolveWorkspace(ctx, workspaceRef)
	if err != nil {
		return Workspace{}, err
	}
	if _, err := s.ReadThread(ctx, workspace.Ref, threadID); err != nil {
		return Workspace{}, err
	}
	return workspace, nil
}

func (s *WorkspaceChatService) ListTurns(ctx context.Context, workspaceRef, threadID string, page NativePageRequest) (NativeTurnPage, error) {
	workspace, err := s.resolveThread(ctx, workspaceRef, threadID)
	if err != nil {
		return NativeTurnPage{}, err
	}
	page, err = normalizeNativePage(page)
	if err != nil {
		return NativeTurnPage{}, err
	}
	result, err := s.backend.ListNativeTurns(ctx, workspace, threadID, page)
	if err != nil {
		return NativeTurnPage{}, fmt.Errorf("workspace chat: list native turns: %w", err)
	}
	return result, nil
}

func (s *WorkspaceChatService) ListItems(ctx context.Context, workspaceRef, threadID, turnID string, page NativePageRequest) (NativeItemPage, error) {
	workspace, err := s.resolveThread(ctx, workspaceRef, threadID)
	if err != nil {
		return NativeItemPage{}, err
	}
	turnID = strings.TrimSpace(turnID)
	page, err = normalizeNativePage(page)
	if err != nil {
		return NativeItemPage{}, err
	}
	result, err := s.backend.ListNativeItems(ctx, workspace, threadID, turnID, page)
	if err != nil {
		return NativeItemPage{}, fmt.Errorf("workspace chat: list native items: %w", err)
	}
	return result, nil
}

func (s *WorkspaceChatService) CreateDraft(ctx context.Context, clientID, workspaceRef string) (WorkspaceChatDraft, error) {
	workspace, err := s.resolveWorkspace(ctx, workspaceRef)
	if err != nil {
		return WorkspaceChatDraft{}, err
	}
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return WorkspaceChatDraft{}, fmt.Errorf("workspace chat: client id is required")
	}
	now := time.Now()
	draft := WorkspaceChatDraft{
		ID: newWorkspaceChatID("draft"), OwnerClientID: clientID, WorkspaceRef: workspace.Ref,
		State: "draft", SettingsPatch: NativeThreadSettingsPatch{}, CreatedAt: now, UpdatedAt: now,
	}
	selection := WorkspaceChatSelection{
		ClientID: clientID, WorkspaceRef: workspace.Ref,
		Conversation: ConversationRef{Kind: ConversationKindDraft, ID: draft.ID}, UpdatedAt: now,
	}
	actor := s.actor(workspace, selection.Conversation)
	operationCtx, endOperation, err := s.beginActorOperation(ctx, actor)
	if err != nil {
		return WorkspaceChatDraft{}, err
	}
	defer endOperation()
	ctx = operationCtx
	if err := s.repo.CreateDraftAndSelect(ctx, draft, selection); err != nil {
		return WorkspaceChatDraft{}, fmt.Errorf("workspace chat: create and select draft: %w", err)
	}
	return draft, nil
}

func (s *WorkspaceChatService) ReadDraft(ctx context.Context, clientID, workspaceRef, draftID string) (WorkspaceChatDraft, error) {
	workspace, err := s.resolveWorkspace(ctx, workspaceRef)
	if err != nil {
		return WorkspaceChatDraft{}, err
	}
	draft, err := s.repo.GetDraft(ctx, strings.TrimSpace(draftID))
	if err != nil {
		return WorkspaceChatDraft{}, fmt.Errorf("workspace chat: read draft: %w", err)
	}
	if draft == nil || draft.WorkspaceRef != workspace.Ref || draft.OwnerClientID != strings.TrimSpace(clientID) {
		return WorkspaceChatDraft{}, fmt.Errorf("workspace chat: draft not found")
	}
	return *draft, nil
}

func (s *WorkspaceChatService) UpdateDraftSettings(ctx context.Context, clientID, workspaceRef, draftID string, patch NativeThreadSettingsPatch) (WorkspaceChatDraft, error) {
	workspace, err := s.resolveWorkspace(ctx, workspaceRef)
	if err != nil {
		return WorkspaceChatDraft{}, err
	}
	clientID = strings.TrimSpace(clientID)
	draftID = strings.TrimSpace(draftID)
	if clientID == "" || draftID == "" || emptyNativeSettingsPatch(patch) {
		return WorkspaceChatDraft{}, fmt.Errorf("workspace chat: client, draft and settings patch are required")
	}
	actor := s.actor(workspace, ConversationRef{Kind: ConversationKindDraft, ID: draftID})
	operationCtx, endOperation, err := s.beginActorOperation(ctx, actor)
	if err != nil {
		return WorkspaceChatDraft{}, err
	}
	defer endOperation()
	ctx = operationCtx
	draft, err := s.repo.GetDraft(ctx, draftID)
	if err != nil {
		return WorkspaceChatDraft{}, fmt.Errorf("workspace chat: read draft settings: %w", err)
	}
	if draft == nil || draft.OwnerClientID != clientID || draft.WorkspaceRef != workspace.Ref || draft.State != "draft" {
		return WorkspaceChatDraft{}, fmt.Errorf("workspace chat: draft not found")
	}
	catalog, err := s.backend.NativeRuntimeCatalog(ctx, workspace)
	if err != nil {
		return WorkspaceChatDraft{}, fmt.Errorf("workspace chat: native runtime catalog: %w", err)
	}
	merged := mergeNativeSettingsPatch(draft.SettingsPatch, patch)
	if err := validateNativeSettingsPatch(catalog, defaultNativeSettings(catalog), merged); err != nil {
		return WorkspaceChatDraft{}, err
	}
	updatedAt := time.Now()
	if err := s.repo.UpdateDraftSettings(ctx, draft.ID, clientID, workspace.Ref, merged, updatedAt); err != nil {
		return WorkspaceChatDraft{}, fmt.Errorf("workspace chat: persist draft settings: %w", err)
	}
	draft.SettingsPatch = merged
	draft.UpdatedAt = updatedAt
	return *draft, nil
}

func (s *WorkspaceChatService) SelectWorkspace(ctx context.Context, clientID, workspaceRef string) (WorkspaceChatSelection, error) {
	workspace, err := s.resolveWorkspace(ctx, workspaceRef)
	if err != nil {
		return WorkspaceChatSelection{}, err
	}
	page, err := s.ListThreads(ctx, workspace.Ref, NativePageRequest{Limit: 1, SortDirection: "desc"})
	if err != nil {
		return WorkspaceChatSelection{}, err
	}
	if len(page.Data) == 0 {
		draft, err := s.CreateDraft(ctx, clientID, workspace.Ref)
		if err != nil {
			return WorkspaceChatSelection{}, err
		}
		return WorkspaceChatSelection{ClientID: clientID, WorkspaceRef: workspace.Ref, Conversation: ConversationRef{Kind: ConversationKindDraft, ID: draft.ID}, UpdatedAt: draft.UpdatedAt}, nil
	}
	return s.SelectConversation(ctx, clientID, workspace.Ref, ConversationRef{Kind: ConversationKindThread, ID: page.Data[0].ID})
}

func (s *WorkspaceChatService) SelectConversation(ctx context.Context, clientID, workspaceRef string, conversation ConversationRef) (WorkspaceChatSelection, error) {
	workspace, err := s.resolveWorkspace(ctx, workspaceRef)
	if err != nil {
		return WorkspaceChatSelection{}, err
	}
	conversation, err = s.validateConversation(ctx, strings.TrimSpace(clientID), workspace, conversation)
	if err != nil {
		return WorkspaceChatSelection{}, err
	}
	selection := WorkspaceChatSelection{ClientID: clientID, WorkspaceRef: workspace.Ref, Conversation: conversation, UpdatedAt: time.Now()}
	if err := s.repo.PutSelection(ctx, selection); err != nil {
		return WorkspaceChatSelection{}, fmt.Errorf("workspace chat: persist selection: %w", err)
	}
	return selection, nil
}

func (s *WorkspaceChatService) Selection(ctx context.Context, clientID string) (*WorkspaceChatSelection, error) {
	selection, err := s.repo.GetSelection(ctx, strings.TrimSpace(clientID))
	if err != nil || selection == nil {
		return selection, err
	}
	workspace, err := s.resolveWorkspace(ctx, selection.WorkspaceRef)
	if err != nil {
		if isPermanentWorkspaceSelectionError(err) {
			if deleteErr := s.repo.DeleteSelection(ctx, selection.ClientID); deleteErr != nil {
				return selection, fmt.Errorf("workspace chat: delete invalid selection: %w", deleteErr)
			}
			return nil, nil
		}
		return selection, err
	}
	conversation, err := s.validateConversation(ctx, selection.ClientID, workspace, selection.Conversation)
	if errors.Is(err, ErrWorkspaceDraftMaterialized) {
		draft, readErr := s.repo.GetDraft(ctx, selection.Conversation.ID)
		if readErr == nil && draft != nil && draft.ThreadID != "" {
			normalized := ConversationRef{Kind: ConversationKindThread, ID: draft.ThreadID}
			return s.selectRecoveredConversation(ctx, selection, normalized)
		}
	}
	if err != nil {
		if isPermanentWorkspaceSelectionError(err) {
			if deleteErr := s.repo.DeleteSelection(ctx, selection.ClientID); deleteErr != nil {
				return selection, fmt.Errorf("workspace chat: delete invalid selection: %w", deleteErr)
			}
			return nil, nil
		}
		return selection, err
	}
	if conversation != selection.Conversation {
		return s.selectRecoveredConversation(ctx, selection, conversation)
	}
	return selection, nil
}

func isPermanentWorkspaceSelectionError(err error) bool {
	return errors.Is(err, ErrWorkspaceNotFound) || errors.Is(err, ErrNativeThreadNotFound) ||
		errors.Is(err, ErrWorkspaceDraftNotFound)
}

func (s *WorkspaceChatService) selectRecoveredConversation(ctx context.Context, selection *WorkspaceChatSelection, conversation ConversationRef) (*WorkspaceChatSelection, error) {
	selection.Conversation = conversation
	selection.UpdatedAt = time.Now()
	if err := s.repo.PutSelection(ctx, *selection); err != nil {
		return nil, fmt.Errorf("workspace chat: repair materialized selection: %w", err)
	}
	return selection, nil
}

func (s *WorkspaceChatService) validateConversation(ctx context.Context, clientID string, workspace Workspace, conversation ConversationRef) (ConversationRef, error) {
	conversation.ID = strings.TrimSpace(conversation.ID)
	if conversation.ID == "" {
		return ConversationRef{}, fmt.Errorf("workspace chat: conversation reference is required")
	}
	switch conversation.Kind {
	case ConversationKindDraft:
		draft, err := s.repo.GetDraft(ctx, conversation.ID)
		if err != nil {
			return ConversationRef{}, fmt.Errorf("workspace chat: read draft: %w", err)
		}
		if draft == nil || draft.WorkspaceRef != workspace.Ref || draft.OwnerClientID != clientID {
			return ConversationRef{}, ErrWorkspaceDraftNotFound
		}
		if draft.State == "materialization_uncertain" {
			return ConversationRef{}, ErrWorkspaceDraftMaterializationUncertain
		}
		if draft.State != "draft" {
			return ConversationRef{}, ErrWorkspaceDraftMaterialized
		}
	case ConversationKindThread:
		if _, err := s.ReadThread(ctx, workspace.Ref, conversation.ID); err != nil {
			return ConversationRef{}, err
		}
	default:
		return ConversationRef{}, fmt.Errorf("workspace chat: conversation kind must be draft or thread")
	}
	return conversation, nil
}

func (s *WorkspaceChatService) UpdateSettings(ctx context.Context, workspaceRef, threadID string, patch NativeThreadSettingsPatch) (NativeThreadSettings, error) {
	snapshot, err := s.ReadThread(ctx, workspaceRef, threadID)
	if err != nil {
		return NativeThreadSettings{}, err
	}
	actor := s.actorForThread(workspaceRef, threadID)
	operationCtx, endOperation, err := s.beginActorOperation(ctx, actor)
	if err != nil {
		return NativeThreadSettings{}, err
	}
	defer endOperation()
	ctx = operationCtx
	generation, err := s.waitNativePump(ctx, actor)
	if err != nil {
		return NativeThreadSettings{}, fmt.Errorf("workspace chat: subscribe native settings: %w", err)
	}
	catalog, err := s.backend.NativeRuntimeCatalog(ctx, actor.workspace)
	if err != nil {
		return NativeThreadSettings{}, fmt.Errorf("workspace chat: native runtime catalog: %w", err)
	}
	if err := validateNativeSettingsPatch(catalog, snapshot.Settings, patch); err != nil {
		return NativeThreadSettings{}, err
	}
	intent := WorkspaceChatSettingIntent{
		ID: newWorkspaceChatID("settings"), WorkspaceRef: actor.workspace.Ref, ThreadID: threadID,
		Patch: patch, Status: "pending", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := s.repo.PutSettingIntent(ctx, intent); err != nil {
		return NativeThreadSettings{}, fmt.Errorf("workspace chat: persist setting intent: %w", err)
	}
	settings, err := s.settings.UpdateNativeConversationSettings(ctx, actor.workspace, threadID, generation, patch)
	if err != nil {
		status := "failed"
		if IsNativeAcceptanceUnknown(err) {
			status = "needs_retry"
		}
		_ = s.resolveSettingIntent(intent.ID, status, err.Error())
		return NativeThreadSettings{}, fmt.Errorf("workspace chat: update native settings: %w", err)
	}
	if strings.TrimSpace(settings.Revision) == "" {
		err = fmt.Errorf("native settings update returned without thread/settings/updated revision")
		_ = s.resolveSettingIntent(intent.ID, "needs_retry", err.Error())
		return NativeThreadSettings{}, err
	}
	if err := s.resolveSettingIntent(intent.ID, "applied", ""); err != nil {
		return NativeThreadSettings{}, fmt.Errorf("workspace chat: commit setting intent: %w", err)
	}
	actor.mu.Lock()
	if actor.snapshot != nil {
		actor.snapshot.Settings = settings
	}
	actor.mu.Unlock()
	return settings, nil
}

func (s *WorkspaceChatService) commitContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(s.ctx, workspaceChatCommitTimeout)
}

func (s *WorkspaceChatService) finishSubmission(requestID, status, errorMessage string) error {
	ctx, cancel := s.commitContext()
	defer cancel()
	return s.repo.FinishSubmission(ctx, requestID, status, workspaceChatPersistentErrorCode(status, errorMessage))
}

func (s *WorkspaceChatService) resolveSettingIntent(intentID, status, errorMessage string) error {
	ctx, cancel := s.commitContext()
	defer cancel()
	return s.repo.ResolveSettingIntent(ctx, intentID, status, workspaceChatPersistentErrorCode(status, errorMessage))
}

func workspaceChatPersistentErrorCode(status, detail string) string {
	if strings.TrimSpace(detail) == "" {
		return ""
	}
	if status == "needs_retry" {
		return workspaceChatErrorTerminalStateUnconfirmed
	}
	return workspaceChatErrorOperationFailed
}

func validateNativeSettingsPatch(catalog NativeRuntimeCatalog, current NativeThreadSettings, patch NativeThreadSettingsPatch) error {
	model := current.Model
	if model == "" {
		model = defaultNativeSettings(catalog).Model
	}
	if patch.Mode != nil {
		mode := strings.TrimSpace(*patch.Mode)
		if mode != "default" && mode != "plan" {
			return fmt.Errorf("workspace chat: only default and plan collaboration modes are supported")
		}
		modeOption := findNativeModeOption(catalog.Modes, mode)
		if modeOption == nil {
			return fmt.Errorf("workspace chat: collaboration mode is not present in the native catalog")
		}
		if patch.Model == nil && modeOption.Model != nil {
			model = strings.TrimSpace(*modeOption.Model)
			if model == "" || findNativeModel(catalog.Models, model) == nil {
				return fmt.Errorf("workspace chat: collaboration mode model is not present in the native catalog")
			}
		}
	}
	if patch.Model != nil {
		model = strings.TrimSpace(*patch.Model)
		if model == "" || findNativeModel(catalog.Models, model) == nil {
			return fmt.Errorf("workspace chat: model is not present in the native catalog")
		}
	}
	modelOption := findNativeModel(catalog.Models, model)
	if patch.Effort != nil {
		effort := strings.TrimSpace(*patch.Effort)
		if modelOption == nil || !containsEffort(modelOption.ReasoningEfforts, effort) {
			return fmt.Errorf("workspace chat: effort is not available for the selected model")
		}
	}
	if patch.PlanEffort != nil {
		effort := strings.TrimSpace(*patch.PlanEffort)
		if modelOption == nil || !containsEffort(modelOption.ReasoningEfforts, effort) {
			return fmt.Errorf("workspace chat: plan effort is not available for the selected model")
		}
	}
	if patch.ServiceTier != nil {
		tier := strings.TrimSpace(*patch.ServiceTier)
		if tier != "" && (modelOption == nil || !containsTier(modelOption.ServiceTiers, tier)) {
			return fmt.Errorf("workspace chat: service tier is not available for the selected model")
		}
	}
	if patch.PermissionProfile != nil {
		permission := strings.TrimSpace(*patch.PermissionProfile)
		valid := permission == ""
		for _, option := range catalog.Permissions {
			if option.ID == permission && option.Allowed {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("workspace chat: permission profile is not allowed by the native catalog")
		}
	}
	if patch.Personality != nil {
		personality := strings.TrimSpace(*patch.Personality)
		if personality != "" && !containsString(catalog.Personalities, personality) {
			return fmt.Errorf("workspace chat: personality is not present in the native catalog")
		}
	}
	if patch.Summary != nil {
		summary := strings.TrimSpace(*patch.Summary)
		if summary != "" && !containsString(catalog.Summaries, summary) {
			return fmt.Errorf("workspace chat: reasoning summary is not present in the native catalog")
		}
	}
	return nil
}

func defaultNativeSettings(catalog NativeRuntimeCatalog) NativeThreadSettings {
	for _, model := range catalog.Models {
		if model.Default {
			return NativeThreadSettings{Model: model.Model, Effort: model.DefaultReasoningEffort, ServiceTier: model.DefaultServiceTier}
		}
	}
	if len(catalog.Models) > 0 {
		model := catalog.Models[0]
		return NativeThreadSettings{Model: model.Model, Effort: model.DefaultReasoningEffort, ServiceTier: model.DefaultServiceTier}
	}
	return NativeThreadSettings{}
}

func findNativeModel(options []NativeModelOption, value string) *NativeModelOption {
	for i := range options {
		if options[i].ID == value || options[i].Model == value {
			return &options[i]
		}
	}
	return nil
}

func findNativeModeOption(options []NativeCollaborationModeOption, value string) *NativeCollaborationModeOption {
	for i := range options {
		if options[i].Mode != nil && strings.TrimSpace(*options[i].Mode) != "" && *options[i].Mode == value {
			return &options[i]
		}
	}
	return nil
}

func containsEffort(options []ReasoningEffortOption, value string) bool {
	for _, option := range options {
		if option.Effort == value {
			return true
		}
	}
	return false
}

func containsTier(options []ServiceTierOption, value string) bool {
	for _, option := range options {
		if option.ID == value {
			return true
		}
	}
	return false
}

func containsString(options []string, value string) bool {
	for _, option := range options {
		if option == value {
			return true
		}
	}
	return false
}

func (s *WorkspaceChatService) StartTurn(ctx context.Context, clientID, requestID, workspaceRef string, conversation ConversationRef, input []NativeUserInput, patch NativeThreadSettingsPatch) (NativeTurnResult, error) {
	return s.startTurn(ctx, clientID, requestID, workspaceRef, conversation, input, patch, nil, false)
}

func (s *WorkspaceChatService) startTrustedTurn(ctx context.Context, clientID, requestID, workspaceRef string, conversation ConversationRef, input []NativeUserInput, delivery *workspaceChatDeliveryTarget) (NativeTurnResult, error) {
	return s.startTurn(ctx, clientID, requestID, workspaceRef, conversation, input, NativeThreadSettingsPatch{}, delivery, true)
}

func (s *WorkspaceChatService) startTurn(ctx context.Context, clientID, requestID, workspaceRef string, conversation ConversationRef, input []NativeUserInput, patch NativeThreadSettingsPatch, delivery *workspaceChatDeliveryTarget, trusted bool) (NativeTurnResult, error) {
	workspace, err := s.resolveWorkspace(ctx, workspaceRef)
	if err != nil {
		return NativeTurnResult{}, err
	}
	conversation, err = s.validateConversation(ctx, clientID, workspace, conversation)
	if err != nil {
		return NativeTurnResult{}, err
	}
	if conversation.Kind == ConversationKindThread && !emptyNativeSettingsPatch(patch) {
		return NativeTurnResult{}, fmt.Errorf("workspace chat: turn start settings are only valid when materializing a draft")
	}
	input, err = s.validateNativeInputs(input, trusted)
	if err != nil {
		return NativeTurnResult{}, err
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return NativeTurnResult{}, fmt.Errorf("workspace chat: request id is required")
	}
	actor := s.actor(workspace, conversation)
	operationCtx, endOperation, err := s.beginActorOperation(ctx, actor)
	if err != nil {
		return NativeTurnResult{}, err
	}
	defer endOperation()
	ctx = operationCtx
	if conversation.Kind == ConversationKindDraft {
		return s.materializeDraftTurn(ctx, actor, clientID, requestID, input, patch, delivery)
	}
	return s.startThreadTurn(ctx, actor, clientID, requestID, input, delivery)
}

func (s *WorkspaceChatService) materializeDraftTurn(ctx context.Context, actor *workspaceChatActor, clientID, requestID string, input []NativeUserInput, patch NativeThreadSettingsPatch, delivery *workspaceChatDeliveryTarget) (NativeTurnResult, error) {
	actor.mu.Lock()
	materializationUncertain := actor.materializationUncertain
	actor.mu.Unlock()
	if materializationUncertain {
		return NativeTurnResult{}, ErrWorkspaceDraftMaterializationUncertain
	}
	draft, err := s.repo.GetDraft(ctx, actor.conversation.ID)
	if err == nil && draft != nil && draft.State == "materialization_uncertain" {
		return NativeTurnResult{}, ErrWorkspaceDraftMaterializationUncertain
	}
	if err != nil || draft == nil || draft.State != "draft" || draft.OwnerClientID != clientID || draft.WorkspaceRef != actor.workspace.Ref {
		if err == nil {
			err = fmt.Errorf("draft is not available")
		}
		return NativeTurnResult{}, fmt.Errorf("workspace chat: materialize draft: %w", err)
	}
	patch = mergeNativeSettingsPatch(draft.SettingsPatch, patch)
	if !emptyNativeSettingsPatch(patch) {
		catalog, err := s.backend.NativeRuntimeCatalog(ctx, actor.workspace)
		if err != nil {
			return NativeTurnResult{}, fmt.Errorf("workspace chat: native runtime catalog: %w", err)
		}
		if err := validateNativeSettingsPatch(catalog, NativeThreadSettings{}, patch); err != nil {
			return NativeTurnResult{}, err
		}
	}
	if err := s.beginSubmission(ctx, clientID, requestID, actor.workspace.Ref, actor.conversation, "start", "", input); err != nil {
		return NativeTurnResult{}, err
	}
	snapshot, err := s.backend.StartNativeConversation(ctx, actor.workspace)
	if err != nil {
		if IsNativeAcceptanceUnknown(err) {
			return NativeTurnResult{}, s.failDraftMaterialization(actor, draft.ID, requestID, "", fmt.Errorf("start native conversation: %w", err))
		}
		_ = s.finishSubmission(requestID, "failed", err.Error())
		return NativeTurnResult{}, fmt.Errorf("workspace chat: start native conversation: %w", err)
	}
	threadID := strings.TrimSpace(snapshot.Thread.ID)
	if err := validateNativeSnapshot(actor.workspace, threadID, snapshot); err != nil {
		return NativeTurnResult{}, s.failDraftMaterialization(actor, draft.ID, requestID, "", fmt.Errorf("invalid created native conversation: %w", err))
	}
	actor.mu.Lock()
	actor.threadID = threadID
	actor.snapshot = &snapshot
	actor.mu.Unlock()
	generation, err := s.waitNativePump(ctx, actor)
	if err != nil {
		return NativeTurnResult{}, s.failDraftMaterialization(actor, draft.ID, requestID, "", fmt.Errorf("subscribe first native turn: %w", err))
	}
	if !emptyNativeSettingsPatch(patch) {
		intent := WorkspaceChatSettingIntent{ID: newWorkspaceChatID("settings"), WorkspaceRef: actor.workspace.Ref, ThreadID: threadID, Patch: patch, Status: "pending", CreatedAt: time.Now(), UpdatedAt: time.Now()}
		if err := s.repo.PutSettingIntent(ctx, intent); err != nil {
			return NativeTurnResult{}, s.failDraftMaterialization(actor, draft.ID, requestID, "", fmt.Errorf("persist initial setting intent: %w", err))
		}
		settings, updateErr := s.settings.UpdateNativeConversationSettings(ctx, actor.workspace, threadID, generation, patch)
		if updateErr != nil || strings.TrimSpace(settings.Revision) == "" {
			if updateErr == nil {
				updateErr = fmt.Errorf("settings update completed without thread/settings/updated")
			}
			intentStatus := "failed"
			if IsNativeAcceptanceUnknown(updateErr) || strings.TrimSpace(settings.Revision) == "" {
				intentStatus = "needs_retry"
			}
			_ = s.resolveSettingIntent(intent.ID, intentStatus, updateErr.Error())
			return NativeTurnResult{}, s.failDraftMaterialization(actor, draft.ID, requestID, "", fmt.Errorf("apply initial settings: %w", updateErr))
		}
		if err := s.resolveSettingIntent(intent.ID, "applied", ""); err != nil {
			return NativeTurnResult{}, s.failDraftMaterialization(actor, draft.ID, requestID, "", fmt.Errorf("commit initial settings: %w", err))
		}
		snapshot.Settings = settings
	}
	actor.mu.Lock()
	actor.pendingStart = &workspaceChatPendingStart{requestID: requestID, delivery: delivery}
	actor.mu.Unlock()
	result, err := s.turns.StartNativeTurn(ctx, actor.workspace, threadID, generation, NativeTurnStartRequest{ClientMessageID: requestID, Input: input})
	if err != nil {
		actor.mu.Lock()
		actor.pendingStart = nil
		actor.mu.Unlock()
		return NativeTurnResult{}, s.failDraftMaterialization(actor, draft.ID, requestID, "", fmt.Errorf("start first native turn: %w", err))
	}
	if result.TurnID == "" {
		err = fmt.Errorf("native turn/start returned an empty turn id")
		return NativeTurnResult{}, s.failDraftMaterialization(actor, draft.ID, requestID, "", err)
	}
	commitCtx, cancelCommit := s.commitContext()
	err = s.repo.MaterializeDraft(commitCtx, draft.ID, requestID, threadID, result.TurnID)
	cancelCommit()
	if err != nil {
		return NativeTurnResult{}, s.failDraftMaterialization(actor, draft.ID, requestID, result.TurnID, fmt.Errorf("commit draft materialization: %w", err))
	}
	s.bindMaterializedActor(actor, threadID)
	s.associateStartedTurn(actor, result.TurnID, requestID, delivery)
	payload, _ := json.Marshal(snapshot)
	s.publish(actor, WorkspaceChatEvent{Type: "thread_materialized", ThreadID: threadID, TurnID: result.TurnID, RequestID: requestID, Payload: payload})
	s.activateNativeEvents(actor)
	return result, nil
}

func (s *WorkspaceChatService) startThreadTurn(ctx context.Context, actor *workspaceChatActor, clientID, requestID string, input []NativeUserInput, delivery *workspaceChatDeliveryTarget) (NativeTurnResult, error) {
	snapshot, err := s.backend.ReadNativeConversation(ctx, actor.workspace, actor.threadID)
	if err != nil {
		return NativeTurnResult{}, fmt.Errorf("workspace chat: refresh native thread: %w", err)
	}
	if err := validateNativeSnapshot(actor.workspace, actor.threadID, snapshot); err != nil {
		return NativeTurnResult{}, fmt.Errorf("workspace chat: refresh native thread: %w", err)
	}
	if snapshot.ActiveTurn != nil {
		return NativeTurnResult{}, fmt.Errorf("%w: use turn_steer with expected_turn_id %s", ErrWorkspaceTurnRunning, snapshot.ActiveTurn.ID)
	}
	if err := s.beginSubmission(ctx, clientID, requestID, actor.workspace.Ref, actor.conversation, "start", "", input); err != nil {
		return NativeTurnResult{}, err
	}
	generation, err := s.waitNativePump(ctx, actor)
	if err != nil {
		_ = s.finishSubmission(requestID, "failed", err.Error())
		return NativeTurnResult{}, fmt.Errorf("workspace chat: subscribe native turn: %w", err)
	}
	actor.mu.Lock()
	actor.pendingStart = &workspaceChatPendingStart{requestID: requestID, delivery: delivery}
	actor.mu.Unlock()
	result, err := s.turns.StartNativeTurn(ctx, actor.workspace, actor.threadID, generation, NativeTurnStartRequest{ClientMessageID: requestID, Input: input})
	if err != nil {
		actor.mu.Lock()
		actor.pendingStart = nil
		actor.mu.Unlock()
		status := "failed"
		if IsNativeAcceptanceUnknown(err) {
			status = "needs_retry"
		}
		_ = s.finishSubmission(requestID, status, err.Error())
		return NativeTurnResult{}, fmt.Errorf("workspace chat: start native turn: %w", err)
	}
	if result.TurnID == "" {
		err = fmt.Errorf("native turn/start returned an empty turn id")
		_ = s.finishSubmission(requestID, "needs_retry", err.Error())
		return NativeTurnResult{}, err
	}
	commitCtx, cancelCommit := s.commitContext()
	err = s.repo.MarkSubmissionAccepted(commitCtx, requestID, actor.threadID, result.TurnID)
	cancelCommit()
	if err != nil {
		err = s.finishAcceptedSubmissionFailure(requestID, err)
		return NativeTurnResult{}, fmt.Errorf("workspace chat: persist accepted turn: %w", err)
	}
	s.associateStartedTurn(actor, result.TurnID, requestID, delivery)
	return result, nil
}

func (s *WorkspaceChatService) beginSubmission(ctx context.Context, clientID, requestID, workspaceRef string, conversation ConversationRef, kind, expectedTurnID string, input []NativeUserInput) error {
	raw, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("workspace chat: encode pending submission: %w", err)
	}
	now := time.Now()
	record := WorkspaceChatSubmission{
		RequestID: requestID, ClientID: clientID, WorkspaceRef: workspaceRef, Conversation: conversation,
		Kind: kind, ExpectedTurnID: expectedTurnID, InputJSON: raw, Status: "pending", CreatedAt: now, UpdatedAt: now,
	}
	if conversation.Kind == ConversationKindThread {
		record.ThreadID = conversation.ID
	}
	if err := s.repo.BeginSubmission(ctx, record); err != nil {
		return fmt.Errorf("workspace chat: persist pending submission: %w", err)
	}
	return nil
}

func (s *WorkspaceChatService) SteerTurn(ctx context.Context, clientID, requestID, workspaceRef, threadID, expectedTurnID string, input []NativeUserInput) (NativeTurnResult, error) {
	return s.steerTurn(ctx, clientID, requestID, workspaceRef, threadID, expectedTurnID, input, false)
}

func (s *WorkspaceChatService) steerTrustedTurn(ctx context.Context, clientID, requestID, workspaceRef, threadID, expectedTurnID string, input []NativeUserInput) (NativeTurnResult, error) {
	return s.steerTurn(ctx, clientID, requestID, workspaceRef, threadID, expectedTurnID, input, true)
}

func (s *WorkspaceChatService) steerTurn(ctx context.Context, clientID, requestID, workspaceRef, threadID, expectedTurnID string, input []NativeUserInput, trusted bool) (NativeTurnResult, error) {
	input, err := s.validateNativeInputs(input, trusted)
	if err != nil {
		return NativeTurnResult{}, err
	}
	snapshot, err := s.ReadThread(ctx, workspaceRef, threadID)
	if err != nil {
		return NativeTurnResult{}, err
	}
	actor := s.actorForThread(workspaceRef, threadID)
	operationCtx, endOperation, err := s.beginActorOperation(ctx, actor)
	if err != nil {
		return NativeTurnResult{}, err
	}
	defer endOperation()
	ctx = operationCtx
	expectedTurnID = strings.TrimSpace(expectedTurnID)
	if snapshot.ActiveTurn == nil {
		return NativeTurnResult{}, ErrWorkspaceTurnNotRunning
	}
	if expectedTurnID == "" || snapshot.ActiveTurn.ID != expectedTurnID {
		return NativeTurnResult{}, fmt.Errorf("%w: active turn is %s", ErrWorkspaceStaleTurn, snapshot.ActiveTurn.ID)
	}
	conversation := ConversationRef{Kind: ConversationKindThread, ID: threadID}
	if err := s.beginSubmission(ctx, clientID, requestID, workspaceRef, conversation, "steer", expectedTurnID, input); err != nil {
		return NativeTurnResult{}, err
	}
	generation, err := s.waitNativePump(ctx, actor)
	if err != nil {
		_ = s.finishSubmission(requestID, "failed", err.Error())
		return NativeTurnResult{}, fmt.Errorf("workspace chat: subscribe native steer: %w", err)
	}
	result, err := s.turns.SteerNativeTurn(ctx, actor.workspace, threadID, generation, expectedTurnID, input)
	if err != nil {
		status := "failed"
		if IsNativeAcceptanceUnknown(err) {
			status = "needs_retry"
		}
		_ = s.finishSubmission(requestID, status, err.Error())
		return NativeTurnResult{}, fmt.Errorf("workspace chat: steer native turn: %w", err)
	}
	if result.TurnID != expectedTurnID {
		err = fmt.Errorf("native turn/steer returned unexpected turn %q", result.TurnID)
		_ = s.finishSubmission(requestID, "needs_retry", err.Error())
		return NativeTurnResult{}, err
	}
	commitCtx, cancelCommit := s.commitContext()
	err = s.repo.MarkSubmissionAccepted(commitCtx, requestID, threadID, result.TurnID)
	cancelCommit()
	if err != nil {
		err = s.finishAcceptedSubmissionFailure(requestID, err)
		return NativeTurnResult{}, fmt.Errorf("workspace chat: persist accepted steer: %w", err)
	}
	actor.mu.Lock()
	actor.submissions[result.TurnID] = append(actor.submissions[result.TurnID], requestID)
	terminal := actor.terminal[result.TurnID]
	actor.mu.Unlock()
	if terminal != "" {
		_ = s.finishSubmission(requestID, terminal, "")
	}
	return result, nil
}

func (s *WorkspaceChatService) finishAcceptedSubmissionFailure(requestID string, cause error) error {
	if err := s.finishSubmission(requestID, "needs_retry", cause.Error()); err != nil {
		return errors.Join(cause, fmt.Errorf("clear accepted submission input: %w", err))
	}
	return cause
}

func (s *WorkspaceChatService) InterruptTurn(ctx context.Context, workspaceRef, threadID, expectedTurnID string) error {
	snapshot, err := s.ReadThread(ctx, workspaceRef, threadID)
	if err != nil {
		return err
	}
	actor := s.actorForThread(workspaceRef, threadID)
	operationCtx, endOperation, err := s.beginActorOperation(ctx, actor)
	if err != nil {
		return err
	}
	defer endOperation()
	ctx = operationCtx
	if snapshot.ActiveTurn == nil {
		return ErrWorkspaceTurnNotRunning
	}
	if strings.TrimSpace(expectedTurnID) == "" || snapshot.ActiveTurn.ID != expectedTurnID {
		return fmt.Errorf("%w: active turn is %s", ErrWorkspaceStaleTurn, snapshot.ActiveTurn.ID)
	}
	generation, err := s.waitNativePump(ctx, actor)
	if err != nil {
		return fmt.Errorf("workspace chat: subscribe native interrupt: %w", err)
	}
	if err := s.turns.InterruptNativeTurn(ctx, actor.workspace, threadID, generation, expectedTurnID); err != nil {
		return fmt.Errorf("workspace chat: interrupt native turn: %w", err)
	}
	return nil
}

func (s *WorkspaceChatService) RespondInteraction(ctx context.Context, workspaceRef, threadID, interactionID string, response json.RawMessage) error {
	if !json.Valid(response) {
		return fmt.Errorf("workspace chat: interaction response must be valid JSON")
	}
	if _, err := s.ReadThread(ctx, workspaceRef, threadID); err != nil {
		return err
	}
	actor := s.actorForThread(workspaceRef, threadID)
	operationCtx, endOperation, err := s.beginActorOperation(ctx, actor)
	if err != nil {
		return err
	}
	defer endOperation()
	ctx = operationCtx
	generation, err := s.waitNativePump(ctx, actor)
	if err != nil {
		return fmt.Errorf("workspace chat: subscribe native interaction: %w", err)
	}
	actor.mu.Lock()
	interaction, ok := actor.pending[strings.TrimSpace(interactionID)]
	actor.mu.Unlock()
	if !ok || interaction.ThreadID != threadID {
		return ErrWorkspaceInteractionStale
	}
	if interaction.ConnectionGeneration != generation {
		return ErrWorkspaceInteractionStale
	}
	if err := validateNativeInteractionResponse(interaction, response); err != nil {
		return err
	}
	if err := s.turns.RespondNativeInteraction(ctx, actor.workspace, threadID, interaction.ConnectionGeneration, interaction.RequestID, response); err != nil {
		return fmt.Errorf("workspace chat: respond native interaction: %w", err)
	}
	actor.mu.Lock()
	delete(actor.pending, interaction.ID)
	if actor.snapshot != nil {
		actor.snapshot.PendingInteractions = removeNativeInteraction(actor.snapshot.PendingInteractions, interaction.ID)
	}
	actor.mu.Unlock()
	commitCtx, cancelCommit := s.commitContext()
	err = s.repo.ResolveInteraction(commitCtx, interaction.ID, "resolved")
	cancelCommit()
	if err != nil {
		s.publish(actor, WorkspaceChatEvent{Type: "error", Error: "native interaction was answered but its local terminal state could not be persisted"})
		return fmt.Errorf("workspace chat: commit interaction response: %w", err)
	}
	resolved := NativeEventEnvelope{
		Method: "serverRequest/resolved", ThreadID: threadID, TurnID: interaction.TurnID,
		ItemID: interaction.ItemID, InteractionID: interaction.ID,
		ConnectionGeneration: interaction.ConnectionGeneration,
		Payload:              json.RawMessage(fmt.Sprintf(`{"interactionId":%q}`, interaction.ID)), OccurredAt: time.Now().UTC(),
	}
	raw, marshalErr := json.Marshal(resolved)
	if marshalErr != nil {
		return fmt.Errorf("workspace chat: encode interaction resolution event: %w", marshalErr)
	}
	s.publish(actor, WorkspaceChatEvent{Type: "native_event", ThreadID: threadID, TurnID: interaction.TurnID, Payload: raw, OccurredAt: resolved.OccurredAt})
	return nil
}

func removeNativeInteraction(interactions []NativeInteraction, interactionID string) []NativeInteraction {
	result := make([]NativeInteraction, 0, len(interactions))
	for _, interaction := range interactions {
		if interaction.ID != interactionID {
			result = append(result, interaction)
		}
	}
	return result
}

func validateNativeInteractionResponse(interaction NativeInteraction, response json.RawMessage) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(response, &object); err != nil {
		return fmt.Errorf("workspace chat: interaction response must be an object")
	}
	if len(interaction.AllowedDecisions) > 0 {
		var decisionRaw json.RawMessage
		for _, key := range []string{"decision", "action"} {
			if raw := object[key]; len(raw) > 0 {
				decisionRaw = raw
				break
			}
		}
		if len(decisionRaw) > 0 && nativeDecisionValueAllowed(interaction.AllowedDecisions, decisionRaw) {
			return nil
		}
		return fmt.Errorf("workspace chat: decision is not declared by the native request")
	}
	switch interaction.Kind {
	case "item/tool/requestUserInput":
		if len(object["answers"]) == 0 {
			return fmt.Errorf("workspace chat: user input response requires answers")
		}
	case "item/permissions/requestApproval":
		if len(object["permissions"]) == 0 {
			return fmt.Errorf("workspace chat: permissions response requires permissions")
		}
	default:
		return fmt.Errorf("workspace chat: native request did not declare an accepted response")
	}
	return nil
}

func nativeDecisionValueAllowed(allowed []json.RawMessage, decision json.RawMessage) bool {
	var requested any
	if json.Unmarshal(decision, &requested) != nil {
		return false
	}
	for _, raw := range allowed {
		var candidate any
		if json.Unmarshal(raw, &candidate) == nil && reflect.DeepEqual(candidate, requested) {
			return true
		}
	}
	return false
}

func cloneNativeDecisionValues(values []json.RawMessage) []json.RawMessage {
	cloned := make([]json.RawMessage, len(values))
	for index, value := range values {
		cloned[index] = append(json.RawMessage(nil), value...)
	}
	return cloned
}

func (s *WorkspaceChatService) StartRealtime(ctx context.Context, owner, workspaceRef, threadID string, request NativeRealtimeStartRequest) error {
	if s.realtime == nil {
		return fmt.Errorf("%w: realtime controller is not available", ErrNativeCapabilityUnavailable)
	}
	if strings.TrimSpace(request.SDP) == "" {
		return fmt.Errorf("workspace chat: realtime SDP is required")
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return fmt.Errorf("workspace chat: realtime owner is required")
	}
	snapshot, err := s.ReadThread(ctx, workspaceRef, threadID)
	if err != nil {
		return err
	}
	if capability := snapshot.Capabilities["realtime"]; !capability.Supported {
		return fmt.Errorf("%w: %s", ErrNativeCapabilityUnavailable, capability.Reason)
	}
	actor := s.actorForThread(workspaceRef, threadID)
	operationCtx, endOperation, err := s.beginActorOperation(ctx, actor)
	if err != nil {
		return err
	}
	defer endOperation()
	ctx = operationCtx
	generation, err := s.waitNativePump(ctx, actor)
	if err != nil {
		return fmt.Errorf("workspace chat: subscribe native realtime: %w", err)
	}
	actor.mu.Lock()
	active := actor.realtime
	terminalSequence := actor.realtimeTerminalSequence
	actor.mu.Unlock()
	if active {
		return fmt.Errorf("workspace chat: realtime is already active")
	}
	if err := s.realtime.StartNativeRealtime(ctx, actor.workspace, threadID, generation, request); err != nil {
		return fmt.Errorf("workspace chat: start native realtime: %w", err)
	}
	actor.mu.Lock()
	if actor.realtimeTerminalSequence == terminalSequence {
		actor.realtime = true
		actor.realtimeOwner = owner
	}
	actor.mu.Unlock()
	return nil
}

func (s *WorkspaceChatService) AppendRealtimeText(ctx context.Context, owner, workspaceRef, threadID, text string) error {
	if s.realtime == nil {
		return fmt.Errorf("%w: realtime controller is not available", ErrNativeCapabilityUnavailable)
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("workspace chat: active realtime and text are required")
	}
	if _, err := s.ReadThread(ctx, workspaceRef, threadID); err != nil {
		return err
	}
	actor := s.actorForThread(workspaceRef, threadID)
	operationCtx, endOperation, err := s.beginActorOperation(ctx, actor)
	if err != nil {
		return err
	}
	defer endOperation()
	ctx = operationCtx
	generation, err := s.waitNativePump(ctx, actor)
	if err != nil {
		return fmt.Errorf("workspace chat: subscribe native realtime text: %w", err)
	}
	actor.mu.Lock()
	active := actor.realtime
	currentOwner := actor.realtimeOwner
	actor.mu.Unlock()
	if !active {
		return fmt.Errorf("workspace chat: realtime is not active")
	}
	if strings.TrimSpace(owner) == "" || currentOwner != strings.TrimSpace(owner) {
		return fmt.Errorf("workspace chat: realtime is owned by another connection")
	}
	return s.realtime.AppendNativeRealtimeText(ctx, actor.workspace, threadID, generation, text)
}

func (s *WorkspaceChatService) StopRealtime(ctx context.Context, owner, workspaceRef, threadID string) error {
	if s.realtime == nil {
		return fmt.Errorf("%w: realtime controller is not available", ErrNativeCapabilityUnavailable)
	}
	if _, err := s.ReadThread(ctx, workspaceRef, threadID); err != nil {
		return err
	}
	actor := s.actorForThread(workspaceRef, threadID)
	operationCtx, endOperation, err := s.beginActorOperation(ctx, actor)
	if err != nil {
		return err
	}
	defer endOperation()
	ctx = operationCtx
	generation, err := s.waitNativePump(ctx, actor)
	if err != nil {
		return fmt.Errorf("workspace chat: subscribe native realtime stop: %w", err)
	}
	actor.mu.Lock()
	active := actor.realtime
	currentOwner := actor.realtimeOwner
	actor.mu.Unlock()
	if !active {
		return nil
	}
	if strings.TrimSpace(owner) == "" || currentOwner != strings.TrimSpace(owner) {
		return fmt.Errorf("workspace chat: realtime is owned by another connection")
	}
	if err := s.realtime.StopNativeRealtime(ctx, actor.workspace, threadID, generation); err != nil {
		return fmt.Errorf("workspace chat: stop native realtime: %w", err)
	}
	actor.mu.Lock()
	actor.realtime = false
	actor.realtimeOwner = ""
	actor.mu.Unlock()
	return nil
}

func (s *WorkspaceChatService) Subscribe(ctx context.Context, clientID, workspaceRef string, conversation ConversationRef, afterEpoch string, afterSequence uint64) (WorkspaceChatSubscription, error) {
	workspace, err := s.resolveWorkspace(ctx, workspaceRef)
	if err != nil {
		return WorkspaceChatSubscription{}, err
	}
	conversation, err = s.validateConversation(ctx, clientID, workspace, conversation)
	if err != nil {
		return WorkspaceChatSubscription{}, err
	}
	actor := s.actor(workspace, conversation)
	operationCtx, endOperation, err := s.beginActorOperation(ctx, actor)
	if err != nil {
		return WorkspaceChatSubscription{}, err
	}
	defer endOperation()
	ctx = operationCtx
	if conversation.Kind == ConversationKindThread {
		if _, err := s.waitNativePump(ctx, actor); err != nil {
			return WorkspaceChatSubscription{}, fmt.Errorf("workspace chat: subscribe native events: %w", err)
		}
	}
	actor.mu.Lock()
	actor.nextSubID++
	id := actor.nextSubID
	ch := make(chan WorkspaceChatEvent, workspaceChatSubscriberDepth)
	actor.subscribers[id] = ch
	current := actor.sequence
	oldest := current + 1
	if len(actor.replay) > 0 {
		oldest = actor.replay[0].Sequence
	}
	resync := afterEpoch == "" || afterEpoch != actor.epoch || afterSequence > current || afterSequence+1 < oldest
	baseline := afterSequence
	if resync {
		baseline = current
	}
	initial := []WorkspaceChatEvent{{
		Type: "subscribed", Epoch: actor.epoch, Sequence: baseline,
		WorkspaceRef: actor.workspace.Ref, Conversation: actor.conversation, ThreadID: actor.threadID, OccurredAt: time.Now(),
	}}
	if resync {
		if afterEpoch != "" {
			initial = append(initial, WorkspaceChatEvent{
				Type: "resync_required", Epoch: actor.epoch, Sequence: current,
				WorkspaceRef: actor.workspace.Ref, Conversation: actor.conversation, ThreadID: actor.threadID,
				Payload: json.RawMessage(`{"reason":"event_gap"}`), OccurredAt: time.Now(),
			})
		}
		initial = append(initial, actor.snapshotEventLocked(current))
	} else {
		for _, event := range actor.replay {
			if event.Sequence > afterSequence {
				initial = append(initial, event)
			}
		}
	}
	actor.mu.Unlock()
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			actor.mu.Lock()
			if current, exists := actor.subscribers[id]; exists {
				delete(actor.subscribers, id)
				close(current)
			}
			actor.mu.Unlock()
			s.scheduleActorRetirement(actor)
		})
	}
	return WorkspaceChatSubscription{Initial: initial, Events: ch, Cancel: cancel}, nil
}

func (s *WorkspaceChatService) actor(workspace Workspace, conversation ConversationRef) *workspaceChatActor {
	key := workspaceChatActorKey(workspace.Ref, conversation)
	s.actorsMu.Lock()
	defer s.actorsMu.Unlock()
	if s.closed {
		return nil
	}
	if actor := s.actors[key]; actor != nil {
		return actor
	}
	actor := &workspaceChatActor{
		workspace: workspace, conversation: conversation, epoch: newWorkspaceChatID("stream"),
		subscribers: make(map[uint64]chan WorkspaceChatEvent), submissions: make(map[string][]string),
		terminal: make(map[string]string), deliveries: make(map[string]*workspaceChatDeliveryTarget), pending: make(map[string]NativeInteraction), nativeAttempt: newWorkspaceChatNativeAttempt(),
		nativeEventsReady: conversation.Kind == ConversationKindThread,
	}
	if conversation.Kind == ConversationKindThread {
		actor.threadID = conversation.ID
	}
	s.actors[key] = actor
	return actor
}

func (s *WorkspaceChatService) actorForThread(workspaceRef, threadID string) *workspaceChatActor {
	key := workspaceChatActorKey(workspaceRef, ConversationRef{Kind: ConversationKindThread, ID: threadID})
	s.actorsMu.Lock()
	if s.closed {
		s.actorsMu.Unlock()
		return nil
	}
	actor := s.actors[key]
	s.actorsMu.Unlock()
	return actor
}

func (s *WorkspaceChatService) beginActorOperation(ctx context.Context, actor *workspaceChatActor) (context.Context, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	operationCtx, cancel := context.WithCancel(ctx)
	stopServiceCancellation := context.AfterFunc(s.operations, cancel)
	cancelOperation := func() {
		stopServiceCancellation()
		cancel()
	}
	if actor == nil {
		s.actorsMu.Lock()
		closed := s.closed
		s.actorsMu.Unlock()
		cancelOperation()
		if closed {
			return nil, nil, ErrWorkspaceChatClosed
		}
		return nil, nil, ErrNativeThreadNotFound
	}
	actor.opMu.Lock()
	s.actorsMu.Lock()
	closed := s.closed
	s.actorsMu.Unlock()
	if closed {
		actor.opMu.Unlock()
		cancelOperation()
		return nil, nil, ErrWorkspaceChatClosed
	}
	actor.mu.Lock()
	actor.idleToken++
	if actor.idleTimer != nil {
		actor.idleTimer.Stop()
		actor.idleTimer = nil
	}
	retired := actor.retired
	actor.mu.Unlock()
	if retired {
		actor.opMu.Unlock()
		cancelOperation()
		return nil, nil, ErrNativeThreadNotFound
	}
	if err := operationCtx.Err(); err != nil {
		actor.opMu.Unlock()
		cancelOperation()
		return nil, nil, err
	}
	endOperation := func() {
		cancelOperation()
		actor.opMu.Unlock()
		s.scheduleActorRetirement(actor)
	}
	return operationCtx, endOperation, nil
}

func workspaceChatActorIdleLocked(actor *workspaceChatActor) bool {
	return actor.activeTurnID == "" && actor.pendingStart == nil && len(actor.submissions) == 0 &&
		len(actor.pending) == 0 && len(actor.subscribers) == 0 && !actor.realtime && len(actor.deliveries) == 0
}

func (s *WorkspaceChatService) scheduleActorRetirement(actor *workspaceChatActor) {
	if actor == nil || actor.conversation.Kind != ConversationKindThread || workspaceChatActorIdleTimeout <= 0 {
		return
	}
	actor.mu.Lock()
	if actor.retired || !workspaceChatActorIdleLocked(actor) {
		actor.mu.Unlock()
		return
	}
	actor.idleToken++
	token := actor.idleToken
	if actor.idleTimer != nil {
		actor.idleTimer.Stop()
	}
	actor.idleTimer = time.AfterFunc(workspaceChatActorIdleTimeout, func() {
		actor.opMu.Lock()
		defer actor.opMu.Unlock()
		actor.mu.Lock()
		if actor.retired || actor.idleToken != token || !workspaceChatActorIdleLocked(actor) {
			actor.mu.Unlock()
			return
		}
		actor.retired = true
		actor.idleTimer = nil
		actor.mu.Unlock()
		if err := actor.nativePump.Close(workspaceChatPumpCloseTimeout); err != nil {
			slog.Warn("workspace chat idle actor pump close failed", "thread", actor.threadID, "error", err)
		}
		key := workspaceChatActorKey(actor.workspace.Ref, actor.conversation)
		s.actorsMu.Lock()
		if s.actors[key] == actor {
			delete(s.actors, key)
		}
		s.actorsMu.Unlock()
	})
	actor.mu.Unlock()
}

func (s *WorkspaceChatService) bindMaterializedActor(actor *workspaceChatActor, threadID string) {
	s.actorsMu.Lock()
	delete(s.actors, workspaceChatActorKey(actor.workspace.Ref, actor.conversation))
	conversation := ConversationRef{Kind: ConversationKindThread, ID: threadID}
	actor.mu.Lock()
	actor.conversation = conversation
	actor.threadID = threadID
	actor.mu.Unlock()
	s.actors[workspaceChatActorKey(actor.workspace.Ref, conversation)] = actor
	s.actorsMu.Unlock()
}

func (s *WorkspaceChatService) markDraftMaterializationUncertain(actor *workspaceChatActor, turnID string) {
	actor.mu.Lock()
	actor.materializationUncertain = true
	actor.activeTurnID = turnID
	actor.pendingStart = nil
	actor.deliveries = make(map[string]*workspaceChatDeliveryTarget)
	actor.mu.Unlock()
}

func (s *WorkspaceChatService) failDraftMaterialization(actor *workspaceChatActor, draftID, requestID, turnID string, cause error) error {
	s.markDraftMaterializationUncertain(actor, turnID)
	result := errors.Join(ErrWorkspaceDraftMaterializationUncertain, cause)
	ctx, cancel := s.commitContext()
	markErr := s.repo.MarkDraftMaterializationUncertain(ctx, draftID)
	cancel()
	if markErr != nil {
		result = errors.Join(result, fmt.Errorf("persist uncertain draft state: %w", markErr))
	}
	if finishErr := s.finishSubmission(requestID, "needs_retry", cause.Error()); finishErr != nil {
		result = errors.Join(result, fmt.Errorf("clear uncertain submission input: %w", finishErr))
	}
	return result
}

// PublishOperationError 把已验证 conversation 的操作错误放入同一 actor 有序事件流。
func (s *WorkspaceChatService) PublishOperationError(ctx context.Context, clientID, workspaceRef string, conversation ConversationRef, requestID, message string) error {
	workspace, err := s.resolveWorkspace(ctx, workspaceRef)
	if err != nil {
		return err
	}
	conversation, err = s.validateConversation(ctx, strings.TrimSpace(clientID), workspace, conversation)
	if err != nil {
		return err
	}
	actor := s.actor(workspace, conversation)
	_, endOperation, err := s.beginActorOperation(ctx, actor)
	if err != nil {
		return err
	}
	defer endOperation()
	s.publish(actor, WorkspaceChatEvent{Type: "error", RequestID: strings.TrimSpace(requestID), Error: message})
	return nil
}

func workspaceChatActorKey(workspaceRef string, conversation ConversationRef) string {
	return workspaceRef + "\x00" + string(conversation.Kind) + "\x00" + conversation.ID
}

func (s *WorkspaceChatService) ensureNativePump(actor *workspaceChatActor) error {
	actor.mu.Lock()
	retired := actor.retired
	actor.mu.Unlock()
	if retired {
		return ErrNativeThreadNotFound
	}
	err := actor.nativePump.Start(s.ctx, func(ctx context.Context) {
		s.runNativePump(ctx, actor)
	})
	if errors.Is(err, errTurnLifecyclePumpRunning) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("workspace chat: start native event pump: %w", err)
	}
	return nil
}

// retireProvisionalActor 只回收从未通过权威 thread/cwd 校验的空 actor。
// 调用方必须持有 actor.opMu，避免退役与 Turn、订阅或 realtime mutation 交错。
func (s *WorkspaceChatService) retireProvisionalActor(actor *workspaceChatActor) error {
	if actor == nil || actor.conversation.Kind != ConversationKindThread {
		return nil
	}
	actor.mu.Lock()
	if actor.retired {
		actor.mu.Unlock()
		return nil
	}
	provisional := actor.snapshot == nil && actor.activeTurnID == "" && actor.pendingStart == nil &&
		len(actor.submissions) == 0 && len(actor.pending) == 0 && len(actor.subscribers) == 0 &&
		!actor.realtime && len(actor.deliveries) == 0
	if !provisional {
		actor.mu.Unlock()
		return nil
	}
	actor.retired = true
	actor.mu.Unlock()

	if err := actor.nativePump.Close(workspaceChatPumpCloseTimeout); err != nil {
		return fmt.Errorf("workspace chat: close rejected thread actor: %w", err)
	}
	key := workspaceChatActorKey(actor.workspace.Ref, actor.conversation)
	s.actorsMu.Lock()
	if s.actors[key] == actor {
		delete(s.actors, key)
	}
	s.actorsMu.Unlock()
	return nil
}

func (s *WorkspaceChatService) runNativePump(ctx context.Context, actor *workspaceChatActor) {
	for ctx.Err() == nil {
		actor.mu.Lock()
		threadID := actor.threadID
		attempt := actor.nativeAttempt
		if attempt == nil || attempt.completed {
			attempt = newWorkspaceChatNativeAttempt()
			actor.nativeAttempt = attempt
		}
		actor.mu.Unlock()
		if threadID == "" {
			if !waitWorkspaceChat(ctx, 50*time.Millisecond) {
				return
			}
			continue
		}
		subscription, err := s.backend.SubscribeNativeConversation(ctx, actor.workspace, threadID)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			completeWorkspaceChatNativeAttempt(actor, attempt, 0, err)
			s.publishNativePumpEvent(actor, WorkspaceChatEvent{Type: "resync_required", Error: err.Error(), Payload: json.RawMessage(`{"reason":"native_subscribe_failed"}`)})
			if !waitWorkspaceChat(ctx, time.Second) {
				return
			}
			continue
		}
		if subscription.Generation == 0 || subscription.Events == nil || subscription.Cancel == nil {
			if subscription.Cancel != nil {
				subscription.Cancel()
			}
			err = fmt.Errorf("native subscription is incomplete")
			completeWorkspaceChatNativeAttempt(actor, attempt, 0, err)
			s.publishNativePumpEvent(actor, WorkspaceChatEvent{Type: "resync_required", Error: err.Error(), Payload: json.RawMessage(`{"reason":"native_subscribe_failed"}`)})
			if !waitWorkspaceChat(ctx, time.Second) {
				return
			}
			continue
		}
		snapshot, err := s.backend.ReadNativeConversation(ctx, actor.workspace, threadID)
		if err != nil {
			subscription.Cancel()
			if ctx.Err() != nil {
				return
			}
			completeWorkspaceChatNativeAttempt(actor, attempt, 0, err)
			s.publishNativePumpEvent(actor, WorkspaceChatEvent{Type: "resync_required", Error: err.Error(), Payload: json.RawMessage(`{"reason":"native_snapshot_failed"}`)})
			if !waitWorkspaceChat(ctx, time.Second) {
				return
			}
			continue
		}
		if err := validateNativeSnapshot(actor.workspace, threadID, snapshot); err != nil {
			err = fmt.Errorf("%w: %v", ErrNativeThreadNotFound, err)
			subscription.Cancel()
			if ctx.Err() != nil {
				return
			}
			completeWorkspaceChatNativeAttempt(actor, attempt, 0, err)
			s.publishNativePumpEvent(actor, WorkspaceChatEvent{Type: "resync_required", Error: err.Error(), Payload: json.RawMessage(`{"reason":"native_snapshot_failed"}`)})
			if !waitWorkspaceChat(ctx, time.Second) {
				return
			}
			continue
		}
		if err := s.reconcileNativeSnapshotInteractions(actor, &snapshot, subscription.Generation); err != nil {
			subscription.Cancel()
			if ctx.Err() != nil {
				return
			}
			completeWorkspaceChatNativeAttempt(actor, attempt, 0, err)
			s.publishNativePumpEvent(actor, WorkspaceChatEvent{Type: "resync_required", Error: err.Error(), Payload: json.RawMessage(`{"reason":"native_snapshot_interactions_failed"}`)})
			if !waitWorkspaceChat(ctx, time.Second) {
				return
			}
			continue
		}
		actorSnapshot := cloneNativeConversationSnapshot(snapshot)
		actor.mu.Lock()
		actor.snapshot = &actorSnapshot
		actor.activeTurnID = ""
		if snapshot.ActiveTurn != nil {
			actor.activeTurnID = snapshot.ActiveTurn.ID
		}
		actor.nativeGeneration = subscription.Generation
		actor.nativeActive = true
		attempt.generation = subscription.Generation
		attempt.completed = true
		close(attempt.ready)
		actor.mu.Unlock()
		snapshotPayload, _ := json.Marshal(snapshot)
		s.publishNativePumpEvent(actor, WorkspaceChatEvent{Type: "snapshot", Payload: snapshotPayload})
		exit := runTurnLifecycleEventPump(ctx, subscription.Events, turnLifecycleEventCallbacks[NativeEventEnvelope]{
			Handle: func(event NativeEventEnvelope) bool {
				if event.ConnectionGeneration == subscription.Generation {
					s.dispatchNativeEvent(actor, event)
				}
				return true
			},
			Cleanup: func(turnLifecycleEventExit) {
				subscription.Cancel()
			},
		})
		if exit == turnLifecycleEventContextDone || ctx.Err() != nil {
			return
		}
		actor.mu.Lock()
		wasRealtime := actor.realtime
		pending := make([]string, 0, len(actor.pending))
		for id := range actor.pending {
			pending = append(pending, id)
		}
		actor.pending = make(map[string]NativeInteraction)
		if actor.snapshot != nil {
			actor.snapshot.PendingInteractions = nil
		}
		actor.realtime = false
		actor.nativeActive = false
		actor.nativeGeneration = 0
		actor.nativeAttempt = newWorkspaceChatNativeAttempt()
		actor.mu.Unlock()
		for _, interactionID := range pending {
			commitCtx, cancelCommit := s.commitContext()
			err := s.repo.ResolveInteraction(commitCtx, interactionID, "connection_lost")
			cancelCommit()
			if err != nil {
				slog.Error("workspace chat interaction expiration failed", "interaction", interactionID, "error", err)
			}
		}
		if wasRealtime {
			payload := json.RawMessage(`{"reason":"native_connection_changed"}`)
			s.dispatchNativeEvent(actor, NativeEventEnvelope{
				Method: "thread/realtime/closed", ThreadID: threadID,
				ConnectionGeneration: subscription.Generation, Payload: payload, OccurredAt: time.Now().UTC(),
			})
		}
		s.publishNativePumpEvent(actor, WorkspaceChatEvent{Type: "resync_required", Payload: json.RawMessage(`{"reason":"native_connection_changed"}`)})
		if !waitWorkspaceChat(ctx, time.Second) {
			return
		}
	}
}

func (s *WorkspaceChatService) reconcileNativeSnapshotInteractions(actor *workspaceChatActor, snapshot *NativeConversationSnapshot, generation uint64) error {
	canonical := make(map[string]NativeInteraction, len(snapshot.PendingInteractions))
	for _, raw := range snapshot.PendingInteractions {
		interaction, err := normalizeNativeSnapshotInteraction(raw, actor.threadID, generation)
		if err != nil {
			return fmt.Errorf("workspace chat: invalid native snapshot interaction: %w", err)
		}
		ctx, cancel := s.commitContext()
		err = s.repo.PutInteraction(ctx, WorkspaceChatInteractionRecord{
			Interaction: interaction, ConnectionGeneration: generation, Status: "pending",
		})
		cancel()
		if err != nil {
			return fmt.Errorf("workspace chat: persist native snapshot interaction %s: %w", interaction.ID, err)
		}
		canonical[interaction.ID] = interaction
	}

	ctx, cancel := s.commitContext()
	records, err := s.repo.ListPendingInteractions(ctx, actor.threadID)
	cancel()
	if err != nil {
		return fmt.Errorf("workspace chat: load reconciled native interactions: %w", err)
	}
	for _, record := range records {
		status := "resolved"
		if record.ConnectionGeneration != generation {
			status = "connection_lost"
		}
		if _, exists := canonical[record.Interaction.ID]; !exists {
			ctx, cancel := s.commitContext()
			err := s.repo.ResolveInteraction(ctx, record.Interaction.ID, status)
			cancel()
			if err != nil {
				return fmt.Errorf("workspace chat: expire stale native interaction %s: %w", record.Interaction.ID, err)
			}
		}
	}

	interactions := make([]NativeInteraction, 0, len(canonical))
	for _, interaction := range canonical {
		interactions = append(interactions, publicInteraction(interaction))
	}
	sort.Slice(interactions, func(i, j int) bool {
		if interactions[i].OccurredAt.Equal(interactions[j].OccurredAt) {
			return interactions[i].ID < interactions[j].ID
		}
		return interactions[i].OccurredAt.Before(interactions[j].OccurredAt)
	})
	snapshot.PendingInteractions = interactions
	actor.mu.Lock()
	actor.pending = canonical
	actor.mu.Unlock()
	return nil
}

func normalizeNativeSnapshotInteraction(interaction NativeInteraction, threadID string, generation uint64) (NativeInteraction, error) {
	interaction.ID = strings.TrimSpace(interaction.ID)
	interaction.ThreadID = strings.TrimSpace(interaction.ThreadID)
	interaction.TurnID = strings.TrimSpace(interaction.TurnID)
	interaction.ItemID = strings.TrimSpace(interaction.ItemID)
	if interaction.ID == "" || interaction.ThreadID != threadID || interaction.ConnectionGeneration != generation {
		return NativeInteraction{}, fmt.Errorf("interaction identity does not match the subscribed thread generation")
	}
	if len(interaction.RequestID) == 0 || !json.Valid(interaction.RequestID) || string(interaction.RequestID) == "null" {
		return NativeInteraction{}, fmt.Errorf("interaction %s has an invalid JSON-RPC request id", interaction.ID)
	}
	if len(interaction.Payload) == 0 || !json.Valid(interaction.Payload) {
		return NativeInteraction{}, fmt.Errorf("interaction %s has an invalid payload", interaction.ID)
	}
	for _, decision := range interaction.AllowedDecisions {
		if !json.Valid(decision) {
			return NativeInteraction{}, fmt.Errorf("interaction %s has an invalid allowed decision", interaction.ID)
		}
	}
	if !nativeInteractionMethodSupported(interaction.Kind) {
		return NativeInteraction{}, fmt.Errorf("interaction %s has unsupported method %q", interaction.ID, interaction.Kind)
	}
	interaction.RequestID = append(json.RawMessage(nil), interaction.RequestID...)
	interaction.Payload = append(json.RawMessage(nil), interaction.Payload...)
	interaction.AllowedDecisions = cloneNativeDecisionValues(interaction.AllowedDecisions)
	return interaction, nil
}

func (s *WorkspaceChatService) waitNativePump(ctx context.Context, actor *workspaceChatActor) (uint64, error) {
	if err := s.ensureNativePump(actor); err != nil {
		return 0, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if err := s.ctx.Err(); err != nil {
			return 0, err
		}
		actor.mu.Lock()
		if actor.nativeActive && actor.nativeGeneration != 0 {
			generation := actor.nativeGeneration
			actor.mu.Unlock()
			return generation, nil
		}
		attempt := actor.nativeAttempt
		if attempt == nil {
			attempt = newWorkspaceChatNativeAttempt()
			actor.nativeAttempt = attempt
		}
		actor.mu.Unlock()
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-s.ctx.Done():
			return 0, context.Canceled
		case <-attempt.ready:
			if attempt.err != nil {
				return 0, attempt.err
			}
			actor.mu.Lock()
			connected := actor.nativeActive && actor.nativeGeneration != 0 && actor.nativeGeneration == attempt.generation
			generation := actor.nativeGeneration
			actor.mu.Unlock()
			if connected {
				return generation, nil
			}
		}
	}
}

func completeWorkspaceChatNativeAttempt(actor *workspaceChatActor, attempt *workspaceChatNativeAttempt, generation uint64, err error) {
	actor.mu.Lock()
	defer actor.mu.Unlock()
	if attempt == nil || attempt.completed || actor.nativeAttempt != attempt {
		return
	}
	attempt.generation = generation
	attempt.err = err
	attempt.completed = true
	if err != nil {
		actor.nativeActive = false
		actor.nativeGeneration = 0
		actor.nativeAttempt = newWorkspaceChatNativeAttempt()
	}
	close(attempt.ready)
}

func (s *WorkspaceChatService) publishNativePumpEvent(actor *workspaceChatActor, event WorkspaceChatEvent) {
	actor.mu.Lock()
	ready := actor.nativeEventsReady
	actor.mu.Unlock()
	if ready {
		s.publish(actor, event)
	}
}

func (s *WorkspaceChatService) dispatchNativeEvent(actor *workspaceChatActor, event NativeEventEnvelope) {
	actor.nativeEventMu.Lock()
	defer actor.nativeEventMu.Unlock()
	actor.mu.Lock()
	if !actor.nativeEventsReady {
		event.Payload = append(json.RawMessage(nil), event.Payload...)
		event.RequestID = append(json.RawMessage(nil), event.RequestID...)
		event.AllowedDecisions = cloneNativeDecisionValues(event.AllowedDecisions)
		if event.Settings != nil {
			settings := cloneNativeThreadSettings(*event.Settings)
			event.Settings = &settings
		}
		actor.nativeStagedEvents = append(actor.nativeStagedEvents, event)
		actor.mu.Unlock()
		return
	}
	actor.mu.Unlock()
	s.handleNativeEvent(actor, event)
}

func (s *WorkspaceChatService) activateNativeEvents(actor *workspaceChatActor) {
	actor.nativeEventMu.Lock()
	defer actor.nativeEventMu.Unlock()
	actor.mu.Lock()
	actor.nativeEventsReady = true
	staged := actor.nativeStagedEvents
	actor.nativeStagedEvents = nil
	actor.mu.Unlock()
	for _, event := range staged {
		s.handleNativeEvent(actor, event)
	}
}

func (s *WorkspaceChatService) handleNativeEvent(actor *workspaceChatActor, event NativeEventEnvelope) {
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now()
	}
	interaction := nativeInteractionFromEvent(event)
	if interaction != nil {
		commitCtx, cancelCommit := s.commitContext()
		err := s.repo.PutInteraction(commitCtx, WorkspaceChatInteractionRecord{
			Interaction: *interaction, ConnectionGeneration: interaction.ConnectionGeneration, Status: "pending",
		})
		cancelCommit()
		if err != nil {
			s.publish(actor, WorkspaceChatEvent{Type: "error", Error: "failed to persist native interaction"})
			slog.Error("workspace chat interaction persistence failed", "thread", actor.threadID, "error", err)
			return
		}
		actor.mu.Lock()
		actor.pending[interaction.ID] = *interaction
		if actor.snapshot != nil {
			actor.snapshot.PendingInteractions = upsertPublicNativeInteraction(actor.snapshot.PendingInteractions, *interaction)
		}
		actor.mu.Unlock()
		event.InteractionID = interaction.ID
		event.AllowedDecisions = cloneNativeDecisionValues(interaction.AllowedDecisions)
	}
	method := event.Method
	var finishIDs []string
	var terminalStatus string
	var terminalError string
	var terminalDelivery *workspaceChatDeliveryTarget
	var resolvedInteractionID string
	var settingsError string
	actor.mu.Lock()
	switch method {
	case "turn/started":
		if event.TurnID != "" {
			actor.activeTurnID = event.TurnID
			if actor.snapshot != nil {
				actor.snapshot.ActiveTurn = &NativeActiveTurn{ID: event.TurnID, StartedAt: event.OccurredAt}
			}
			if actor.pendingStart != nil {
				actor.submissions[event.TurnID] = append(actor.submissions[event.TurnID], actor.pendingStart.requestID)
				if actor.pendingStart.delivery != nil {
					actor.deliveries[event.TurnID] = actor.pendingStart.delivery
				}
				actor.pendingStart = nil
			}
		}
	case "turn/completed":
		if event.TurnID != "" {
			terminalDelivery = actor.deliveries[event.TurnID]
			if terminalDelivery == nil && actor.pendingStart != nil {
				terminalDelivery = actor.pendingStart.delivery
			}
			actor.activeTurnID = ""
			if actor.snapshot != nil {
				actor.snapshot.ActiveTurn = nil
			}
			var statusErr error
			terminalStatus, statusErr = nativeTurnTerminalStatus(event.Payload)
			if statusErr != nil {
				terminalStatus = "needs_retry"
				terminalError = statusErr.Error()
			}
			actor.terminal[event.TurnID] = terminalStatus
			finishIDs = append(finishIDs, actor.submissions[event.TurnID]...)
			delete(actor.submissions, event.TurnID)
			delete(actor.deliveries, event.TurnID)
		}
	case "thread/settings/updated":
		if event.Settings == nil || strings.TrimSpace(event.Settings.Revision) == "" {
			settingsError = "thread/settings/updated did not include canonical typed settings"
		} else if actor.snapshot != nil {
			actor.snapshot.Settings = cloneNativeThreadSettings(*event.Settings)
		}
	case "serverRequest/resolved":
		resolvedInteractionID = strings.TrimSpace(event.InteractionID)
		if resolvedInteractionID != "" {
			delete(actor.pending, resolvedInteractionID)
			if actor.snapshot != nil {
				actor.snapshot.PendingInteractions = removeNativeInteraction(actor.snapshot.PendingInteractions, resolvedInteractionID)
			}
		}
	case "thread/realtime/closed", "thread/realtime/error":
		actor.realtime = false
		actor.realtimeOwner = ""
		actor.realtimeTerminalSequence++
	case "thread/status/changed":
		if actor.snapshot != nil {
			actor.snapshot.Status = append(json.RawMessage(nil), event.Payload...)
		}
	case "thread/tokenUsage/updated":
		if actor.snapshot != nil {
			actor.snapshot.Usage = append(json.RawMessage(nil), event.Payload...)
		}
	}
	actor.mu.Unlock()
	if resolvedInteractionID != "" {
		commitCtx, cancelCommit := s.commitContext()
		err := s.repo.ResolveInteraction(commitCtx, resolvedInteractionID, "resolved")
		cancelCommit()
		if err != nil {
			slog.Error("workspace chat interaction resolution persistence failed", "interaction", resolvedInteractionID, "error", err)
		}
	}
	for _, requestID := range finishIDs {
		if err := s.finishSubmission(requestID, terminalStatus, terminalError); err != nil {
			slog.Error("workspace chat submission terminal persistence failed", "request", requestID, "error", err)
		}
	}
	raw, err := json.Marshal(event)
	if err == nil {
		s.publish(actor, WorkspaceChatEvent{Type: "native_event", ThreadID: event.ThreadID, TurnID: event.TurnID, Payload: raw, OccurredAt: event.OccurredAt})
	}
	if terminalError != "" {
		s.publish(actor, WorkspaceChatEvent{Type: "error", TurnID: event.TurnID, Error: terminalError})
	}
	if settingsError != "" {
		s.publish(actor, WorkspaceChatEvent{Type: "error", Error: settingsError})
	}
	if interaction != nil {
		s.deliverInteraction(actor, *interaction)
	}
	if method == "item/completed" {
		if text := nativeAssistantText(event.Payload); text != "" {
			s.deliverAssistant(actor, event.TurnID, text)
		}
	}
	if method == "turn/completed" && terminalStatus != "completed" {
		if terminalDelivery != nil {
			s.deliverPlatform(actor.workspace.Ref, actor.conversation, terminalDelivery, s.engine.i18n.Tf(MsgWorkspaceChatTurnEndedStatus, terminalStatus), "assistant")
		}
	}
	if method == "turn/completed" || method == "serverRequest/resolved" || method == "thread/realtime/closed" || method == "thread/realtime/error" {
		s.scheduleActorRetirement(actor)
	}
}

func upsertPublicNativeInteraction(interactions []NativeInteraction, interaction NativeInteraction) []NativeInteraction {
	public := publicInteraction(interaction)
	for index := range interactions {
		if interactions[index].ID == interaction.ID {
			interactions[index] = public
			return interactions
		}
	}
	return append(interactions, public)
}

func (s *WorkspaceChatService) associateStartedTurn(actor *workspaceChatActor, turnID, requestID string, delivery *workspaceChatDeliveryTarget) {
	actor.mu.Lock()
	actor.pendingStart = nil
	if !containsString(actor.submissions[turnID], requestID) {
		actor.submissions[turnID] = append(actor.submissions[turnID], requestID)
	}
	if delivery != nil {
		actor.deliveries[turnID] = delivery
	}
	terminal := actor.terminal[turnID]
	if terminal == "" {
		actor.activeTurnID = turnID
		if actor.snapshot != nil {
			actor.snapshot.ActiveTurn = &NativeActiveTurn{ID: turnID, RequestID: requestID, StartedAt: time.Now()}
		}
	} else {
		delete(actor.submissions, turnID)
		delete(actor.deliveries, turnID)
	}
	actor.mu.Unlock()
	if terminal != "" {
		_ = s.finishSubmission(requestID, terminal, "")
	}
}

func (s *WorkspaceChatService) publish(actor *workspaceChatActor, event WorkspaceChatEvent) {
	actor.mu.Lock()
	defer actor.mu.Unlock()
	if s.ctx.Err() != nil {
		return
	}
	actor.sequence++
	event.Epoch = actor.epoch
	event.Sequence = actor.sequence
	event.WorkspaceRef = actor.workspace.Ref
	event.Conversation = actor.conversation
	if event.ThreadID == "" {
		event.ThreadID = actor.threadID
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now()
	}
	actor.replay = append(actor.replay, event)
	if len(actor.replay) > workspaceChatReplayLimit {
		actor.replay = append([]WorkspaceChatEvent(nil), actor.replay[len(actor.replay)-workspaceChatReplayLimit:]...)
	}
	for _, ch := range actor.subscribers {
		select {
		case ch <- event:
		default:
			for {
				select {
				case <-ch:
				default:
					goto drained
				}
			}
		drained:
			ch <- WorkspaceChatEvent{
				Type: "resync_required", Epoch: actor.epoch, Sequence: actor.sequence,
				WorkspaceRef: actor.workspace.Ref, Conversation: actor.conversation, ThreadID: actor.threadID,
				Payload: json.RawMessage(`{"reason":"subscriber_overflow"}`), OccurredAt: time.Now(),
			}
			ch <- actor.snapshotEventLocked(actor.sequence)
		}
	}
}

func (actor *workspaceChatActor) snapshotEventLocked(sequence uint64) WorkspaceChatEvent {
	var payload json.RawMessage
	if actor.conversation.Kind == ConversationKindThread && actor.snapshot != nil {
		payload, _ = json.Marshal(actor.snapshot)
	} else {
		payload, _ = json.Marshal(map[string]any{"conversation": actor.conversation, "materialized": false})
	}
	return WorkspaceChatEvent{
		Type: "snapshot", Epoch: actor.epoch, Sequence: sequence, WorkspaceRef: actor.workspace.Ref,
		Conversation: actor.conversation, ThreadID: actor.threadID, Payload: payload, OccurredAt: time.Now(),
	}
}

func (s *WorkspaceChatService) deliverAssistant(actor *workspaceChatActor, turnID, content string) {
	actor.mu.Lock()
	target := actor.deliveries[strings.TrimSpace(turnID)]
	conversation := actor.conversation
	actor.mu.Unlock()
	if target == nil || target.platform == nil || strings.TrimSpace(content) == "" {
		return
	}
	s.deliverPlatform(actor.workspace.Ref, conversation, target, content, "assistant")
}

func (s *WorkspaceChatService) deliverInteraction(actor *workspaceChatActor, interaction NativeInteraction) {
	actor.mu.Lock()
	target := actor.deliveries[strings.TrimSpace(interaction.TurnID)]
	conversation := actor.conversation
	actor.mu.Unlock()
	if target == nil || target.platform == nil {
		return
	}
	content := s.engine.i18n.Tf(MsgWorkspaceChatInteractionDelivery, interaction.Kind, interaction.ID)
	s.deliverPlatform(actor.workspace.Ref, conversation, target, content, "interaction")
}

func (s *WorkspaceChatService) deliverPlatform(workspaceRef string, conversation ConversationRef, target *workspaceChatDeliveryTarget, content, kind string) {
	s.deliveryMu.Lock()
	if s.deliveryClosed {
		s.deliveryMu.Unlock()
		return
	}
	s.workers.Add(1)
	s.deliveryMu.Unlock()
	workerOwned := true
	defer func() {
		if workerOwned {
			s.workers.Done()
		}
	}()

	deliveryID := newWorkspaceChatID("delivery")
	metadata, _ := json.Marshal(map[string]string{"kind": kind})
	record := WorkspaceChatDeliveryRecord{
		ID: deliveryID, ClientID: target.clientID, WorkspaceRef: workspaceRef, Conversation: conversation,
		RequestID: target.requestID, Transport: target.platform.Name(), Destination: target.destination,
		Status: "pending", Metadata: metadata, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := s.repo.PutDelivery(context.Background(), record); err != nil {
		slog.Error("workspace chat delivery persistence failed", "delivery", deliveryID, "error", err)
		return
	}
	workerOwned = false
	go func() {
		defer s.workers.Done()
		err := target.platform.Send(s.ctx, target.replyCtx, content)
		status, message := "delivered", ""
		if err != nil {
			status, message = "failed", workspaceChatErrorTransportSendFailed
			slog.Error("workspace chat delivery send failed", "delivery", deliveryID, "transport", target.platform.Name(), "error_type", fmt.Sprintf("%T", err))
		}
		if finishErr := s.repo.FinishDelivery(context.Background(), deliveryID, status, message); finishErr != nil {
			slog.Error("workspace chat delivery terminal persistence failed", "delivery", deliveryID, "error", finishErr)
		}
	}()
}

func validateNativeSnapshot(workspace Workspace, threadID string, snapshot NativeConversationSnapshot) error {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" || snapshot.Thread.ID != threadID {
		return fmt.Errorf("native snapshot returned a different thread")
	}
	if !sameWorkspacePath(snapshot.Thread.Cwd, workspace.RootPath) {
		return fmt.Errorf("native snapshot does not belong to workspace")
	}
	deepLink := strings.TrimSpace(snapshot.DeepLink)
	parsed, err := url.Parse(deepLink)
	if err != nil || deepLink == "" || !parsed.IsAbs() {
		if err == nil {
			err = fmt.Errorf("deep link must be an absolute URI")
		}
		return fmt.Errorf("native snapshot has invalid deep link: %w", err)
	}
	return nil
}

func nativeInteractionFromEvent(event NativeEventEnvelope) *NativeInteraction {
	if len(event.RequestID) == 0 || string(event.RequestID) == "null" {
		return nil
	}
	if !nativeInteractionMethodSupported(event.Method) {
		return nil
	}
	interactionID := strings.TrimSpace(event.InteractionID)
	if interactionID == "" {
		hash := sha256.Sum256(append([]byte(fmt.Sprintf("%d\x00%s\x00", event.ConnectionGeneration, event.ThreadID)), event.RequestID...))
		interactionID = hex.EncodeToString(hash[:16])
	}
	return &NativeInteraction{
		ID: interactionID, Kind: event.Method, ThreadID: event.ThreadID, TurnID: event.TurnID,
		ItemID: event.ItemID, RequestID: append(json.RawMessage(nil), event.RequestID...),
		ConnectionGeneration: event.ConnectionGeneration, AllowedDecisions: cloneNativeDecisionValues(event.AllowedDecisions),
		Payload: append(json.RawMessage(nil), event.Payload...), OccurredAt: event.OccurredAt,
	}
}

func nativeInteractionMethodSupported(method string) bool {
	switch method {
	case "item/permissions/requestApproval", "item/commandExecution/requestApproval",
		"item/fileChange/requestApproval", "item/tool/requestUserInput", "mcpServer/elicitation/request":
		return true
	default:
		return false
	}
}

func publicInteraction(interaction NativeInteraction) NativeInteraction {
	interaction.RequestID = nil
	interaction.ConnectionGeneration = 0
	return interaction
}

func cloneNativeConversationSnapshot(snapshot NativeConversationSnapshot) NativeConversationSnapshot {
	clone := snapshot
	clone.Thread.Status = append(json.RawMessage(nil), snapshot.Thread.Status...)
	clone.Status = append(json.RawMessage(nil), snapshot.Status...)
	clone.Usage = append(json.RawMessage(nil), snapshot.Usage...)
	clone.Settings = cloneNativeThreadSettings(snapshot.Settings)
	clone.Capabilities = make(map[string]CapabilityStatus, len(snapshot.Capabilities))
	for key, value := range snapshot.Capabilities {
		clone.Capabilities[key] = value
	}
	if snapshot.ActiveTurn != nil {
		active := *snapshot.ActiveTurn
		clone.ActiveTurn = &active
	}
	clone.PendingInteractions = make([]NativeInteraction, 0, len(snapshot.PendingInteractions))
	for _, interaction := range snapshot.PendingInteractions {
		interaction.RequestID = append(json.RawMessage(nil), interaction.RequestID...)
		interaction.Payload = append(json.RawMessage(nil), interaction.Payload...)
		interaction.AllowedDecisions = cloneNativeDecisionValues(interaction.AllowedDecisions)
		clone.PendingInteractions = append(clone.PendingInteractions, interaction)
	}
	return clone
}

func cloneNativeThreadSettings(settings NativeThreadSettings) NativeThreadSettings {
	clone := settings
	clone.ApprovalPolicy = append(json.RawMessage(nil), settings.ApprovalPolicy...)
	clone.SandboxPolicy = append(json.RawMessage(nil), settings.SandboxPolicy...)
	if settings.CollaborationMode != nil {
		mode := *settings.CollaborationMode
		if mode.Settings.ReasoningEffort != nil {
			effort := *mode.Settings.ReasoningEffort
			mode.Settings.ReasoningEffort = &effort
		}
		if mode.Settings.DeveloperInstructions != nil {
			instructions := *mode.Settings.DeveloperInstructions
			mode.Settings.DeveloperInstructions = &instructions
		}
		clone.CollaborationMode = &mode
	}
	return clone
}

func nativeTurnTerminalStatus(payload json.RawMessage) (string, error) {
	var notification struct {
		Turn struct {
			Status string `json:"status"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(payload, &notification); err != nil {
		return "", fmt.Errorf("workspace chat: decode turn/completed payload: %w", err)
	}
	switch notification.Turn.Status {
	case "completed", "failed", "interrupted":
		return notification.Turn.Status, nil
	case "inProgress":
		return "", fmt.Errorf("workspace chat: turn/completed carried non-terminal status inProgress")
	case "":
		return "", fmt.Errorf("workspace chat: turn/completed payload is missing turn.status")
	default:
		return "", fmt.Errorf("workspace chat: turn/completed carried unknown turn.status %q", notification.Turn.Status)
	}
}

func nativeAssistantText(payload json.RawMessage) string {
	var value any
	if json.Unmarshal(payload, &value) != nil {
		return ""
	}
	return findAssistantText(value)
}

func findAssistantText(value any) string {
	switch current := value.(type) {
	case map[string]any:
		kind := strings.ToLower(asString(current["type"]))
		role := strings.ToLower(asString(current["role"]))
		if role == "assistant" || strings.Contains(kind, "assistantmessage") || kind == "agent_message" {
			for _, key := range []string{"text", "content", "message"} {
				if text := flattenText(current[key]); text != "" {
					return text
				}
			}
		}
		for _, key := range []string{"item", "message", "result"} {
			if text := findAssistantText(current[key]); text != "" {
				return text
			}
		}
	case []any:
		for _, item := range current {
			if text := findAssistantText(item); text != "" {
				return text
			}
		}
	}
	return ""
}

func flattenText(value any) string {
	switch current := value.(type) {
	case string:
		return strings.TrimSpace(current)
	case []any:
		parts := make([]string, 0, len(current))
		for _, item := range current {
			if text := flattenText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		for _, key := range []string{"text", "content"} {
			if text := flattenText(current[key]); text != "" {
				return text
			}
		}
	}
	return ""
}

func findJSONString(value any, key string) string {
	switch current := value.(type) {
	case map[string]any:
		if text, ok := current[key].(string); ok {
			return text
		}
		for _, child := range current {
			if text := findJSONString(child, key); text != "" {
				return text
			}
		}
	case []any:
		for _, child := range current {
			if text := findJSONString(child, key); text != "" {
				return text
			}
		}
	}
	return ""
}

func asString(value any) string {
	text, _ := value.(string)
	return text
}

func (s *WorkspaceChatService) validateNativeInputs(input []NativeUserInput, trusted bool) ([]NativeUserInput, error) {
	if len(input) == 0 {
		return nil, fmt.Errorf("workspace chat: at least one input item is required")
	}
	result := make([]NativeUserInput, 0, len(input))
	for _, item := range input {
		item.Type = strings.ToLower(strings.TrimSpace(item.Type))
		switch item.Type {
		case "text":
			item.Text = strings.TrimSpace(item.Text)
			if item.Text == "" {
				return nil, fmt.Errorf("workspace chat: text input is empty")
			}
			if item.URL != "" || item.LocalPath != "" {
				return nil, fmt.Errorf("workspace chat: text input cannot contain a path or URL")
			}
		case "image":
			if !trusted {
				return nil, fmt.Errorf("workspace chat: browser media requires a server-signed attachment reference")
			}
			if item.LocalPath == "" && item.URL == "" {
				return nil, fmt.Errorf("workspace chat: trusted media input has no source")
			}
		case "audio", "file":
			if !trusted || item.LocalPath == "" {
				return nil, fmt.Errorf("workspace chat: attachment input must come from a verified platform attachment")
			}
			result = append(result, NativeUserInput{Type: "text", Text: s.engine.i18n.Tf(MsgWorkspaceChatVerifiedAttachment, item.LocalPath)})
			continue
		default:
			return nil, fmt.Errorf("workspace chat: unsupported input type %q", item.Type)
		}
		result = append(result, item)
	}
	return result, nil
}

func mergeNativeSettingsPatch(base, override NativeThreadSettingsPatch) NativeThreadSettingsPatch {
	result := base
	if override.Model != nil {
		result.Model = override.Model
	}
	if override.Effort != nil {
		result.Effort = override.Effort
	}
	if override.PlanEffort != nil {
		result.PlanEffort = override.PlanEffort
	}
	if override.ServiceTier != nil {
		result.ServiceTier = override.ServiceTier
	}
	if override.Personality != nil {
		result.Personality = override.Personality
	}
	if override.Summary != nil {
		result.Summary = override.Summary
	}
	if override.PermissionProfile != nil {
		result.PermissionProfile = override.PermissionProfile
	}
	if override.Mode != nil {
		result.Mode = override.Mode
	}
	return result
}

func emptyNativeSettingsPatch(patch NativeThreadSettingsPatch) bool {
	return patch.Model == nil && patch.Effort == nil && patch.PlanEffort == nil && patch.ServiceTier == nil && patch.Personality == nil && patch.Summary == nil && patch.PermissionProfile == nil && patch.Mode == nil
}

func sameWorkspacePath(left, right string) bool {
	canonical := func(value string) string {
		value = filepath.Clean(strings.TrimSpace(value))
		if absolute, err := filepath.Abs(value); err == nil {
			value = absolute
		}
		if resolved, err := filepath.EvalSymlinks(value); err == nil {
			value = resolved
		}
		return filepath.Clean(value)
	}
	return canonical(left) == canonical(right)
}

func newWorkspaceChatID(prefix string) string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		panic(fmt.Sprintf("workspace chat: crypto/rand failed: %v", err))
	}
	return prefix + "_" + hex.EncodeToString(buffer)
}

func waitWorkspaceChat(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
