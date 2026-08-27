package codex

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const (
	nativeCapabilityModels             = "models"
	nativeCapabilityCollaborationMode  = "collaboration_mode"
	nativeCapabilityPermissionProfiles = "permission_profiles"
	nativeCapabilitySettings           = "settings"
	nativeCapabilityTurns              = "turns"
	nativeCapabilityRealtime           = "realtime"
	nativeSettingsWaitTimeout          = 10 * time.Second
	nativeEventSubscriptionQueueLimit  = 1024
)

type nativeThreadWire struct {
	ID        string           `json:"id"`
	Cwd       string           `json:"cwd"`
	Name      *string          `json:"name"`
	Preview   string           `json:"preview"`
	Status    json.RawMessage  `json:"status"`
	CreatedAt int64            `json:"createdAt"`
	UpdatedAt int64            `json:"updatedAt"`
	Turns     []nativeTurnWire `json:"turns"`
}

type nativeTurnWire struct {
	ID          string            `json:"id"`
	Status      string            `json:"status"`
	StartedAt   *int64            `json:"startedAt"`
	CompletedAt *int64            `json:"completedAt"`
	DurationMS  *int64            `json:"durationMs"`
	Error       json.RawMessage   `json:"error"`
	Items       []json.RawMessage `json:"items"`
}

type nativeCollaborationModeWire struct {
	Mode     string                          `json:"mode"`
	Settings nativeCollaborationSettingsWire `json:"settings"`
}

type nativeCollaborationSettingsWire struct {
	Model                 string  `json:"model"`
	ReasoningEffort       *string `json:"reasoning_effort"`
	DeveloperInstructions *string `json:"developer_instructions"`
}

type nativeThreadSettingsWire struct {
	Model                   string                       `json:"model"`
	ModelProvider           string                       `json:"modelProvider"`
	Effort                  *string                      `json:"effort"`
	ServiceTier             *string                      `json:"serviceTier"`
	Personality             *string                      `json:"personality"`
	Summary                 *string                      `json:"summary"`
	ActivePermissionProfile *nativeActivePermissionWire  `json:"activePermissionProfile"`
	ApprovalPolicy          json.RawMessage              `json:"approvalPolicy"`
	ApprovalsReviewer       string                       `json:"approvalsReviewer"`
	SandboxPolicy           json.RawMessage              `json:"sandboxPolicy"`
	CollaborationMode       *nativeCollaborationModeWire `json:"collaborationMode"`
	Cwd                     string                       `json:"cwd"`
}

type nativeActivePermissionWire struct {
	ID string `json:"id"`
}

type nativeThreadRuntimeResponse struct {
	Cwd                     string                      `json:"cwd"`
	Model                   string                      `json:"model"`
	ModelProvider           string                      `json:"modelProvider"`
	ReasoningEffort         *string                     `json:"reasoningEffort"`
	ServiceTier             *string                     `json:"serviceTier"`
	ApprovalPolicy          json.RawMessage             `json:"approvalPolicy"`
	ApprovalsReviewer       string                      `json:"approvalsReviewer"`
	Sandbox                 json.RawMessage             `json:"sandbox"`
	ActivePermissionProfile *nativeActivePermissionWire `json:"activePermissionProfile"`
	Thread                  nativeThreadWire            `json:"thread"`
}

type nativeConversationState struct {
	settingsMu          sync.Mutex
	Cwd                 string
	Settings            core.NativeThreadSettings
	HasSettings         bool
	Status              json.RawMessage
	Usage               json.RawMessage
	ActiveTurn          *core.NativeActiveTurn
	LastCompletedTurnID string
	PendingInteractions map[string]core.NativeInteraction
}

type nativePendingRequest struct {
	ThreadID   string
	Method     string
	RequestID  json.RawMessage
	Params     json.RawMessage
	Generation uint64
}

type nativeEventSubscription struct {
	mu       sync.Mutex
	cond     *sync.Cond
	queue    []core.NativeEventEnvelope
	out      chan core.NativeEventEnvelope
	done     chan struct{}
	finished chan struct{}
	closed   bool
}

func newNativeEventSubscription() *nativeEventSubscription {
	subscription := &nativeEventSubscription{
		out: make(chan core.NativeEventEnvelope), done: make(chan struct{}), finished: make(chan struct{}),
	}
	subscription.cond = sync.NewCond(&subscription.mu)
	go subscription.run()
	return subscription
}

func (s *nativeEventSubscription) run() {
	defer close(s.finished)
	defer close(s.out)
	for {
		s.mu.Lock()
		for len(s.queue) == 0 && !s.closed {
			s.cond.Wait()
		}
		if s.closed {
			s.queue = nil
			s.mu.Unlock()
			return
		}
		event := s.queue[0]
		s.queue[0] = core.NativeEventEnvelope{}
		s.queue = s.queue[1:]
		s.mu.Unlock()
		select {
		case s.out <- event:
		case <-s.done:
			return
		}
	}
}

func (s *nativeEventSubscription) enqueue(event core.NativeEventEnvelope) bool {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	if len(s.queue) >= nativeEventSubscriptionQueueLimit {
		s.closed = true
		s.queue = nil
		close(s.done)
		s.cond.Broadcast()
		s.mu.Unlock()
		<-s.finished
		return false
	}
	s.queue = append(s.queue, event)
	s.cond.Signal()
	s.mu.Unlock()
	return true
}

func (s *nativeEventSubscription) close() {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.queue = nil
		close(s.done)
		s.cond.Broadcast()
	}
	s.mu.Unlock()
	<-s.finished
}

func (s *appServerSession) nativeStateLocked(threadID string) *nativeConversationState {
	state := s.nativeStates[threadID]
	if state == nil {
		state = &nativeConversationState{PendingInteractions: make(map[string]core.NativeInteraction)}
		s.nativeStates[threadID] = state
	}
	return state
}

func (s *appServerSession) nativeState(threadID string) *nativeConversationState {
	if s.owner != nil {
		return s.owner.nativeState(threadID)
	}
	s.nativeMu.Lock()
	defer s.nativeMu.Unlock()
	return s.nativeStateLocked(strings.TrimSpace(threadID))
}

func (s *appServerSession) claimThreadOwner(threadID, owner string) error {
	if s.owner != nil {
		return s.owner.claimThreadOwner(threadID, owner)
	}
	threadID, owner = strings.TrimSpace(threadID), strings.TrimSpace(owner)
	if threadID == "" || owner == "" {
		return fmt.Errorf("codex: invalid thread lifecycle owner")
	}
	s.threadOwnersMu.Lock()
	defer s.threadOwnersMu.Unlock()
	if existing := s.threadOwners[threadID]; existing != "" && existing != owner {
		return fmt.Errorf("codex: thread %s is already owned by %s lifecycle", threadID, existing)
	}
	s.threadOwners[threadID] = owner
	return nil
}

func (s *appServerSession) releaseThreadOwner(threadID, owner string) {
	if s.owner != nil {
		s.owner.releaseThreadOwner(threadID, owner)
		return
	}
	s.threadOwnersMu.Lock()
	if s.threadOwners[threadID] == owner {
		delete(s.threadOwners, threadID)
	}
	s.threadOwnersMu.Unlock()
}

func (s *appServerSession) registerNativeThread(threadID, cwd string) error {
	if s.owner != nil {
		return s.owner.registerNativeThread(threadID, cwd)
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return fmt.Errorf("codex: native thread id is empty")
	}
	if err := s.claimThreadOwner(threadID, "native"); err != nil {
		return err
	}
	s.nativeMu.Lock()
	if existing := s.nativeThreads[threadID]; existing != "" && strings.TrimSpace(existing) != strings.TrimSpace(cwd) {
		s.nativeMu.Unlock()
		return fmt.Errorf("codex: native thread cwd changed unexpectedly")
	}
	s.nativeThreads[threadID] = strings.TrimSpace(cwd)
	state := s.nativeStateLocked(threadID)
	if strings.TrimSpace(cwd) != "" {
		state.Cwd = strings.TrimSpace(cwd)
	}
	s.nativeMu.Unlock()
	return nil
}

func (s *appServerSession) rollbackNativeThreadRegistration(threadID, cwd string) {
	if s.owner != nil {
		s.owner.rollbackNativeThreadRegistration(threadID, cwd)
		return
	}
	threadID, cwd = strings.TrimSpace(threadID), strings.TrimSpace(cwd)
	removed := false
	s.nativeMu.Lock()
	if s.nativeThreads[threadID] == cwd {
		delete(s.nativeThreads, threadID)
		state := s.nativeStateLocked(threadID)
		state.Cwd = ""
		state.Settings = core.NativeThreadSettings{}
		state.HasSettings = false
		state.Status = nil
		state.Usage = nil
		state.ActiveTurn = nil
		state.LastCompletedTurnID = ""
		state.PendingInteractions = make(map[string]core.NativeInteraction)
		removed = true
	}
	s.nativeMu.Unlock()
	if removed {
		s.releaseThreadOwner(threadID, "native")
	}
}

func (s *appServerSession) hasNativeThread(threadID, cwd string) bool {
	if s.owner != nil {
		return s.owner.hasNativeThread(threadID, cwd)
	}
	s.nativeMu.Lock()
	defer s.nativeMu.Unlock()
	registeredCwd, registered := s.nativeThreads[strings.TrimSpace(threadID)]
	return registered && registeredCwd == strings.TrimSpace(cwd)
}

func (s *appServerSession) subscribeNative(threadID string) (<-chan core.NativeEventEnvelope, func(), error) {
	if s.owner != nil {
		return s.owner.subscribeNative(threadID)
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return nil, nil, fmt.Errorf("codex: native subscription requires thread id")
	}
	if !s.Alive() {
		return nil, nil, fmt.Errorf("codex: app-server connection is closed")
	}
	subscription := newNativeEventSubscription()
	s.nativeMu.Lock()
	s.nativeNextID++
	id := s.nativeNextID
	if s.nativeSubscriptions[threadID] == nil {
		s.nativeSubscriptions[threadID] = make(map[uint64]*nativeEventSubscription)
	}
	s.nativeSubscriptions[threadID][id] = subscription
	s.nativeMu.Unlock()
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			s.nativeMu.Lock()
			delete(s.nativeSubscriptions[threadID], id)
			if len(s.nativeSubscriptions[threadID]) == 0 {
				delete(s.nativeSubscriptions, threadID)
			}
			s.nativeMu.Unlock()
			subscription.close()
		})
	}
	return subscription.out, cancel, nil
}

func (s *appServerSession) publishNative(event core.NativeEventEnvelope) {
	if s.owner != nil {
		s.owner.publishNative(event)
		return
	}
	s.nativeMu.Lock()
	subscribers := make([]*nativeEventSubscription, 0, len(s.nativeSubscriptions[event.ThreadID]))
	for _, subscriber := range s.nativeSubscriptions[event.ThreadID] {
		subscribers = append(subscribers, subscriber)
	}
	s.nativeMu.Unlock()
	for _, subscriber := range subscribers {
		subscriber.enqueue(event)
	}
}

func (s *appServerSession) closeNativeState(_ error) {
	if s.owner != nil {
		return
	}
	s.nativeMu.Lock()
	var subscriptions []*nativeEventSubscription
	for _, byID := range s.nativeSubscriptions {
		for _, subscription := range byID {
			subscriptions = append(subscriptions, subscription)
		}
	}
	s.nativeSubscriptions = make(map[string]map[uint64]*nativeEventSubscription)
	s.nativeRequests = make(map[string]nativePendingRequest)
	for _, waiters := range s.nativeSettingsWaiters {
		for id, waiter := range waiters {
			delete(waiters, id)
			close(waiter)
		}
	}
	s.nativeSettingsWaiters = make(map[string]map[uint64]chan core.NativeThreadSettings)
	s.nativeMu.Unlock()
	for _, subscription := range subscriptions {
		subscription.close()
	}
}

func nativeEnvelopeIDs(paramsRaw json.RawMessage) (threadID, turnID, itemID string) {
	var params struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		ItemID   string `json:"itemId"`
		Turn     struct {
			ID string `json:"id"`
		} `json:"turn"`
		Item struct {
			ID string `json:"id"`
		} `json:"item"`
	}
	if json.Unmarshal(paramsRaw, &params) != nil {
		return "", "", ""
	}
	turnID = strings.TrimSpace(params.TurnID)
	if turnID == "" {
		turnID = strings.TrimSpace(params.Turn.ID)
	}
	itemID = strings.TrimSpace(params.ItemID)
	if itemID == "" {
		itemID = strings.TrimSpace(params.Item.ID)
	}
	return strings.TrimSpace(params.ThreadID), turnID, itemID
}

func (s *appServerSession) handleNativeNotification(method string, paramsRaw json.RawMessage) {
	threadID, turnID, itemID := nativeEnvelopeIDs(paramsRaw)
	if threadID == "" {
		return
	}
	s.nativeMu.Lock()
	_, registered := s.nativeThreads[threadID]
	if !registered {
		s.nativeMu.Unlock()
		return
	}
	state := s.nativeStateLocked(threadID)
	payload := cloneRawMessage(paramsRaw)
	var materializedSettings *core.NativeThreadSettings
	var requestID json.RawMessage
	interactionID := ""
	switch method {
	case "thread/settings/updated":
		var notification struct {
			ThreadSettings nativeThreadSettingsWire `json:"threadSettings"`
		}
		if json.Unmarshal(paramsRaw, &notification) == nil {
			settings := mapNativeSettings(notification.ThreadSettings)
			state.Settings, state.HasSettings = settings, true
			cloned := cloneNativeSettings(settings)
			materializedSettings = &cloned
			for _, waiter := range s.nativeSettingsWaiters[threadID] {
				select {
				case waiter <- cloneNativeSettings(settings):
				default:
				}
			}
		}
	case "thread/status/changed":
		var notification struct {
			Status json.RawMessage `json:"status"`
		}
		if json.Unmarshal(paramsRaw, &notification) == nil {
			state.Status = cloneRawMessage(notification.Status)
		}
	case "thread/tokenUsage/updated":
		state.Usage = cloneRawMessage(paramsRaw)
	case "turn/started":
		if turnID != "" && state.LastCompletedTurnID != turnID {
			state.ActiveTurn = &core.NativeActiveTurn{ID: turnID, StartedAt: time.Now().UTC()}
		}
	case "turn/completed":
		state.LastCompletedTurnID = turnID
		if state.ActiveTurn != nil && (turnID == "" || state.ActiveTurn.ID == turnID) {
			state.ActiveTurn = nil
		}
	case "serverRequest/resolved":
		var notification struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		if json.Unmarshal(paramsRaw, &notification) == nil && len(notification.RequestID) > 0 {
			requestID = cloneRawMessage(notification.RequestID)
			interactionID = nativeRequestKey(s.connectionGeneration, threadID, requestID)
			delete(s.nativeRequests, interactionID)
			delete(state.PendingInteractions, interactionID)
		}
	}
	s.nativeMu.Unlock()
	s.publishNative(core.NativeEventEnvelope{
		Method: method, ThreadID: threadID, TurnID: turnID, ItemID: itemID,
		RequestID: requestID, InteractionID: interactionID, ConnectionGeneration: s.connectionGeneration,
		Settings: materializedSettings, Payload: payload, OccurredAt: time.Now().UTC(),
	})
}

func nativeInteractiveRequest(method string) bool {
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval",
		"item/permissions/requestApproval", "item/tool/requestUserInput", "mcpServer/elicitation/request":
		return true
	default:
		return false
	}
}

func nativeRequestKey(generation uint64, threadID string, requestID json.RawMessage) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s", generation, threadID, string(requestID))))
	return "ni_" + hex.EncodeToString(sum[:16])
}

func nativeAvailableDecisions(paramsRaw json.RawMessage) ([]json.RawMessage, bool) {
	var params map[string]json.RawMessage
	if json.Unmarshal(paramsRaw, &params) != nil {
		return nil, false
	}
	raw, exists := params["availableDecisions"]
	if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false
	}
	var available []json.RawMessage
	if json.Unmarshal(raw, &available) != nil {
		return nil, true
	}
	return available, true
}

func nativeAllowedDecisions(method string, paramsRaw json.RawMessage) []json.RawMessage {
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "mcpServer/elicitation/request":
		available, explicit := nativeAvailableDecisions(paramsRaw)
		if explicit {
			return cloneRawMessages(available)
		}
		return nil
	default:
		return nil
	}
}

func cloneRawMessages(values []json.RawMessage) []json.RawMessage {
	cloned := make([]json.RawMessage, len(values))
	for index, value := range values {
		cloned[index] = cloneRawMessage(value)
	}
	return cloned
}

func (s *appServerSession) handleNativeServerRequest(probe map[string]json.RawMessage) bool {
	if s.owner != nil {
		return false
	}
	var method string
	if json.Unmarshal(probe["method"], &method) != nil || !nativeInteractiveRequest(method) {
		return false
	}
	paramsRaw := probe["params"]
	threadID, turnID, itemID := nativeEnvelopeIDs(paramsRaw)
	if threadID == "" {
		return false
	}
	s.nativeMu.Lock()
	_, registered := s.nativeThreads[threadID]
	if !registered {
		s.nativeMu.Unlock()
		return false
	}
	rawID := cloneRawMessage(probe["id"])
	key := nativeRequestKey(s.connectionGeneration, threadID, rawID)
	request := nativePendingRequest{
		ThreadID: threadID, Method: method, RequestID: rawID, Params: cloneRawMessage(paramsRaw),
		Generation: s.connectionGeneration,
	}
	s.nativeRequests[key] = request
	interaction := core.NativeInteraction{
		ID: key, Kind: method, ThreadID: threadID, TurnID: turnID, ItemID: itemID,
		RequestID: rawID, ConnectionGeneration: s.connectionGeneration,
		AllowedDecisions: nativeAllowedDecisions(method, paramsRaw),
		Payload:          cloneRawMessage(paramsRaw), OccurredAt: time.Now().UTC(),
	}
	s.nativeStateLocked(threadID).PendingInteractions[key] = interaction
	s.nativeMu.Unlock()
	s.publishNative(core.NativeEventEnvelope{
		Method: method, ThreadID: threadID, TurnID: turnID, ItemID: itemID,
		InteractionID: key, RequestID: rawID, ConnectionGeneration: s.connectionGeneration,
		AllowedDecisions: cloneRawMessages(interaction.AllowedDecisions),
		Payload:          cloneRawMessage(paramsRaw), OccurredAt: interaction.OccurredAt,
	})
	return true
}

func cloneRawMessage(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func (s *appServerSession) requestContext(ctx context.Context, method string, params, out any) error {
	_, err := s.requestContextState(ctx, method, params, out)
	return err
}

func (s *appServerSession) requestContextState(ctx context.Context, method string, params, out any) (bool, error) {
	if s.owner != nil {
		return s.owner.requestContextState(ctx, method, params, out)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	id := s.nextID.Add(1)
	responses := make(chan rpcResponseEnvelope, 1)
	s.pendingMu.Lock()
	if s.pending == nil {
		s.pending = make(map[int64]chan rpcResponseEnvelope)
	}
	s.pending[id] = responses
	s.pendingMu.Unlock()
	removePending := func() {
		s.pendingMu.Lock()
		delete(s.pending, id)
		s.pendingMu.Unlock()
	}
	payload := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	possiblyDispatched, err := s.writeJSONWithTimeoutContextState(ctx, method, payload, appServerRequestTimeout)
	if err != nil {
		removePending()
		return possiblyDispatched, err
	}
	timer := time.NewTimer(appServerRequestTimeout)
	defer timer.Stop()
	select {
	case response := <-responses:
		if response.TransportError != nil {
			return true, response.TransportError
		}
		if response.Error != nil {
			return true, response.Error
		}
		if out != nil {
			if err := json.Unmarshal(response.Result, out); err != nil {
				return true, fmt.Errorf("decode %s response: %w", method, err)
			}
		}
		return true, nil
	case <-ctx.Done():
		removePending()
		return true, ctx.Err()
	case <-s.contextDone():
		removePending()
		return true, s.contextErr()
	case <-timer.C:
		removePending()
		return true, fmt.Errorf("%s timed out", method)
	}
}

func nativeAcceptanceUnknown(operation string, err error) error {
	if err == nil || core.IsNativeAcceptanceUnknown(err) {
		return err
	}
	return &core.NativeAcceptanceUnknownError{Operation: operation, Cause: err}
}

func (s *appServerSession) mutationRequestContext(ctx context.Context, method string, params, out any) error {
	possiblyDispatched, err := s.requestContextState(ctx, method, params, out)
	if err == nil || !possiblyDispatched {
		return err
	}
	var rejected *rpcError
	if errors.As(err, &rejected) {
		return err
	}
	return nativeAcceptanceUnknown(method, err)
}

func (s *appServerSession) mutationWriteJSONWithTimeout(ctx context.Context, operation string, payload any, timeout time.Duration) error {
	possiblyDispatched, err := s.writeJSONWithTimeoutContextState(ctx, operation, payload, timeout)
	if err != nil && possiblyDispatched {
		return nativeAcceptanceUnknown(operation, err)
	}
	return err
}

func canonicalNativePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func (a *Agent) validateNativeWorkspace(ctx context.Context, workspace core.Workspace) (core.Workspace, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return core.Workspace{}, "", err
	}
	ref := strings.TrimSpace(workspace.Ref)
	if ref == "" {
		return core.Workspace{}, "", fmt.Errorf("codex: workspace reference is empty")
	}
	resolved, err := a.ResolveWorkspace(ctx, ref)
	if err != nil {
		return core.Workspace{}, "", err
	}
	if !resolved.Available {
		return core.Workspace{}, "", fmt.Errorf("codex: workspace %s is unavailable: %s", ref, resolved.Error)
	}
	canonical, err := canonicalNativePath(resolved.RootPath)
	if err != nil {
		return core.Workspace{}, "", fmt.Errorf("codex: resolve workspace cwd: %w", err)
	}
	provided, err := canonicalNativePath(workspace.RootPath)
	if err != nil || provided != canonical || workspace.ProjectID != resolved.ProjectID || workspace.RootIndex != resolved.RootIndex {
		return core.Workspace{}, "", fmt.Errorf("codex: workspace payload does not match reference")
	}
	return resolved, canonical, nil
}

func (a *Agent) nativeControl(ctx context.Context, workspace core.Workspace) (*appServerSession, core.Workspace, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	resolved, cwd, err := a.validateNativeWorkspace(ctx, workspace)
	if err != nil {
		return nil, core.Workspace{}, "", err
	}
	control, err := a.appServerControl(ctx)
	if err != nil {
		return nil, core.Workspace{}, "", err
	}
	return control, resolved, cwd, nil
}

func requireNativeGeneration(control *appServerSession, generation uint64) error {
	if generation == 0 || generation != control.connectionGeneration {
		return fmt.Errorf("codex: App Server connection generation is stale: %w", core.ErrNativeConnectionStale)
	}
	return nil
}

func nativeThread(thread nativeThreadWire) core.NativeThread {
	name := ""
	if thread.Name != nil {
		name = strings.TrimSpace(*thread.Name)
	}
	return core.NativeThread{
		ID: strings.TrimSpace(thread.ID), Cwd: strings.TrimSpace(thread.Cwd), Name: name,
		Preview: thread.Preview, Status: cloneRawMessage(thread.Status),
		CreatedAt: time.Unix(thread.CreatedAt, 0).UTC(), UpdatedAt: time.Unix(thread.UpdatedAt, 0).UTC(),
	}
}

func nativeTurn(turn nativeTurnWire) core.NativeTurn {
	mapped := core.NativeTurn{
		ID: turn.ID, Status: turn.Status, DurationMS: turn.DurationMS,
		Error: cloneRawMessage(turn.Error), Items: append([]json.RawMessage(nil), turn.Items...),
	}
	if turn.StartedAt != nil {
		value := time.Unix(*turn.StartedAt, 0).UTC()
		mapped.StartedAt = &value
	}
	if turn.CompletedAt != nil {
		value := time.Unix(*turn.CompletedAt, 0).UTC()
		mapped.CompletedAt = &value
	}
	return mapped
}

func mapNativeSettings(wire nativeThreadSettingsWire) core.NativeThreadSettings {
	settings := core.NativeThreadSettings{
		Model: wire.Model, ModelProvider: wire.ModelProvider,
		Effort: stringValue(wire.Effort), ServiceTier: stringValue(wire.ServiceTier),
		Personality: stringValue(wire.Personality), Summary: stringValue(wire.Summary),
		ApprovalPolicy: cloneRawMessage(wire.ApprovalPolicy), ApprovalsReviewer: wire.ApprovalsReviewer,
		SandboxPolicy: cloneRawMessage(wire.SandboxPolicy),
	}
	if wire.ActivePermissionProfile != nil {
		settings.PermissionProfile = wire.ActivePermissionProfile.ID
	}
	if wire.CollaborationMode != nil {
		settings.CollaborationMode = &core.NativeCollaborationMode{
			Mode: wire.CollaborationMode.Mode,
			Settings: core.NativeCollaborationSettings{
				Model:                 wire.CollaborationMode.Settings.Model,
				ReasoningEffort:       wire.CollaborationMode.Settings.ReasoningEffort,
				DeveloperInstructions: wire.CollaborationMode.Settings.DeveloperInstructions,
			},
		}
	}
	hashInput, _ := json.Marshal(wire)
	hash := sha256.Sum256(hashInput)
	settings.Revision = hex.EncodeToString(hash[:16])
	return settings
}

func settingsFromRuntime(response nativeThreadRuntimeResponse) core.NativeThreadSettings {
	wire := nativeThreadSettingsWire{
		Model: response.Model, ModelProvider: response.ModelProvider, Effort: response.ReasoningEffort,
		ServiceTier: response.ServiceTier, ActivePermissionProfile: response.ActivePermissionProfile,
		ApprovalPolicy: response.ApprovalPolicy, ApprovalsReviewer: response.ApprovalsReviewer,
		SandboxPolicy: response.Sandbox, Cwd: response.Cwd,
	}
	return mapNativeSettings(wire)
}

func isUnavailableCapability(err error) (bool, string) {
	if err == nil {
		return false, ""
	}
	var rpcErr *rpcError
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	if errors.As(err, &rpcErr) {
		message = strings.ToLower(strings.TrimSpace(rpcErr.Message))
		if rpcErr.Code == -32601 {
			return true, rpcErr.Error()
		}
	}
	for _, marker := range []string{"method not found", "unknown method", "unsupported method", "not supported", "experimental api is disabled", "experimental api disabled"} {
		if strings.Contains(message, marker) {
			return true, err.Error()
		}
	}
	return false, ""
}

func nativeCapability(supported bool, reason string) core.CapabilityStatus {
	return core.CapabilityStatus{Supported: supported, Reason: strings.TrimSpace(reason)}
}

func cloneNativeInteraction(interaction core.NativeInteraction) core.NativeInteraction {
	interaction.RequestID = cloneRawMessage(interaction.RequestID)
	interaction.Payload = cloneRawMessage(interaction.Payload)
	interaction.AllowedDecisions = cloneRawMessages(interaction.AllowedDecisions)
	return interaction
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneNativeSettings(settings core.NativeThreadSettings) core.NativeThreadSettings {
	settings.ApprovalPolicy = cloneRawMessage(settings.ApprovalPolicy)
	settings.SandboxPolicy = cloneRawMessage(settings.SandboxPolicy)
	if settings.CollaborationMode != nil {
		mode := *settings.CollaborationMode
		mode.Settings.ReasoningEffort = cloneStringPointer(mode.Settings.ReasoningEffort)
		mode.Settings.DeveloperInstructions = cloneStringPointer(mode.Settings.DeveloperInstructions)
		settings.CollaborationMode = &mode
	}
	return settings
}

func (s *appServerSession) nativeSnapshotState(threadID string) (core.NativeThreadSettings, bool, json.RawMessage, json.RawMessage, *core.NativeActiveTurn, []core.NativeInteraction) {
	s.nativeMu.Lock()
	defer s.nativeMu.Unlock()
	state := s.nativeStateLocked(threadID)
	settings := cloneNativeSettings(state.Settings)
	var active *core.NativeActiveTurn
	if state.ActiveTurn != nil {
		value := *state.ActiveTurn
		active = &value
	}
	interactions := make([]core.NativeInteraction, 0, len(state.PendingInteractions))
	for _, interaction := range state.PendingInteractions {
		interactions = append(interactions, cloneNativeInteraction(interaction))
	}
	sort.Slice(interactions, func(i, j int) bool {
		if interactions[i].OccurredAt.Equal(interactions[j].OccurredAt) {
			return interactions[i].ID < interactions[j].ID
		}
		return interactions[i].OccurredAt.Before(interactions[j].OccurredAt)
	})
	return settings, state.HasSettings, cloneRawMessage(state.Status), cloneRawMessage(state.Usage), active, interactions
}

func validNativeSortDirection(direction, defaultValue string) (string, error) {
	direction = strings.ToLower(strings.TrimSpace(direction))
	if direction == "" {
		return defaultValue, nil
	}
	if direction != "asc" && direction != "desc" {
		return "", fmt.Errorf("codex: invalid sort direction %q", direction)
	}
	return direction, nil
}

func nativePageLimit(limit int) (int, error) {
	if limit < 0 || limit > 1000 {
		return 0, fmt.Errorf("codex: page limit must be between 0 and 1000")
	}
	return limit, nil
}

func nativeDeepLink(threadID string) string {
	return "codex://threads/" + strings.TrimSpace(threadID)
}

func rawMessagesEqual(a, b json.RawMessage) bool {
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}

func (a *Agent) ListNativeConversations(ctx context.Context, workspace core.Workspace, page core.NativePageRequest) (core.NativeThreadPage, error) {
	control, _, cwd, err := a.nativeControl(ctx, workspace)
	if err != nil {
		return core.NativeThreadPage{}, err
	}
	direction, err := validNativeSortDirection(page.SortDirection, "desc")
	if err != nil {
		return core.NativeThreadPage{}, err
	}
	limit, err := nativePageLimit(page.Limit)
	if err != nil {
		return core.NativeThreadPage{}, err
	}
	params := map[string]any{"cwd": cwd, "sortKey": "updated_at", "sortDirection": direction}
	if cursor := strings.TrimSpace(page.Cursor); cursor != "" {
		params["cursor"] = cursor
	}
	if limit > 0 {
		params["limit"] = limit
	}
	var response struct {
		Data            []nativeThreadWire `json:"data"`
		NextCursor      *string            `json:"nextCursor"`
		BackwardsCursor *string            `json:"backwardsCursor"`
	}
	if err := control.requestContext(ctx, "thread/list", params, &response); err != nil {
		return core.NativeThreadPage{}, fmt.Errorf("codex: thread/list: %w", err)
	}
	result := core.NativeThreadPage{Data: make([]core.NativeThread, 0, len(response.Data))}
	for _, wire := range response.Data {
		threadCwd, pathErr := canonicalNativePath(wire.Cwd)
		if pathErr != nil || threadCwd != cwd {
			return core.NativeThreadPage{}, fmt.Errorf("codex: thread/list returned thread %q outside workspace", wire.ID)
		}
		thread := nativeThread(wire)
		result.Data = append(result.Data, thread)
	}
	if response.NextCursor != nil {
		result.NextCursor = *response.NextCursor
	}
	if response.BackwardsCursor != nil {
		result.BackwardsCursor = *response.BackwardsCursor
	}
	return result, nil
}

func (a *Agent) readNativeThreadWithTurns(ctx context.Context, control *appServerSession, cwd, threadID string, includeTurns bool) (nativeThreadWire, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return nativeThreadWire{}, fmt.Errorf("codex: thread id is empty")
	}
	var response struct {
		Thread nativeThreadWire `json:"thread"`
	}
	if err := control.requestContext(ctx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": includeTurns}, &response); err != nil {
		var rpcErr *rpcError
		if errors.As(err, &rpcErr) && rpcErr.Code == -32004 {
			err = fmt.Errorf("%w: %v", core.ErrNativeThreadNotFound, err)
		}
		return nativeThreadWire{}, fmt.Errorf("codex: thread/read: %w", err)
	}
	actual, err := canonicalNativePath(response.Thread.Cwd)
	if err != nil || actual != cwd || strings.TrimSpace(response.Thread.ID) != threadID {
		return nativeThreadWire{}, fmt.Errorf("%w: codex thread does not belong to workspace", core.ErrNativeThreadNotFound)
	}
	return response.Thread, nil
}

func (a *Agent) readNativeThread(ctx context.Context, control *appServerSession, cwd, threadID string) (nativeThreadWire, error) {
	return a.readNativeThreadWithTurns(ctx, control, cwd, threadID, false)
}

func (a *Agent) resumeNativeThread(ctx context.Context, control *appServerSession, cwd, threadID string) (nativeThreadRuntimeResponse, error) {
	if _, err := a.readNativeThread(ctx, control, cwd, threadID); err != nil {
		return nativeThreadRuntimeResponse{}, err
	}
	if err := control.registerNativeThread(threadID, cwd); err != nil {
		return nativeThreadRuntimeResponse{}, err
	}
	committed := false
	defer func() {
		if !committed {
			control.rollbackNativeThreadRegistration(threadID, cwd)
		}
	}()
	params := map[string]any{"threadId": threadID, "excludeTurns": true}
	var response nativeThreadRuntimeResponse
	if err := control.requestContext(ctx, "thread/resume", params, &response); err != nil {
		return nativeThreadRuntimeResponse{}, fmt.Errorf("codex: thread/resume: %w", err)
	}
	actual, err := canonicalNativePath(response.Cwd)
	if err != nil || actual != cwd || response.Thread.ID != threadID {
		return nativeThreadRuntimeResponse{}, fmt.Errorf("%w: codex thread/resume returned unexpected workspace thread", core.ErrNativeThreadNotFound)
	}
	settings := settingsFromRuntime(response)
	control.nativeMu.Lock()
	state := control.nativeStateLocked(threadID)
	if !state.HasSettings {
		state.Settings = settings
		state.HasSettings = true
	}
	state.Status = cloneRawMessage(response.Thread.Status)
	control.nativeMu.Unlock()
	committed = true
	return response, nil
}

func (a *Agent) ensureNativeThread(ctx context.Context, control *appServerSession, cwd, threadID string) (bool, error) {
	state := control.nativeState(threadID)
	state.settingsMu.Lock()
	defer state.settingsMu.Unlock()
	if control.hasNativeThread(threadID, cwd) {
		return false, nil
	}
	if _, err := a.resumeNativeThread(ctx, control, cwd, threadID); err != nil {
		return false, err
	}
	return true, nil
}

func (a *Agent) refreshNativeActiveTurn(ctx context.Context, control *appServerSession, cwd, threadID string) error {
	thread, err := a.readNativeThreadWithTurns(ctx, control, cwd, threadID, true)
	if err != nil {
		return fmt.Errorf("codex: refresh active Turn: %w", err)
	}
	control.nativeMu.Lock()
	state := control.nativeStateLocked(threadID)
	state.ActiveTurn = nil
	if len(thread.Turns) > 0 && thread.Turns[len(thread.Turns)-1].Status == "inProgress" {
		active := thread.Turns[len(thread.Turns)-1]
		startedAt := time.Now().UTC()
		if active.StartedAt != nil {
			startedAt = time.Unix(*active.StartedAt, 0).UTC()
		}
		state.ActiveTurn = &core.NativeActiveTurn{ID: active.ID, StartedAt: startedAt}
	}
	control.nativeMu.Unlock()
	return nil
}

func nativeSettingsMatchParams(settings core.NativeThreadSettings, params map[string]any) bool {
	matchString := func(key, actual string) bool {
		expected, exists := params[key]
		if !exists {
			return true
		}
		if expected == nil {
			return actual == ""
		}
		value, ok := expected.(string)
		return ok && actual == value
	}
	if !matchString("model", settings.Model) || !matchString("effort", settings.Effort) ||
		!matchString("serviceTier", settings.ServiceTier) || !matchString("personality", settings.Personality) ||
		!matchString("summary", settings.Summary) || !matchString("permissions", settings.PermissionProfile) {
		return false
	}
	expectedMode, hasMode := params["collaborationMode"]
	if !hasMode {
		return true
	}
	if expectedMode == nil {
		return settings.CollaborationMode == nil
	}
	expectedRaw, err := json.Marshal(expectedMode)
	if err != nil || settings.CollaborationMode == nil {
		return false
	}
	actualRaw, err := json.Marshal(settings.CollaborationMode)
	return err == nil && rawMessagesEqual(actualRaw, expectedRaw)
}

func (s *appServerSession) waitNativeSettings(ctx context.Context, threadID string, params map[string]any) (core.NativeThreadSettings, error) {
	if s.owner != nil {
		return s.owner.waitNativeSettings(ctx, threadID, params)
	}
	s.nativeMu.Lock()
	state := s.nativeStateLocked(threadID)
	s.nativeMu.Unlock()
	state.settingsMu.Lock()
	defer state.settingsMu.Unlock()
	s.nativeMu.Lock()
	if state.HasSettings && nativeSettingsMatchParams(state.Settings, params) {
		settings := cloneNativeSettings(state.Settings)
		s.nativeMu.Unlock()
		return settings, nil
	}
	s.nativeMu.Unlock()
	waiter := make(chan core.NativeThreadSettings, 1)
	s.nativeMu.Lock()
	s.nativeNextID++
	waiterID := s.nativeNextID
	if s.nativeSettingsWaiters[threadID] == nil {
		s.nativeSettingsWaiters[threadID] = make(map[uint64]chan core.NativeThreadSettings)
	}
	s.nativeSettingsWaiters[threadID][waiterID] = waiter
	s.nativeMu.Unlock()
	cleanup := func() {
		s.nativeMu.Lock()
		delete(s.nativeSettingsWaiters[threadID], waiterID)
		if len(s.nativeSettingsWaiters[threadID]) == 0 {
			delete(s.nativeSettingsWaiters, threadID)
		}
		s.nativeMu.Unlock()
	}
	defer cleanup()
	if err := s.mutationRequestContext(ctx, "thread/settings/update", params, nil); err != nil {
		return core.NativeThreadSettings{}, err
	}
	timer := time.NewTimer(nativeSettingsWaitTimeout)
	defer timer.Stop()
	for {
		select {
		case settings, ok := <-waiter:
			if !ok {
				return core.NativeThreadSettings{}, nativeAcceptanceUnknown("thread/settings/update", fmt.Errorf("codex: App Server closed before settings confirmation"))
			}
			if nativeSettingsMatchParams(settings, params) {
				return settings, nil
			}
		case <-ctx.Done():
			return core.NativeThreadSettings{}, nativeAcceptanceUnknown("thread/settings/update", ctx.Err())
		case <-timer.C:
			return core.NativeThreadSettings{}, nativeAcceptanceUnknown("thread/settings/update", fmt.Errorf("codex: thread/settings/updated confirmation timed out"))
		}
	}
}

func (a *Agent) ReadNativeConversation(ctx context.Context, workspace core.Workspace, threadID string) (core.NativeConversationSnapshot, error) {
	control, resolved, cwd, err := a.nativeControl(ctx, workspace)
	if err != nil {
		return core.NativeConversationSnapshot{}, err
	}
	threadWire, err := a.readNativeThread(ctx, control, cwd, threadID)
	if err != nil {
		return core.NativeConversationSnapshot{}, err
	}
	resumed, err := a.ensureNativeThread(ctx, control, cwd, threadID)
	if err != nil {
		return core.NativeConversationSnapshot{}, err
	}
	if resumed {
		if err := a.refreshNativeActiveTurn(ctx, control, cwd, threadID); err != nil {
			return core.NativeConversationSnapshot{}, err
		}
	}
	catalog, err := a.nativeRuntimeCatalog(ctx, control, resolved, cwd)
	if err != nil {
		return core.NativeConversationSnapshot{}, err
	}
	settings, settingsErr := control.waitNativeSettings(ctx, threadID, map[string]any{"threadId": threadID})
	if settingsErr != nil {
		if unavailable, reason := isUnavailableCapability(settingsErr); unavailable {
			catalog.Capabilities[nativeCapabilitySettings] = nativeCapability(false, reason)
		} else {
			return core.NativeConversationSnapshot{}, fmt.Errorf("codex: read canonical thread settings: %w", settingsErr)
		}
	} else {
		catalog.Capabilities[nativeCapabilitySettings] = nativeCapability(true, "")
	}
	stateSettings, hasSettings, status, usage, active, interactions := control.nativeSnapshotState(threadID)
	if settings.Revision != "" {
		stateSettings, hasSettings = settings, true
	}
	if !hasSettings {
		return core.NativeConversationSnapshot{}, fmt.Errorf("codex: canonical thread settings unavailable")
	}
	if len(status) == 0 {
		status = cloneRawMessage(threadWire.Status)
	}
	return core.NativeConversationSnapshot{
		Thread: nativeThread(threadWire), Settings: stateSettings, Status: status, Usage: usage,
		ActiveTurn: active, PendingInteractions: interactions, Capabilities: catalog.Capabilities,
		DeepLink: nativeDeepLink(threadID),
	}, nil
}

func (a *Agent) StartNativeConversation(ctx context.Context, workspace core.Workspace) (core.NativeConversationSnapshot, error) {
	control, resolved, cwd, err := a.nativeControl(ctx, workspace)
	if err != nil {
		return core.NativeConversationSnapshot{}, err
	}
	params := control.threadRequestParams()
	delete(params, "persistExtendedHistory")
	params["cwd"] = cwd
	var response nativeThreadRuntimeResponse
	if err := control.mutationRequestContext(ctx, "thread/start", params, &response); err != nil {
		return core.NativeConversationSnapshot{}, fmt.Errorf("codex: thread/start: %w", err)
	}
	threadID := strings.TrimSpace(response.Thread.ID)
	actual, pathErr := canonicalNativePath(response.Cwd)
	if pathErr != nil || actual != cwd || threadID == "" {
		return core.NativeConversationSnapshot{}, nativeAcceptanceUnknown("thread/start", fmt.Errorf("codex: thread/start returned unexpected workspace thread"))
	}
	if err := control.registerNativeThread(threadID, cwd); err != nil {
		return core.NativeConversationSnapshot{}, nativeAcceptanceUnknown("thread/start", err)
	}
	settings := settingsFromRuntime(response)
	control.nativeMu.Lock()
	state := control.nativeStateLocked(threadID)
	state.Settings, state.HasSettings, state.Status = settings, true, cloneRawMessage(response.Thread.Status)
	control.nativeMu.Unlock()
	catalog, err := a.nativeRuntimeCatalog(ctx, control, resolved, cwd)
	if err != nil {
		return core.NativeConversationSnapshot{}, nativeAcceptanceUnknown("thread/start", err)
	}
	canonical, settingsErr := control.waitNativeSettings(ctx, threadID, map[string]any{"threadId": threadID})
	if settingsErr != nil {
		if unavailable, reason := isUnavailableCapability(settingsErr); unavailable {
			catalog.Capabilities[nativeCapabilitySettings] = nativeCapability(false, reason)
		} else {
			return core.NativeConversationSnapshot{}, nativeAcceptanceUnknown("thread/start", fmt.Errorf("codex: read new thread settings: %w", settingsErr))
		}
	} else {
		settings = canonical
		catalog.Capabilities[nativeCapabilitySettings] = nativeCapability(true, "")
	}
	return core.NativeConversationSnapshot{
		Thread: nativeThread(response.Thread), Settings: settings, Status: cloneRawMessage(response.Thread.Status),
		PendingInteractions: []core.NativeInteraction{}, Capabilities: catalog.Capabilities,
		DeepLink: nativeDeepLink(threadID),
	}, nil
}

func (a *Agent) ListNativeTurns(ctx context.Context, workspace core.Workspace, threadID string, page core.NativePageRequest) (core.NativeTurnPage, error) {
	control, _, cwd, err := a.nativeControl(ctx, workspace)
	if err != nil {
		return core.NativeTurnPage{}, err
	}
	direction, err := validNativeSortDirection(page.SortDirection, "desc")
	if err != nil {
		return core.NativeTurnPage{}, err
	}
	limit, err := nativePageLimit(page.Limit)
	if err != nil {
		return core.NativeTurnPage{}, err
	}
	thread, err := a.readNativeThreadWithTurns(ctx, control, cwd, threadID, true)
	if err != nil {
		return core.NativeTurnPage{}, err
	}
	turns := append([]nativeTurnWire(nil), thread.Turns...)
	if direction == "desc" {
		slices.Reverse(turns)
	}
	start, end, next, err := nativeHistoryPage("turn", direction, page.Cursor, limit, len(turns))
	if err != nil {
		return core.NativeTurnPage{}, err
	}
	result := core.NativeTurnPage{Data: make([]core.NativeTurn, 0, end-start), NextCursor: next}
	for _, turn := range turns[start:end] {
		result.Data = append(result.Data, nativeTurn(turn))
	}
	return result, nil
}

func (a *Agent) ListNativeItems(ctx context.Context, workspace core.Workspace, threadID, turnID string, page core.NativePageRequest) (core.NativeItemPage, error) {
	control, _, cwd, err := a.nativeControl(ctx, workspace)
	if err != nil {
		return core.NativeItemPage{}, err
	}
	direction, err := validNativeSortDirection(page.SortDirection, "asc")
	if err != nil {
		return core.NativeItemPage{}, err
	}
	limit, err := nativePageLimit(page.Limit)
	if err != nil {
		return core.NativeItemPage{}, err
	}
	thread, err := a.readNativeThreadWithTurns(ctx, control, cwd, threadID, true)
	if err != nil {
		return core.NativeItemPage{}, err
	}
	turnID = strings.TrimSpace(turnID)
	items := make([]core.NativeItem, 0)
	foundTurn := turnID == ""
	for _, turn := range thread.Turns {
		if turnID != "" && turn.ID != turnID {
			continue
		}
		foundTurn = true
		for _, item := range turn.Items {
			items = append(items, core.NativeItem{TurnID: turn.ID, Item: cloneRawMessage(item)})
		}
	}
	if !foundTurn {
		return core.NativeItemPage{}, fmt.Errorf("codex: turn %q was not found in thread history", turnID)
	}
	if direction == "desc" {
		slices.Reverse(items)
	}
	start, end, next, err := nativeHistoryPage("item", direction, page.Cursor, limit, len(items))
	if err != nil {
		return core.NativeItemPage{}, err
	}
	return core.NativeItemPage{Data: items[start:end], NextCursor: next}, nil
}

func nativeHistoryPage(kind, direction, cursor string, limit, total int) (int, int, string, error) {
	start := 0
	if cursor = strings.TrimSpace(cursor); cursor != "" {
		prefix := kind + ":" + direction + ":"
		if !strings.HasPrefix(cursor, prefix) {
			return 0, 0, "", fmt.Errorf("codex: invalid %s history cursor", kind)
		}
		parsed, err := strconv.Atoi(strings.TrimPrefix(cursor, prefix))
		if err != nil || parsed < 0 || parsed > total {
			return 0, 0, "", fmt.Errorf("codex: invalid %s history cursor", kind)
		}
		start = parsed
	}
	end := total
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	next := ""
	if end < total {
		next = fmt.Sprintf("%s:%s:%d", kind, direction, end)
	}
	return start, end, next, nil
}

func (a *Agent) SubscribeNativeConversation(ctx context.Context, workspace core.Workspace, threadID string) (core.NativeConversationSubscription, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	control, _, cwd, err := a.nativeControl(ctx, workspace)
	if err != nil {
		return core.NativeConversationSubscription{}, err
	}
	if _, err := a.ensureNativeThread(ctx, control, cwd, threadID); err != nil {
		return core.NativeConversationSubscription{}, err
	}
	events, cancelNative, err := control.subscribeNative(threadID)
	if err != nil {
		return core.NativeConversationSubscription{}, err
	}
	watchDone := make(chan struct{})
	var cancelOnce sync.Once
	cancel := func() {
		cancelOnce.Do(func() {
			close(watchDone)
			cancelNative()
		})
	}
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-control.contextDone():
			cancel()
		case <-watchDone:
		}
	}()
	return core.NativeConversationSubscription{
		Generation: control.connectionGeneration,
		Events:     events,
		Cancel:     cancel,
	}, nil
}

type nativeReasoningEffortOptionWire struct {
	Effort      string `json:"reasoningEffort"`
	Description string `json:"description"`
}

type nativeServiceTierWire struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type nativeModelWire struct {
	ID                     string                            `json:"id"`
	Model                  string                            `json:"model"`
	DisplayName            string                            `json:"displayName"`
	Description            string                            `json:"description"`
	Hidden                 bool                              `json:"hidden"`
	Default                bool                              `json:"isDefault"`
	DefaultReasoningEffort string                            `json:"defaultReasoningEffort"`
	ReasoningEfforts       []nativeReasoningEffortOptionWire `json:"supportedReasoningEfforts"`
	InputModalities        []string                          `json:"inputModalities"`
	SupportsPersonality    bool                              `json:"supportsPersonality"`
	ServiceTiers           []nativeServiceTierWire           `json:"serviceTiers"`
	DefaultServiceTier     *string                           `json:"defaultServiceTier"`
}

type nativeModeMaskWire struct {
	Name            string          `json:"name"`
	Mode            *string         `json:"mode"`
	Model           *string         `json:"model"`
	ReasoningEffort json.RawMessage `json:"reasoning_effort"`
}

type nativePermissionWire struct {
	ID          string  `json:"id"`
	Description *string `json:"description"`
	Allowed     bool    `json:"allowed"`
}

func nativeModelOption(model nativeModelWire) core.NativeModelOption {
	option := core.NativeModelOption{
		ID: model.ID, Model: model.Model, DisplayName: model.DisplayName,
		Description: model.Description, Hidden: model.Hidden, Default: model.Default,
		DefaultReasoningEffort: model.DefaultReasoningEffort,
		InputModalities:        append([]string(nil), model.InputModalities...),
		SupportsPersonality:    model.SupportsPersonality,
	}
	for _, effort := range model.ReasoningEfforts {
		option.ReasoningEfforts = append(option.ReasoningEfforts, core.ReasoningEffortOption{
			Effort: effort.Effort, Description: effort.Description,
		})
	}
	for _, tier := range model.ServiceTiers {
		option.ServiceTiers = append(option.ServiceTiers, core.ServiceTierOption{
			ID: tier.ID, Name: tier.Name, Description: tier.Description,
		})
	}
	if model.DefaultServiceTier != nil {
		option.DefaultServiceTier = *model.DefaultServiceTier
	}
	return option
}

func (a *Agent) loadNativeModels(ctx context.Context, control *appServerSession) ([]nativeModelWire, core.CapabilityStatus, error) {
	var models []nativeModelWire
	cursor := ""
	seenCursors := map[string]struct{}{}
	for {
		params := map[string]any{"limit": 100, "includeHidden": true}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var response struct {
			Data       []nativeModelWire `json:"data"`
			NextCursor *string           `json:"nextCursor"`
		}
		if err := control.requestContext(ctx, "model/list", params, &response); err != nil {
			if unavailable, reason := isUnavailableCapability(err); unavailable {
				return nil, nativeCapability(false, reason), nil
			}
			return nil, core.CapabilityStatus{}, fmt.Errorf("codex: model/list: %w", err)
		}
		models = append(models, response.Data...)
		if response.NextCursor == nil || strings.TrimSpace(*response.NextCursor) == "" {
			break
		}
		cursor = strings.TrimSpace(*response.NextCursor)
		if _, duplicate := seenCursors[cursor]; duplicate {
			return nil, core.CapabilityStatus{}, fmt.Errorf("codex: model/list repeated pagination cursor")
		}
		seenCursors[cursor] = struct{}{}
	}
	return models, nativeCapability(true, ""), nil
}

func (a *Agent) loadNativeModes(ctx context.Context, control *appServerSession) ([]nativeModeMaskWire, core.CapabilityStatus, error) {
	var response struct {
		Data []nativeModeMaskWire `json:"data"`
	}
	if err := control.requestContext(ctx, "collaborationMode/list", map[string]any{}, &response); err != nil {
		if unavailable, reason := isUnavailableCapability(err); unavailable {
			return nil, nativeCapability(false, reason), nil
		}
		return nil, core.CapabilityStatus{}, fmt.Errorf("codex: collaborationMode/list: %w", err)
	}
	return response.Data, nativeCapability(true, ""), nil
}

func (a *Agent) loadNativePermissions(ctx context.Context, control *appServerSession, cwd string) ([]nativePermissionWire, core.CapabilityStatus, error) {
	var permissions []nativePermissionWire
	cursor := ""
	seenCursors := map[string]struct{}{}
	for {
		params := map[string]any{"cwd": cwd, "limit": 100}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var response struct {
			Data       []nativePermissionWire `json:"data"`
			NextCursor *string                `json:"nextCursor"`
		}
		if err := control.requestContext(ctx, "permissionProfile/list", params, &response); err != nil {
			if unavailable, reason := isUnavailableCapability(err); unavailable {
				return nil, nativeCapability(false, reason), nil
			}
			return nil, core.CapabilityStatus{}, fmt.Errorf("codex: permissionProfile/list: %w", err)
		}
		permissions = append(permissions, response.Data...)
		if response.NextCursor == nil || strings.TrimSpace(*response.NextCursor) == "" {
			break
		}
		cursor = strings.TrimSpace(*response.NextCursor)
		if _, duplicate := seenCursors[cursor]; duplicate {
			return nil, core.CapabilityStatus{}, fmt.Errorf("codex: permissionProfile/list repeated pagination cursor")
		}
		seenCursors[cursor] = struct{}{}
	}
	return permissions, nativeCapability(true, ""), nil
}

func (a *Agent) loadNativeVoices(ctx context.Context, control *appServerSession) (core.NativeRealtimeVoiceCatalog, core.CapabilityStatus, error) {
	var response struct {
		Voices struct {
			V1, V2               []string
			DefaultV1, DefaultV2 string
		} `json:"voices"`
	}
	if err := control.requestContext(ctx, "thread/realtime/listVoices", map[string]any{}, &response); err != nil {
		if unavailable, reason := isUnavailableCapability(err); unavailable {
			return core.NativeRealtimeVoiceCatalog{}, nativeCapability(false, reason), nil
		}
		return core.NativeRealtimeVoiceCatalog{}, core.CapabilityStatus{}, fmt.Errorf("codex: thread/realtime/listVoices: %w", err)
	}
	return core.NativeRealtimeVoiceCatalog{
		V1: append([]string(nil), response.Voices.V1...), V2: append([]string(nil), response.Voices.V2...),
		DefaultV1: response.Voices.DefaultV1, DefaultV2: response.Voices.DefaultV2,
	}, nativeCapability(true, ""), nil
}

func probeNativeMethod(ctx context.Context, control *appServerSession, method string, params map[string]any) core.CapabilityStatus {
	err := control.requestContext(ctx, method, params, nil)
	if err == nil {
		return nativeCapability(true, "")
	}
	if unavailable, reason := isUnavailableCapability(err); unavailable {
		return nativeCapability(false, reason)
	}
	// 参数或资源错误证明方法存在；实际操作仍会原样返回该错误。
	return nativeCapability(true, "")
}

func (a *Agent) nativeRuntimeCatalog(ctx context.Context, control *appServerSession, _ core.Workspace, cwd string) (core.NativeRuntimeCatalog, error) {
	models, modelCapability, err := a.loadNativeModels(ctx, control)
	if err != nil {
		return core.NativeRuntimeCatalog{}, err
	}
	modes, modeCapability, err := a.loadNativeModes(ctx, control)
	if err != nil {
		return core.NativeRuntimeCatalog{}, err
	}
	permissions, permissionCapability, err := a.loadNativePermissions(ctx, control, cwd)
	if err != nil {
		return core.NativeRuntimeCatalog{}, err
	}
	voices, realtimeCapability, err := a.loadNativeVoices(ctx, control)
	if err != nil {
		return core.NativeRuntimeCatalog{}, err
	}
	catalog := core.NativeRuntimeCatalog{
		Capabilities: map[string]core.CapabilityStatus{
			nativeCapabilityModels:             modelCapability,
			nativeCapabilityCollaborationMode:  modeCapability,
			nativeCapabilityPermissionProfiles: permissionCapability,
			nativeCapabilityRealtime:           realtimeCapability,
			nativeCapabilitySettings:           probeNativeMethod(ctx, control, "thread/settings/update", map[string]any{"threadId": "00000000-0000-7000-8000-000000000000"}),
			nativeCapabilityTurns:              probeNativeMethod(ctx, control, "thread/read", map[string]any{"threadId": "00000000-0000-7000-8000-000000000000", "includeTurns": true}),
		},
		Personalities: []string{"none", "friendly", "pragmatic"},
		Summaries:     []string{"auto", "concise", "detailed", "none"}, Voices: voices,
	}
	for _, model := range models {
		catalog.Models = append(catalog.Models, nativeModelOption(model))
	}
	for _, mode := range modes {
		if mode.Mode == nil || (*mode.Mode != "default" && *mode.Mode != "plan") {
			continue
		}
		option := core.NativeCollaborationModeOption{Name: mode.Name, Mode: mode.Mode, Model: mode.Model}
		if len(mode.ReasoningEffort) > 0 && string(mode.ReasoningEffort) != "null" {
			var effort string
			if json.Unmarshal(mode.ReasoningEffort, &effort) == nil {
				option.ReasoningEffort = &effort
			}
		}
		catalog.Modes = append(catalog.Modes, option)
	}
	for _, permission := range permissions {
		description := ""
		if permission.Description != nil {
			description = *permission.Description
		}
		catalog.Permissions = append(catalog.Permissions, core.NativePermissionProfile{ID: permission.ID, Description: description, Allowed: permission.Allowed})
	}
	return catalog, nil
}

func (a *Agent) NativeRuntimeCatalog(ctx context.Context, workspace core.Workspace) (core.NativeRuntimeCatalog, error) {
	control, resolved, cwd, err := a.nativeControl(ctx, workspace)
	if err != nil {
		return core.NativeRuntimeCatalog{}, err
	}
	return a.nativeRuntimeCatalog(ctx, control, resolved, cwd)
}

func findNativeModel(models []nativeModelWire, model string) (nativeModelWire, bool) {
	for _, candidate := range models {
		if candidate.Model == model || candidate.ID == model {
			return candidate, true
		}
	}
	return nativeModelWire{}, false
}

func nativeModelSupportsEffort(model nativeModelWire, effort string) bool {
	for _, option := range model.ReasoningEfforts {
		if option.Effort == effort {
			return true
		}
	}
	return false
}

func nativeModelSupportsTier(model nativeModelWire, tier string) bool {
	if tier == "" {
		return true
	}
	for _, option := range model.ServiceTiers {
		if option.ID == tier {
			return true
		}
	}
	return false
}

func findNativeMode(modes []nativeModeMaskWire, mode string) (nativeModeMaskWire, bool) {
	for _, candidate := range modes {
		if candidate.Mode != nil && *candidate.Mode == mode {
			return candidate, true
		}
	}
	return nativeModeMaskWire{}, false
}

func nativeModeMaskEffort(mask nativeModeMaskWire) (*string, bool, error) {
	if len(mask.ReasoningEffort) == 0 {
		return nil, false, nil
	}
	if bytes.Equal(bytes.TrimSpace(mask.ReasoningEffort), []byte("null")) {
		return nil, true, nil
	}
	var effort string
	if err := json.Unmarshal(mask.ReasoningEffort, &effort); err != nil || strings.TrimSpace(effort) == "" {
		return nil, true, fmt.Errorf("codex: collaboration mode %q has invalid reasoning effort mask", mask.Name)
	}
	effort = strings.TrimSpace(effort)
	return &effort, true, nil
}

func nativeModeSettings(mask nativeModeMaskWire, model string, effort *string) map[string]any {
	var reasoning any
	if effort != nil {
		reasoning = *effort
	}
	return map[string]any{
		"mode":     *mask.Mode,
		"settings": map[string]any{"model": model, "reasoning_effort": reasoning, "developer_instructions": nil},
	}
}

func (a *Agent) UpdateNativeConversationSettings(ctx context.Context, workspace core.Workspace, threadID string, generation uint64, patch core.NativeThreadSettingsPatch) (core.NativeThreadSettings, error) {
	control, _, cwd, err := a.nativeControl(ctx, workspace)
	if err != nil {
		return core.NativeThreadSettings{}, err
	}
	if err := requireNativeGeneration(control, generation); err != nil {
		return core.NativeThreadSettings{}, err
	}
	if _, err := a.readNativeThread(ctx, control, cwd, threadID); err != nil {
		return core.NativeThreadSettings{}, err
	}
	if err := control.registerNativeThread(threadID, cwd); err != nil {
		return core.NativeThreadSettings{}, err
	}
	current, hasCurrent, _, _, _, _ := control.nativeSnapshotState(threadID)
	if !hasCurrent || current.Revision == "" {
		current, err = control.waitNativeSettings(ctx, threadID, map[string]any{"threadId": threadID})
		if err != nil {
			return core.NativeThreadSettings{}, fmt.Errorf("codex: read settings before update: %w", err)
		}
	}
	models, modelCapability, err := a.loadNativeModels(ctx, control)
	if err != nil {
		return core.NativeThreadSettings{}, err
	}
	if !modelCapability.Supported {
		return core.NativeThreadSettings{}, fmt.Errorf("codex: model catalog unavailable: %s", modelCapability.Reason)
	}
	modes, modeCapability, err := a.loadNativeModes(ctx, control)
	if err != nil {
		return core.NativeThreadSettings{}, err
	}
	permissions, permissionCapability, err := a.loadNativePermissions(ctx, control, cwd)
	if err != nil {
		return core.NativeThreadSettings{}, err
	}

	currentMode, desiredMode := "", ""
	if current.CollaborationMode != nil {
		currentMode = current.CollaborationMode.Mode
		desiredMode = currentMode
	}
	if patch.Mode != nil {
		desiredMode = strings.TrimSpace(*patch.Mode)
		if desiredMode != "default" && desiredMode != "plan" {
			return core.NativeThreadSettings{}, fmt.Errorf("codex: only default and plan collaboration modes are supported")
		}
	}
	modeNeedsUpdate := desiredMode != "" && (patch.Mode != nil || patch.Model != nil || patch.Effort != nil || patch.PlanEffort != nil)
	var selectedMode nativeModeMaskWire
	if modeNeedsUpdate {
		if !modeCapability.Supported {
			return core.NativeThreadSettings{}, fmt.Errorf("codex: collaboration modes unavailable: %s", modeCapability.Reason)
		}
		var found bool
		selectedMode, found = findNativeMode(modes, desiredMode)
		if !found {
			return core.NativeThreadSettings{}, fmt.Errorf("codex: collaboration mode %q is not available", desiredMode)
		}
	}
	desiredModel := current.Model
	if patch.Model != nil {
		desiredModel = strings.TrimSpace(*patch.Model)
		if desiredModel == "" {
			return core.NativeThreadSettings{}, fmt.Errorf("codex: model cannot be empty")
		}
	} else if patch.Mode != nil && selectedMode.Model != nil {
		desiredModel = strings.TrimSpace(*selectedMode.Model)
	}
	selectedModel, ok := findNativeModel(models, desiredModel)
	if !ok {
		return core.NativeThreadSettings{}, fmt.Errorf("codex: model %q is not in the App Server catalog", desiredModel)
	}
	desiredModel = selectedModel.Model
	normalEffort := current.Effort
	var planEffort *string
	if currentMode == "plan" && current.CollaborationMode != nil {
		if current.CollaborationMode.Settings.ReasoningEffort != nil {
			value := *current.CollaborationMode.Settings.ReasoningEffort
			planEffort = &value
		}
	} else if normalEffort != "" {
		value := normalEffort
		planEffort = &value
	}
	if patch.Mode != nil && patch.PlanEffort == nil {
		maskedEffort, present, maskErr := nativeModeMaskEffort(selectedMode)
		if maskErr != nil {
			return core.NativeThreadSettings{}, maskErr
		}
		if present {
			if desiredMode == "plan" {
				planEffort = maskedEffort
			} else if maskedEffort != nil {
				normalEffort = *maskedEffort
			}
		}
	}
	if patch.Effort != nil {
		patchedEffort := strings.TrimSpace(*patch.Effort)
		if !nativeModelSupportsEffort(selectedModel, patchedEffort) {
			return core.NativeThreadSettings{}, fmt.Errorf("codex: effort %q is invalid for model %q", patchedEffort, desiredModel)
		}
		normalEffort = patchedEffort
	}
	if patch.PlanEffort != nil {
		patchedEffort := strings.TrimSpace(*patch.PlanEffort)
		if !nativeModelSupportsEffort(selectedModel, patchedEffort) {
			return core.NativeThreadSettings{}, fmt.Errorf("codex: plan effort %q is invalid for model %q", patchedEffort, desiredModel)
		}
		planEffort = &patchedEffort
	}
	if normalEffort == "" || !nativeModelSupportsEffort(selectedModel, normalEffort) {
		normalEffort = selectedModel.DefaultReasoningEffort
	}
	if planEffort != nil && !nativeModelSupportsEffort(selectedModel, *planEffort) {
		value := selectedModel.DefaultReasoningEffort
		planEffort = &value
	}
	if !nativeModelSupportsEffort(selectedModel, normalEffort) || (planEffort != nil && !nativeModelSupportsEffort(selectedModel, *planEffort)) {
		return core.NativeThreadSettings{}, fmt.Errorf("codex: model %q has no valid default effort", desiredModel)
	}
	desiredTier := current.ServiceTier
	if patch.ServiceTier != nil {
		desiredTier = strings.TrimSpace(*patch.ServiceTier)
	}
	if !nativeModelSupportsTier(selectedModel, desiredTier) {
		return core.NativeThreadSettings{}, fmt.Errorf("codex: service tier %q is invalid for model %q", desiredTier, desiredModel)
	}
	params := map[string]any{"threadId": threadID}
	if patch.Model != nil || desiredModel != current.Model {
		params["model"] = desiredModel
	}
	if patch.Model != nil || patch.Effort != nil || patch.PlanEffort != nil || patch.Mode != nil || normalEffort != current.Effort {
		params["effort"] = normalEffort
	}
	if patch.ServiceTier != nil {
		if desiredTier == "" {
			params["serviceTier"] = nil
		} else {
			params["serviceTier"] = desiredTier
		}
	}
	if patch.Personality != nil {
		personality := strings.TrimSpace(*patch.Personality)
		if personality != "" && personality != "none" && personality != "friendly" && personality != "pragmatic" {
			return core.NativeThreadSettings{}, fmt.Errorf("codex: invalid personality %q", personality)
		}
		if personality != "" && personality != "none" && !selectedModel.SupportsPersonality {
			return core.NativeThreadSettings{}, fmt.Errorf("codex: model %q does not support personality", desiredModel)
		}
		if personality == "" {
			params["personality"] = nil
		} else {
			params["personality"] = personality
		}
	}
	if patch.Summary != nil {
		summary := strings.TrimSpace(*patch.Summary)
		switch summary {
		case "":
			params["summary"] = nil
		case "auto", "concise", "detailed", "none":
			params["summary"] = summary
		default:
			return core.NativeThreadSettings{}, fmt.Errorf("codex: invalid reasoning summary %q", summary)
		}
	}
	if patch.PermissionProfile != nil {
		if !permissionCapability.Supported {
			return core.NativeThreadSettings{}, fmt.Errorf("codex: permission profiles unavailable: %s", permissionCapability.Reason)
		}
		profile := strings.TrimSpace(*patch.PermissionProfile)
		if profile == "" {
			params["permissions"] = nil
		} else {
			found := false
			for _, candidate := range permissions {
				if candidate.ID == profile && candidate.Allowed {
					found = true
					break
				}
			}
			if !found {
				return core.NativeThreadSettings{}, fmt.Errorf("codex: permission profile %q is not selectable", profile)
			}
			params["permissions"] = profile
		}
	}
	if modeNeedsUpdate {
		modeEffort := &normalEffort
		if desiredMode == "plan" {
			modeEffort = planEffort
		}
		params["model"], params["effort"] = desiredModel, normalEffort
		params["collaborationMode"] = nativeModeSettings(selectedMode, desiredModel, modeEffort)
	}
	if len(params) == 1 {
		return current, nil
	}
	settings, err := control.waitNativeSettings(ctx, threadID, params)
	if err != nil {
		return core.NativeThreadSettings{}, fmt.Errorf("codex: thread/settings/update: %w", err)
	}
	return settings, nil
}

func validNativeURL(raw, dataMediaPrefix string) bool {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(strings.ToLower(raw), "data:") {
		return strings.HasPrefix(strings.ToLower(raw), "data:"+dataMediaPrefix+"/")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != ""
}

func nativeLocalPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("local attachment path must be absolute")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("local attachment path is a directory")
	}
	return filepath.Clean(path), nil
}

func nativeUserInputs(inputs []core.NativeUserInput) ([]map[string]any, error) {
	if len(inputs) == 0 {
		return nil, fmt.Errorf("codex: native Turn input is empty")
	}
	mapped := make([]map[string]any, 0, len(inputs))
	for index, input := range inputs {
		switch strings.TrimSpace(input.Type) {
		case "text":
			if input.LocalPath != "" || input.URL != "" || input.AttachmentRef != "" || len(input.Data) != 0 {
				return nil, fmt.Errorf("codex: text input %d contains attachment fields", index)
			}
			mapped = append(mapped, map[string]any{"type": "text", "text": input.Text, "text_elements": []any{}})
		case "image":
			if input.AttachmentRef != "" || len(input.Data) != 0 {
				return nil, fmt.Errorf("codex: image input %d contains an unresolved attachment reference", index)
			}
			detail := strings.TrimSpace(input.Detail)
			if detail != "" && detail != "auto" && detail != "low" && detail != "high" && detail != "original" {
				return nil, fmt.Errorf("codex: image input %d has invalid detail %q", index, detail)
			}
			if input.LocalPath != "" {
				path, err := nativeLocalPath(input.LocalPath)
				if err != nil {
					return nil, fmt.Errorf("codex: image input %d: %w", index, err)
				}
				value := map[string]any{"type": "localImage", "path": path}
				if detail != "" {
					value["detail"] = detail
				}
				mapped = append(mapped, value)
				continue
			}
			if !validNativeURL(input.URL, "image") {
				return nil, fmt.Errorf("codex: image input %d has invalid URL", index)
			}
			value := map[string]any{"type": "image", "url": input.URL}
			if detail != "" {
				value["detail"] = detail
			}
			mapped = append(mapped, value)
		case "audio":
			return nil, fmt.Errorf("codex: audio input %d must be converted to a verified text attachment reference by the service", index)
		case "file":
			return nil, fmt.Errorf("codex: file input %d must be converted to a verified path text reference by the service", index)
		default:
			return nil, fmt.Errorf("codex: unsupported native input type %q", input.Type)
		}
	}
	return mapped, nil
}

func (a *Agent) StartNativeTurn(ctx context.Context, workspace core.Workspace, threadID string, generation uint64, request core.NativeTurnStartRequest) (core.NativeTurnResult, error) {
	control, _, cwd, err := a.nativeControl(ctx, workspace)
	if err != nil {
		return core.NativeTurnResult{}, err
	}
	if err := requireNativeGeneration(control, generation); err != nil {
		return core.NativeTurnResult{}, err
	}
	if _, err := a.readNativeThread(ctx, control, cwd, threadID); err != nil {
		return core.NativeTurnResult{}, err
	}
	if err := control.registerNativeThread(threadID, cwd); err != nil {
		return core.NativeTurnResult{}, err
	}
	input, err := nativeUserInputs(request.Input)
	if err != nil {
		return core.NativeTurnResult{}, err
	}
	params := map[string]any{"threadId": threadID, "input": input}
	if clientID := strings.TrimSpace(request.ClientMessageID); clientID != "" {
		params["clientUserMessageId"] = clientID
	}
	var response struct {
		Turn nativeTurnWire `json:"turn"`
	}
	if err := control.mutationRequestContext(ctx, "turn/start", params, &response); err != nil {
		return core.NativeTurnResult{}, fmt.Errorf("codex: turn/start: %w", err)
	}
	turnID := strings.TrimSpace(response.Turn.ID)
	if turnID == "" {
		return core.NativeTurnResult{}, nativeAcceptanceUnknown("turn/start", fmt.Errorf("codex: turn/start returned empty turn id"))
	}
	control.nativeMu.Lock()
	state := control.nativeStateLocked(threadID)
	if state.LastCompletedTurnID != turnID {
		state.ActiveTurn = &core.NativeActiveTurn{ID: turnID, RequestID: request.ClientMessageID, StartedAt: time.Now().UTC()}
	}
	control.nativeMu.Unlock()
	return core.NativeTurnResult{TurnID: turnID}, nil
}

func (a *Agent) SteerNativeTurn(ctx context.Context, workspace core.Workspace, threadID string, generation uint64, expectedTurnID string, inputs []core.NativeUserInput) (core.NativeTurnResult, error) {
	control, _, cwd, err := a.nativeControl(ctx, workspace)
	if err != nil {
		return core.NativeTurnResult{}, err
	}
	if err := requireNativeGeneration(control, generation); err != nil {
		return core.NativeTurnResult{}, err
	}
	if _, err := a.readNativeThread(ctx, control, cwd, threadID); err != nil {
		return core.NativeTurnResult{}, err
	}
	if err := control.registerNativeThread(threadID, cwd); err != nil {
		return core.NativeTurnResult{}, err
	}
	expectedTurnID = strings.TrimSpace(expectedTurnID)
	if expectedTurnID == "" {
		return core.NativeTurnResult{}, fmt.Errorf("codex: expected Turn id is empty")
	}
	input, err := nativeUserInputs(inputs)
	if err != nil {
		return core.NativeTurnResult{}, err
	}
	var response struct {
		TurnID string `json:"turnId"`
	}
	if err := control.mutationRequestContext(ctx, "turn/steer", map[string]any{
		"threadId": threadID, "expectedTurnId": expectedTurnID, "input": input,
	}, &response); err != nil {
		return core.NativeTurnResult{}, fmt.Errorf("codex: turn/steer: %w", err)
	}
	if strings.TrimSpace(response.TurnID) == "" || response.TurnID != expectedTurnID {
		return core.NativeTurnResult{}, nativeAcceptanceUnknown("turn/steer", fmt.Errorf("codex: turn/steer returned unexpected turn id"))
	}
	return core.NativeTurnResult{TurnID: response.TurnID}, nil
}

func (a *Agent) InterruptNativeTurn(ctx context.Context, workspace core.Workspace, threadID string, generation uint64, turnID string) error {
	control, _, cwd, err := a.nativeControl(ctx, workspace)
	if err != nil {
		return err
	}
	if err := requireNativeGeneration(control, generation); err != nil {
		return err
	}
	if _, err := a.readNativeThread(ctx, control, cwd, threadID); err != nil {
		return err
	}
	if err := control.registerNativeThread(threadID, cwd); err != nil {
		return err
	}
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return fmt.Errorf("codex: interrupt Turn id is empty")
	}
	if err := control.mutationRequestContext(ctx, "turn/interrupt", map[string]any{"threadId": threadID, "turnId": turnID}, nil); err != nil {
		return fmt.Errorf("codex: turn/interrupt: %w", err)
	}
	return nil
}

func decodeStrictNativeJSON(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func nativeJSONSubset(granted, requested json.RawMessage) bool {
	var grantedValue, requestedValue any
	if json.Unmarshal(granted, &grantedValue) != nil || json.Unmarshal(requested, &requestedValue) != nil {
		return false
	}
	return nativeValueSubset(grantedValue, requestedValue)
}

func nativeValueSubset(granted, requested any) bool {
	switch value := granted.(type) {
	case map[string]any:
		available, ok := requested.(map[string]any)
		if !ok {
			return false
		}
		for key, child := range value {
			requestedChild, exists := available[key]
			if !exists || !nativeValueSubset(child, requestedChild) {
				return false
			}
		}
		return true
	case []any:
		available, ok := requested.([]any)
		if !ok {
			return false
		}
		for _, child := range value {
			found := false
			for _, requestedChild := range available {
				if reflect.DeepEqual(child, requestedChild) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(granted, requested)
	}
}

func validateNativeApprovalResponse(pending nativePendingRequest, response json.RawMessage) error {
	switch pending.Method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		var value struct {
			Decision json.RawMessage `json:"decision"`
		}
		if decodeStrictNativeJSON(response, &value) != nil || len(value.Decision) == 0 {
			return fmt.Errorf("approval response requires decision")
		}
		allowed := nativeAllowedDecisions(pending.Method, pending.Params)
		for _, allowedDecision := range allowed {
			if rawMessagesEqual(value.Decision, allowedDecision) {
				return nil
			}
		}
		return fmt.Errorf("approval decision was not offered by App Server")
	case "mcpServer/elicitation/request":
		var value struct {
			Action  string          `json:"action"`
			Content json.RawMessage `json:"content"`
		}
		if decodeStrictNativeJSON(response, &value) != nil {
			return fmt.Errorf("invalid MCP elicitation action")
		}
		action, _ := json.Marshal(value.Action)
		allowed := false
		for _, candidate := range nativeAllowedDecisions(pending.Method, pending.Params) {
			if rawMessagesEqual(action, candidate) {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("MCP elicitation action was not offered by App Server")
		}
		content := bytes.TrimSpace(value.Content)
		if value.Action == "accept" && (len(content) == 0 || bytes.Equal(content, []byte("null"))) {
			return fmt.Errorf("accepted MCP elicitation requires content")
		}
		if value.Action != "accept" && !bytes.Equal(content, []byte("null")) {
			return fmt.Errorf("declined or cancelled MCP elicitation requires null content")
		}
		return nil
	case "item/permissions/requestApproval":
		var requested struct {
			Permissions json.RawMessage `json:"permissions"`
		}
		var value struct {
			Permissions json.RawMessage `json:"permissions"`
			Scope       string          `json:"scope"`
			Strict      *bool           `json:"strictAutoReview"`
		}
		if json.Unmarshal(pending.Params, &requested) != nil || decodeStrictNativeJSON(response, &value) != nil || len(value.Permissions) == 0 {
			return fmt.Errorf("invalid permissions approval response")
		}
		if value.Scope != "" && value.Scope != "turn" && value.Scope != "session" {
			return fmt.Errorf("invalid permission grant scope")
		}
		if !nativeJSONSubset(value.Permissions, requested.Permissions) {
			return fmt.Errorf("permissions response exceeds the App Server request")
		}
		return nil
	case "item/tool/requestUserInput":
		var requested struct {
			Questions []struct {
				ID string `json:"id"`
			} `json:"questions"`
		}
		var value struct {
			Answers map[string]struct {
				Answers []string `json:"answers"`
			} `json:"answers"`
		}
		if json.Unmarshal(pending.Params, &requested) != nil || decodeStrictNativeJSON(response, &value) != nil || value.Answers == nil {
			return fmt.Errorf("invalid requestUserInput response")
		}
		allowed := make(map[string]struct{}, len(requested.Questions))
		for _, question := range requested.Questions {
			allowed[question.ID] = struct{}{}
		}
		for id := range value.Answers {
			if _, ok := allowed[id]; !ok {
				return fmt.Errorf("answer references unknown question %q", id)
			}
		}
		return nil
	default:
		return fmt.Errorf("codex: unsupported native interaction %q", pending.Method)
	}
}

func (a *Agent) RespondNativeInteraction(ctx context.Context, workspace core.Workspace, threadID string, generation uint64, requestID, response json.RawMessage) error {
	control, _, cwd, err := a.nativeControl(ctx, workspace)
	if err != nil {
		return err
	}
	if err := requireNativeGeneration(control, generation); err != nil {
		return err
	}
	if _, err := a.readNativeThread(ctx, control, cwd, threadID); err != nil {
		return err
	}
	if err := control.registerNativeThread(threadID, cwd); err != nil {
		return err
	}
	key := nativeRequestKey(generation, threadID, requestID)
	control.nativeMu.Lock()
	pending, exists := control.nativeRequests[key]
	control.nativeMu.Unlock()
	if !exists {
		return fmt.Errorf("codex: native interaction is not pending")
	}
	if err := validateNativeApprovalResponse(pending, response); err != nil {
		return err
	}
	if len(response) == 0 || !json.Valid(response) {
		return fmt.Errorf("codex: interaction response is invalid JSON")
	}
	if err := control.mutationWriteJSONWithTimeout(ctx, "native interaction response", map[string]any{
		"jsonrpc": "2.0", "id": json.RawMessage(requestID), "result": json.RawMessage(response),
	}, appServerRequestTimeout); err != nil {
		return err
	}
	control.nativeMu.Lock()
	delete(control.nativeRequests, key)
	delete(control.nativeStateLocked(threadID).PendingInteractions, key)
	control.nativeMu.Unlock()
	return nil
}

func containsNativeVoice(catalog core.NativeRealtimeVoiceCatalog, version, voice string) bool {
	voices := catalog.V2
	if version == "v1" {
		voices = catalog.V1
	}
	for _, candidate := range voices {
		if candidate == voice {
			return true
		}
	}
	return false
}

func (a *Agent) StartNativeRealtime(ctx context.Context, workspace core.Workspace, threadID string, generation uint64, request core.NativeRealtimeStartRequest) error {
	control, resolved, cwd, err := a.nativeControl(ctx, workspace)
	if err != nil {
		return err
	}
	if err := requireNativeGeneration(control, generation); err != nil {
		return err
	}
	if _, err := a.readNativeThread(ctx, control, cwd, threadID); err != nil {
		return err
	}
	if err := control.registerNativeThread(threadID, cwd); err != nil {
		return err
	}
	sdp := strings.TrimSpace(request.SDP)
	if sdp == "" {
		return fmt.Errorf("codex: WebRTC SDP offer is empty")
	}
	version := strings.TrimSpace(request.Version)
	if version == "" {
		version = "v2"
	}
	if version != "v1" && version != "v2" && version != "v3" {
		return fmt.Errorf("codex: unsupported realtime version %q", version)
	}
	catalog, err := a.nativeRuntimeCatalog(ctx, control, resolved, cwd)
	if err != nil {
		return err
	}
	if capability := catalog.Capabilities[nativeCapabilityRealtime]; !capability.Supported {
		return fmt.Errorf("codex: realtime unavailable: %s", capability.Reason)
	}
	voice := strings.TrimSpace(request.Voice)
	if voice == "" {
		if version == "v1" {
			voice = catalog.Voices.DefaultV1
		} else {
			voice = catalog.Voices.DefaultV2
		}
	}
	if voice == "" || !containsNativeVoice(catalog.Voices, version, voice) {
		return fmt.Errorf("codex: realtime voice %q is not available", voice)
	}
	params := map[string]any{
		"threadId": threadID, "outputModality": "audio", "version": version, "voice": voice,
		"transport": map[string]any{"type": "webrtc", "sdp": sdp},
	}
	if err := control.mutationRequestContext(ctx, "thread/realtime/start", params, nil); err != nil {
		return fmt.Errorf("codex: thread/realtime/start: %w", err)
	}
	return nil
}

func (a *Agent) AppendNativeRealtimeText(ctx context.Context, workspace core.Workspace, threadID string, generation uint64, text string) error {
	control, _, cwd, err := a.nativeControl(ctx, workspace)
	if err != nil {
		return err
	}
	if err := requireNativeGeneration(control, generation); err != nil {
		return err
	}
	if _, err := a.readNativeThread(ctx, control, cwd, threadID); err != nil {
		return err
	}
	if err := control.registerNativeThread(threadID, cwd); err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("codex: realtime text is empty")
	}
	if err := control.mutationRequestContext(ctx, "thread/realtime/appendText", map[string]any{"threadId": threadID, "role": "user", "text": text}, nil); err != nil {
		return fmt.Errorf("codex: thread/realtime/appendText: %w", err)
	}
	return nil
}

func (a *Agent) StopNativeRealtime(ctx context.Context, workspace core.Workspace, threadID string, generation uint64) error {
	control, _, cwd, err := a.nativeControl(ctx, workspace)
	if err != nil {
		return err
	}
	if err := requireNativeGeneration(control, generation); err != nil {
		return err
	}
	if _, err := a.readNativeThread(ctx, control, cwd, threadID); err != nil {
		return err
	}
	if err := control.registerNativeThread(threadID, cwd); err != nil {
		return err
	}
	if err := control.mutationRequestContext(ctx, "thread/realtime/stop", map[string]any{"threadId": threadID}, nil); err != nil {
		return fmt.Errorf("codex: thread/realtime/stop: %w", err)
	}
	return nil
}

var _ core.NativeConversationBackend = (*Agent)(nil)
var _ core.NativeConversationSettingsController = (*Agent)(nil)
var _ core.NativeConversationTurnController = (*Agent)(nil)
var _ core.NativeConversationRealtimeController = (*Agent)(nil)
