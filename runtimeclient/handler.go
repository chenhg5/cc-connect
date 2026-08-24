package runtimeclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/chenhg5/cc-connect/core"
	"github.com/chenhg5/cc-connect/runtimeprotocol"
)

type Dependencies struct {
	Catalog   core.WorkspaceCatalogProvider
	Validator core.WorkspaceAccessValidator
	Backend   core.NativeConversationBackend
	Settings  core.NativeConversationSettingsController
	Turns     core.NativeConversationTurnController
	Realtime  core.NativeConversationRealtimeController
	Updater   RuntimeUpdater
}

type RuntimeUpdater interface {
	Stage(context.Context, string) error
	Activate(context.Context, string) error
	Confirm(context.Context, string) error
}

type Handler struct {
	dependencies Dependencies

	mu              sync.Mutex
	subscriptions   map[string]*runtimeSubscription
	emit            func(runtimeprotocol.Method, runtimeprotocol.Resource, json.RawMessage) error
	fetchAttachment func(context.Context, string) (runtimeprotocol.AttachmentContent, error)
	turnArtifacts   map[string][]string
	terminalTurns   map[string]struct{}
}

type runtimeSubscription struct {
	cancel func()
	done   chan struct{}
}

func NewHandler(dependencies Dependencies) (*Handler, error) {
	if dependencies.Catalog == nil || dependencies.Validator == nil || dependencies.Backend == nil || dependencies.Settings == nil || dependencies.Turns == nil {
		return nil, errors.New("runtime handler: catalog, validator, backend, settings and turns are required")
	}
	return &Handler{
		dependencies: dependencies, subscriptions: make(map[string]*runtimeSubscription),
		turnArtifacts: make(map[string][]string), terminalTurns: make(map[string]struct{}),
	}, nil
}

func (h *Handler) SetEventEmitter(emitter func(runtimeprotocol.Method, runtimeprotocol.Resource, json.RawMessage) error) {
	h.mu.Lock()
	h.emit = emitter
	h.mu.Unlock()
}

func (h *Handler) Handle(ctx context.Context, method runtimeprotocol.Method, resource runtimeprotocol.Resource, payload json.RawMessage) (json.RawMessage, error) {
	if method == runtimeprotocol.MethodCatalogList {
		return h.catalog(ctx)
	}
	if method == runtimeprotocol.MethodUpdateStage || method == runtimeprotocol.MethodUpdateActivate || method == runtimeprotocol.MethodUpdateConfirm {
		if h.dependencies.Updater == nil {
			return nil, errors.New("runtime handler: signed release updates are unavailable")
		}
		request, err := decodePayload[runtimeprotocol.RuntimeUpdateRequest](payload)
		if err != nil {
			return nil, err
		}
		if method == runtimeprotocol.MethodUpdateStage {
			err = h.dependencies.Updater.Stage(ctx, request.Tag)
			return encodeResult(runtimeprotocol.RuntimeUpdateResult{Tag: request.Tag, Status: "staged"}, err)
		}
		if method == runtimeprotocol.MethodUpdateConfirm {
			err = h.dependencies.Updater.Confirm(ctx, request.Tag)
			return encodeResult(runtimeprotocol.RuntimeUpdateResult{Tag: request.Tag, Status: "confirmed"}, err)
		}
		err = h.dependencies.Updater.Activate(ctx, request.Tag)
		return encodeResult(runtimeprotocol.RuntimeUpdateResult{Tag: request.Tag, Status: "activating"}, err)
	}
	workspace, err := h.workspace(ctx, resource.WorkspaceRef)
	if err != nil {
		return nil, err
	}
	switch method {
	case runtimeprotocol.MethodThreadList:
		page, err := decodePayload[core.NativePageRequest](payload)
		if err != nil {
			return nil, err
		}
		result, err := h.dependencies.Backend.ListNativeConversations(ctx, workspace, page)
		for index := range result.Data {
			result.Data[index].Cwd = ""
		}
		return encodeResult(result, err)
	case runtimeprotocol.MethodThreadRead:
		result, err := h.dependencies.Backend.ReadNativeConversation(ctx, workspace, requiredConversation(resource))
		result.Thread.Cwd = ""
		return encodeResult(result, err)
	case runtimeprotocol.MethodThreadStart:
		result, err := h.dependencies.Backend.StartNativeConversation(ctx, workspace)
		result.Thread.Cwd = ""
		return encodeResult(result, err)
	case runtimeprotocol.MethodTurnList:
		page, err := decodePayload[core.NativePageRequest](payload)
		if err != nil {
			return nil, err
		}
		result, err := h.dependencies.Backend.ListNativeTurns(ctx, workspace, requiredConversation(resource), page)
		return encodeResult(result, err)
	case runtimeprotocol.MethodItemList:
		page, err := decodePayload[core.NativePageRequest](payload)
		if err != nil {
			return nil, err
		}
		result, err := h.dependencies.Backend.ListNativeItems(ctx, workspace, requiredConversation(resource), resource.TurnID, page)
		return encodeResult(result, err)
	case runtimeprotocol.MethodRuntimeCatalog:
		result, err := h.dependencies.Backend.NativeRuntimeCatalog(ctx, workspace)
		return encodeResult(result, err)
	case runtimeprotocol.MethodThreadSubscribe:
		return h.subscribe(ctx, workspace, requiredConversation(resource))
	case runtimeprotocol.MethodSettingsUpdate:
		request, err := decodePayload[struct {
			Generation uint64                         `json:"generation"`
			Patch      core.NativeThreadSettingsPatch `json:"patch"`
		}](payload)
		if err != nil {
			return nil, err
		}
		result, err := h.dependencies.Settings.UpdateNativeConversationSettings(ctx, workspace, requiredConversation(resource), request.Generation, request.Patch)
		return encodeResult(result, err)
	case runtimeprotocol.MethodTurnStart:
		request, err := decodePayload[struct {
			Generation uint64                      `json:"generation"`
			Request    core.NativeTurnStartRequest `json:"request"`
		}](payload)
		if err != nil {
			return nil, err
		}
		prepared, artifacts, err := h.materializeInputs(ctx, workspace, request.Request.Input)
		if err != nil {
			return nil, err
		}
		request.Request.Input = prepared
		result, err := h.dependencies.Turns.StartNativeTurn(ctx, workspace, requiredConversation(resource), request.Generation, request.Request)
		if err != nil {
			removeArtifactDirectories(artifacts)
			return nil, err
		}
		h.bindTurnArtifacts(workspace.Ref, requiredConversation(resource), result.TurnID, artifacts)
		return encodeResult(result, err)
	case runtimeprotocol.MethodTurnSteer:
		request, err := decodePayload[struct {
			Generation uint64                 `json:"generation"`
			Input      []core.NativeUserInput `json:"input"`
		}](payload)
		if err != nil {
			return nil, err
		}
		prepared, artifacts, err := h.materializeInputs(ctx, workspace, request.Input)
		if err != nil {
			return nil, err
		}
		result, err := h.dependencies.Turns.SteerNativeTurn(ctx, workspace, requiredConversation(resource), request.Generation, resource.TurnID, prepared)
		if err != nil {
			removeArtifactDirectories(artifacts)
			return nil, err
		}
		h.bindTurnArtifacts(workspace.Ref, requiredConversation(resource), resource.TurnID, artifacts)
		return encodeResult(result, err)
	case runtimeprotocol.MethodTurnInterrupt:
		request, err := decodePayload[struct {
			Generation uint64 `json:"generation"`
		}](payload)
		if err != nil {
			return nil, err
		}
		err = h.dependencies.Turns.InterruptNativeTurn(ctx, workspace, requiredConversation(resource), request.Generation, resource.TurnID)
		return encodeResult(struct{}{}, err)
	case runtimeprotocol.MethodInteractionReply:
		request, err := decodePayload[struct {
			Generation uint64          `json:"generation"`
			RequestID  json.RawMessage `json:"request_id"`
			Response   json.RawMessage `json:"response"`
		}](payload)
		if err != nil {
			return nil, err
		}
		err = h.dependencies.Turns.RespondNativeInteraction(ctx, workspace, requiredConversation(resource), request.Generation, request.RequestID, request.Response)
		return encodeResult(struct{}{}, err)
	case runtimeprotocol.MethodRealtimeStart:
		if h.dependencies.Realtime == nil {
			return nil, errors.New("runtime handler: realtime is unavailable")
		}
		request, err := decodePayload[struct {
			Generation uint64                          `json:"generation"`
			Request    core.NativeRealtimeStartRequest `json:"request"`
		}](payload)
		if err != nil {
			return nil, err
		}
		err = h.dependencies.Realtime.StartNativeRealtime(ctx, workspace, requiredConversation(resource), request.Generation, request.Request)
		return encodeResult(struct{}{}, err)
	case runtimeprotocol.MethodRealtimeAppend:
		if h.dependencies.Realtime == nil {
			return nil, errors.New("runtime handler: realtime is unavailable")
		}
		request, err := decodePayload[struct {
			Generation uint64 `json:"generation"`
			Text       string `json:"text"`
		}](payload)
		if err != nil {
			return nil, err
		}
		err = h.dependencies.Realtime.AppendNativeRealtimeText(ctx, workspace, requiredConversation(resource), request.Generation, request.Text)
		return encodeResult(struct{}{}, err)
	case runtimeprotocol.MethodRealtimeStop:
		if h.dependencies.Realtime == nil {
			return nil, errors.New("runtime handler: realtime is unavailable")
		}
		request, err := decodePayload[struct {
			Generation uint64 `json:"generation"`
		}](payload)
		if err != nil {
			return nil, err
		}
		err = h.dependencies.Realtime.StopNativeRealtime(ctx, workspace, requiredConversation(resource), request.Generation)
		return encodeResult(struct{}{}, err)
	default:
		return nil, fmt.Errorf("runtime handler: method %q is not a request method", method)
	}
}

func (h *Handler) workspace(ctx context.Context, localRef string) (core.Workspace, error) {
	if strings.TrimSpace(localRef) == "" {
		return core.Workspace{}, errors.New("runtime handler: workspace_ref is required")
	}
	workspace, err := h.dependencies.Catalog.ResolveWorkspace(ctx, localRef)
	if err != nil {
		return core.Workspace{}, fmt.Errorf("runtime handler: resolve workspace: %w", err)
	}
	if err := h.dependencies.Validator.ValidateWorkspaceAccess(ctx, workspace); err != nil {
		return core.Workspace{}, fmt.Errorf("runtime handler: validate workspace: %w", err)
	}
	return workspace, nil
}

func (h *Handler) catalog(ctx context.Context) (json.RawMessage, error) {
	workspaces, err := h.dependencies.Catalog.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	result := runtimeprotocol.Catalog{Workspaces: make([]runtimeprotocol.Workspace, 0, len(workspaces))}
	for _, workspace := range workspaces {
		result.Workspaces = append(result.Workspaces, runtimeprotocol.Workspace{
			LocalRef: workspace.Ref, ProjectID: workspace.ProjectID, ProjectName: workspace.ProjectName,
			RootIndex: workspace.RootIndex, RootName: workspace.RootName,
			Available: workspace.Available, Reason: workspace.Error, Order: workspace.Order,
		})
	}
	return encodeResult(result, nil)
}

func (h *Handler) CatalogSnapshot(ctx context.Context) (json.RawMessage, error) {
	return h.catalog(ctx)
}

func (h *Handler) subscribe(ctx context.Context, workspace core.Workspace, threadID string) (json.RawMessage, error) {
	subscription, err := h.dependencies.Backend.SubscribeNativeConversation(ctx, workspace, threadID)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		subscription.Cancel()
		return nil, err
	}
	key := workspace.Ref + "\x00" + threadID
	registered := &runtimeSubscription{cancel: subscription.Cancel, done: make(chan struct{})}
	h.mu.Lock()
	if err := ctx.Err(); err != nil {
		h.mu.Unlock()
		subscription.Cancel()
		return nil, err
	}
	previous := h.subscriptions[key]
	h.subscriptions[key] = registered
	h.mu.Unlock()
	if previous != nil {
		previous.cancel()
		<-previous.done
	}
	go h.forwardEvents(workspace.Ref, threadID, key, registered, subscription)
	return encodeResult(struct {
		Generation uint64 `json:"generation"`
	}{subscription.Generation}, nil)
}

func (h *Handler) forwardEvents(workspaceRef, threadID, key string, registered *runtimeSubscription, subscription core.NativeConversationSubscription) {
	defer func() {
		subscription.Cancel()
		h.mu.Lock()
		if h.subscriptions[key] == registered {
			delete(h.subscriptions, key)
		}
		h.mu.Unlock()
		close(registered.done)
	}()
	for event := range subscription.Events {
		h.completeTurnArtifacts(workspaceRef, threadID, event.TurnID, event.Method)
		payload, err := json.Marshal(event)
		if err != nil {
			continue
		}
		h.mu.Lock()
		emitter := h.emit
		h.mu.Unlock()
		if emitter == nil || emitter(runtimeprotocol.MethodNativeEvent, runtimeprotocol.Resource{
			WorkspaceRef: workspaceRef, ConversationRef: threadID, TurnID: event.TurnID,
			ItemID: event.ItemID, InteractionID: event.InteractionID,
		}, payload) != nil {
			return
		}
	}
}

func (h *Handler) ReleaseConnection() {
	h.mu.Lock()
	subscriptions := make([]*runtimeSubscription, 0, len(h.subscriptions))
	for _, subscription := range h.subscriptions {
		subscriptions = append(subscriptions, subscription)
	}
	h.subscriptions = make(map[string]*runtimeSubscription)
	h.mu.Unlock()
	for _, subscription := range subscriptions {
		subscription.cancel()
	}
	for _, subscription := range subscriptions {
		<-subscription.done
	}
	h.ReleaseConnectionArtifacts()
}

func (h *Handler) Close() {
	h.ReleaseConnection()
}

func requiredConversation(resource runtimeprotocol.Resource) string {
	return strings.TrimSpace(resource.ConversationRef)
}

func decodePayload[T any](payload json.RawMessage) (T, error) {
	var result T
	if len(payload) == 0 {
		return result, errors.New("runtime handler: payload is required")
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, fmt.Errorf("runtime handler: invalid payload: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return result, errors.New("runtime handler: payload contains trailing JSON")
	}
	return result, nil
}

func encodeResult(value any, err error) (json.RawMessage, error) {
	if err != nil {
		return nil, err
	}
	payload, marshalErr := json.Marshal(value)
	if marshalErr != nil {
		return nil, fmt.Errorf("runtime handler: encode response: %w", marshalErr)
	}
	return payload, nil
}
