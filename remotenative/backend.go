package remotenative

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/chenhg5/cc-connect/controlplane"
	"github.com/chenhg5/cc-connect/core"
	"github.com/chenhg5/cc-connect/runtimeprotocol"
)

type Backend struct {
	client *http.Client
	base   string
}

func New(socketPath string) (*Backend, error) {
	if strings.TrimSpace(socketPath) == "" {
		return nil, errors.New("remote native backend: runtime socket is required")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	}}
	return &Backend{client: &http.Client{Transport: transport}, base: "http://cc-connect-control"}, nil
}

func (b *Backend) ListWorkspaces(ctx context.Context) ([]core.Workspace, error) {
	var catalog []controlplane.CatalogWorkspace
	if err := b.get(ctx, "/runtime/v1/catalog", &catalog); err != nil {
		return nil, err
	}
	result := make([]core.Workspace, 0, len(catalog))
	for _, workspace := range catalog {
		result = append(result, core.Workspace{
			Ref: workspace.Ref, DeviceID: workspace.DeviceID, DeviceName: workspace.DeviceName,
			ProjectID: workspace.ProjectID, ProjectName: workspace.ProjectName,
			RootIndex: workspace.RootIndex, RootName: workspace.RootName,
			Available: workspace.Available, Online: workspace.Online, Error: workspace.Reason, Order: workspace.Order,
		})
	}
	return result, nil
}

func (b *Backend) ListWorkspaceDevices(ctx context.Context) ([]core.WorkspaceDevice, error) {
	var devices []controlplane.DeviceStatus
	if err := b.get(ctx, "/runtime/v1/devices", &devices); err != nil {
		return nil, err
	}
	result := make([]core.WorkspaceDevice, 0, len(devices))
	for _, device := range devices {
		result = append(result, core.WorkspaceDevice{
			ID: device.ID, Name: device.Name, Online: device.Online,
			Revoked: device.RevokedAt != nil,
		})
	}
	return result, nil
}

func (b *Backend) ResolveWorkspace(ctx context.Context, ref string) (core.Workspace, error) {
	workspaces, err := b.ListWorkspaces(ctx)
	if err != nil {
		return core.Workspace{}, err
	}
	for _, workspace := range workspaces {
		if workspace.Ref == ref {
			return workspace, nil
		}
	}
	return core.Workspace{}, core.ErrWorkspaceNotFound
}

func (b *Backend) ValidateWorkspaceAccess(ctx context.Context, workspace core.Workspace) error {
	resolved, err := b.ResolveWorkspace(ctx, workspace.Ref)
	if err != nil {
		return err
	}
	if resolved.DeviceID != workspace.DeviceID || !resolved.Online || !resolved.Available {
		return fmt.Errorf("remote native backend: workspace unavailable: %s", resolved.Error)
	}
	return nil
}

func (b *Backend) ValidateNativeThreadAccess(_ context.Context, workspace core.Workspace, thread core.NativeThread) error {
	if strings.TrimSpace(thread.ID) == "" || thread.Cwd != workspace.Ref {
		return core.ErrNativeThreadNotFound
	}
	return nil
}

func (b *Backend) ListNativeConversations(ctx context.Context, workspace core.Workspace, page core.NativePageRequest) (core.NativeThreadPage, error) {
	var result core.NativeThreadPage
	if err := b.rpc(ctx, workspace, runtimeprotocol.MethodThreadList, runtimeprotocol.Resource{}, page, &result); err != nil {
		return result, err
	}
	for index := range result.Data {
		result.Data[index].Cwd = workspace.Ref
	}
	return result, nil
}

func (b *Backend) ReadNativeConversation(ctx context.Context, workspace core.Workspace, threadID string) (core.NativeConversationSnapshot, error) {
	var result core.NativeConversationSnapshot
	err := b.rpc(ctx, workspace, runtimeprotocol.MethodThreadRead, runtimeprotocol.Resource{ConversationRef: threadID}, nil, &result)
	result.Thread.Cwd = workspace.Ref
	return result, err
}

func (b *Backend) StartNativeConversation(ctx context.Context, workspace core.Workspace) (core.NativeConversationSnapshot, error) {
	var result core.NativeConversationSnapshot
	err := b.rpc(ctx, workspace, runtimeprotocol.MethodThreadStart, runtimeprotocol.Resource{}, nil, &result)
	result.Thread.Cwd = workspace.Ref
	return result, err
}

func (b *Backend) ListNativeTurns(ctx context.Context, workspace core.Workspace, threadID string, page core.NativePageRequest) (core.NativeTurnPage, error) {
	var result core.NativeTurnPage
	err := b.rpc(ctx, workspace, runtimeprotocol.MethodTurnList, runtimeprotocol.Resource{ConversationRef: threadID}, page, &result)
	return result, err
}

func (b *Backend) ListNativeItems(ctx context.Context, workspace core.Workspace, threadID, turnID string, page core.NativePageRequest) (core.NativeItemPage, error) {
	var result core.NativeItemPage
	err := b.rpc(ctx, workspace, runtimeprotocol.MethodItemList, runtimeprotocol.Resource{ConversationRef: threadID, TurnID: turnID}, page, &result)
	return result, err
}

func (b *Backend) NativeRuntimeCatalog(ctx context.Context, workspace core.Workspace) (core.NativeRuntimeCatalog, error) {
	var result core.NativeRuntimeCatalog
	err := b.rpc(ctx, workspace, runtimeprotocol.MethodRuntimeCatalog, runtimeprotocol.Resource{}, nil, &result)
	return result, err
}

func (b *Backend) SubscribeNativeConversation(ctx context.Context, workspace core.Workspace, threadID string) (core.NativeConversationSubscription, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	events := make(chan core.NativeEventEnvelope, 128)
	ready := make(chan error, 1)
	go b.streamEvents(streamCtx, workspace.Ref, threadID, events, ready)
	if err := <-ready; err != nil {
		cancel()
		return core.NativeConversationSubscription{}, err
	}
	var response struct {
		Generation uint64 `json:"generation"`
	}
	if err := b.rpc(ctx, workspace, runtimeprotocol.MethodThreadSubscribe, runtimeprotocol.Resource{ConversationRef: threadID}, nil, &response); err != nil {
		cancel()
		return core.NativeConversationSubscription{}, err
	}
	if response.Generation == 0 {
		cancel()
		return core.NativeConversationSubscription{}, errors.New("remote native backend: subscription returned no native generation")
	}
	return core.NativeConversationSubscription{Generation: response.Generation, Events: events, Cancel: cancel}, nil
}

func (b *Backend) UpdateNativeConversationSettings(ctx context.Context, workspace core.Workspace, threadID string, generation uint64, patch core.NativeThreadSettingsPatch) (core.NativeThreadSettings, error) {
	var result core.NativeThreadSettings
	err := b.rpc(ctx, workspace, runtimeprotocol.MethodSettingsUpdate, runtimeprotocol.Resource{ConversationRef: threadID}, struct {
		Generation uint64                         `json:"generation"`
		Patch      core.NativeThreadSettingsPatch `json:"patch"`
	}{generation, patch}, &result)
	return result, err
}

func (b *Backend) StartNativeTurn(ctx context.Context, workspace core.Workspace, threadID string, generation uint64, request core.NativeTurnStartRequest) (core.NativeTurnResult, error) {
	var result core.NativeTurnResult
	input, err := b.stageInputs(ctx, workspace, request.Input)
	if err != nil {
		return result, err
	}
	request.Input = input
	err = b.rpc(ctx, workspace, runtimeprotocol.MethodTurnStart, runtimeprotocol.Resource{ConversationRef: threadID}, struct {
		Generation uint64                      `json:"generation"`
		Request    core.NativeTurnStartRequest `json:"request"`
	}{generation, request}, &result)
	return result, err
}

func (b *Backend) SteerNativeTurn(ctx context.Context, workspace core.Workspace, threadID string, generation uint64, expectedTurnID string, input []core.NativeUserInput) (core.NativeTurnResult, error) {
	var result core.NativeTurnResult
	stagedInput, err := b.stageInputs(ctx, workspace, input)
	if err != nil {
		return result, err
	}
	err = b.rpc(ctx, workspace, runtimeprotocol.MethodTurnSteer, runtimeprotocol.Resource{ConversationRef: threadID, TurnID: expectedTurnID}, struct {
		Generation uint64                 `json:"generation"`
		Input      []core.NativeUserInput `json:"input"`
	}{generation, stagedInput}, &result)
	return result, err
}

func (b *Backend) InterruptNativeTurn(ctx context.Context, workspace core.Workspace, threadID string, generation uint64, turnID string) error {
	return b.rpc(ctx, workspace, runtimeprotocol.MethodTurnInterrupt, runtimeprotocol.Resource{ConversationRef: threadID, TurnID: turnID}, struct {
		Generation uint64 `json:"generation"`
	}{generation}, nil)
}

func (b *Backend) RespondNativeInteraction(ctx context.Context, workspace core.Workspace, threadID string, generation uint64, requestID, response json.RawMessage) error {
	return b.rpc(ctx, workspace, runtimeprotocol.MethodInteractionReply, runtimeprotocol.Resource{ConversationRef: threadID}, struct {
		Generation uint64          `json:"generation"`
		RequestID  json.RawMessage `json:"request_id"`
		Response   json.RawMessage `json:"response"`
	}{generation, requestID, response}, nil)
}

func (b *Backend) StartNativeRealtime(ctx context.Context, workspace core.Workspace, threadID string, generation uint64, request core.NativeRealtimeStartRequest) error {
	return b.rpc(ctx, workspace, runtimeprotocol.MethodRealtimeStart, runtimeprotocol.Resource{ConversationRef: threadID}, struct {
		Generation uint64                          `json:"generation"`
		Request    core.NativeRealtimeStartRequest `json:"request"`
	}{generation, request}, nil)
}

func (b *Backend) AppendNativeRealtimeText(ctx context.Context, workspace core.Workspace, threadID string, generation uint64, text string) error {
	return b.rpc(ctx, workspace, runtimeprotocol.MethodRealtimeAppend, runtimeprotocol.Resource{ConversationRef: threadID}, struct {
		Generation uint64 `json:"generation"`
		Text       string `json:"text"`
	}{generation, text}, nil)
}

func (b *Backend) StopNativeRealtime(ctx context.Context, workspace core.Workspace, threadID string, generation uint64) error {
	return b.rpc(ctx, workspace, runtimeprotocol.MethodRealtimeStop, runtimeprotocol.Resource{ConversationRef: threadID}, struct {
		Generation uint64 `json:"generation"`
	}{generation}, nil)
}

func (b *Backend) rpc(ctx context.Context, workspace core.Workspace, method runtimeprotocol.Method, resource runtimeprotocol.Resource, request any, response any) error {
	resource.WorkspaceRef = workspace.Ref
	var payload json.RawMessage
	if request != nil {
		encoded, err := json.Marshal(request)
		if err != nil {
			return fmt.Errorf("remote native backend: encode %s request: %w", method, err)
		}
		payload = encoded
	}
	body, err := json.Marshal(runtimeprotocol.InternalRequest{DeviceID: workspace.DeviceID, Method: method, Resource: resource, Payload: payload})
	if err != nil {
		return err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, b.base+"/runtime/v1/rpc", bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpResponse, err := b.client.Do(httpRequest)
	if err != nil {
		return fmt.Errorf("remote native backend: call control: %w", err)
	}
	defer func() { _ = httpResponse.Body.Close() }()
	return decodeControlResponse(httpResponse, response)
}

func (b *Backend) stageInputs(ctx context.Context, workspace core.Workspace, input []core.NativeUserInput) ([]core.NativeUserInput, error) {
	result := append([]core.NativeUserInput(nil), input...)
	uploads := make([]runtimeprotocol.AttachmentUpload, 0)
	indexes := make([]int, 0)
	for index := range result {
		item := &result[index]
		switch strings.ToLower(strings.TrimSpace(item.Type)) {
		case "text":
			continue
		case "image", "file", "audio":
			if item.AttachmentRef != "" || item.LocalPath != "" || item.URL != "" || len(item.Data) == 0 {
				return nil, fmt.Errorf("remote native backend: media input %d is not verified in-memory attachment data", index)
			}
			uploads = append(uploads, runtimeprotocol.AttachmentUpload{
				Type: item.Type, MimeType: item.MimeType, FileName: item.FileName, Data: append([]byte(nil), item.Data...),
			})
			indexes = append(indexes, index)
		default:
			return nil, fmt.Errorf("remote native backend: unsupported input type %q", item.Type)
		}
	}
	if len(uploads) == 0 {
		return result, nil
	}
	body, err := json.Marshal(runtimeprotocol.AttachmentStageRequest{
		DeviceID: workspace.DeviceID, WorkspaceRef: workspace.Ref, Attachments: uploads,
	})
	if err != nil {
		return nil, fmt.Errorf("remote native backend: encode attachments: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, b.base+"/runtime/v1/attachments", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := b.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("remote native backend: stage attachments: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	var references []runtimeprotocol.AttachmentReference
	if err := decodeControlResponse(response, &references); err != nil {
		return nil, err
	}
	if len(references) != len(indexes) {
		return nil, errors.New("remote native backend: control returned an invalid attachment reference count")
	}
	for offset, index := range indexes {
		if references[offset].Ref == "" || references[offset].Type != strings.ToLower(strings.TrimSpace(result[index].Type)) {
			return nil, errors.New("remote native backend: control returned an invalid attachment reference")
		}
		result[index].AttachmentRef = references[offset].Ref
		result[index].Data = nil
		result[index].LocalPath = ""
		result[index].URL = ""
	}
	return result, nil
}

func (b *Backend) get(ctx context.Context, path string, response any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, b.base+path, nil)
	if err != nil {
		return err
	}
	result, err := b.client.Do(request)
	if err != nil {
		return fmt.Errorf("remote native backend: read control: %w", err)
	}
	defer func() { _ = result.Body.Close() }()
	return decodeControlResponse(result, response)
}

func decodeControlResponse(response *http.Response, target any) error {
	var envelope struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 50<<20))
	if err := decoder.Decode(&envelope); err != nil {
		return fmt.Errorf("remote native backend: decode control response: %w", err)
	}
	if !envelope.OK || response.StatusCode >= 400 {
		return fmt.Errorf("remote native backend: %s", envelope.Error)
	}
	if target == nil || len(envelope.Data) == 0 || bytes.Equal(envelope.Data, []byte("null")) {
		return nil
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		return fmt.Errorf("remote native backend: decode response data: %w", err)
	}
	return nil
}

func (b *Backend) streamEvents(ctx context.Context, workspaceRef, threadID string, events chan<- core.NativeEventEnvelope, ready chan<- error) {
	defer close(events)
	values := url.Values{"workspace_ref": []string{workspaceRef}, "thread_id": []string{threadID}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, b.base+"/runtime/v1/events?"+values.Encode(), nil)
	if err != nil {
		ready <- err
		return
	}
	response, err := b.client.Do(request)
	if err != nil {
		ready <- err
		return
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		ready <- fmt.Errorf("remote native backend: event stream status %s", response.Status)
		return
	}
	ready <- nil
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	for scanner.Scan() {
		envelope, err := runtimeprotocol.Decode(scanner.Bytes())
		if err != nil {
			return
		}
		if envelope.Method != runtimeprotocol.MethodNativeEvent || envelope.Resource.WorkspaceRef != workspaceRef || envelope.Resource.ConversationRef != threadID {
			continue
		}
		payload, err := runtimeprotocol.DecodePayload[runtimeprotocol.NativeEventPayload](envelope)
		if err != nil || payload.NativeConnectionGeneration == 0 || len(payload.Event) == 0 {
			return
		}
		var event core.NativeEventEnvelope
		if err := json.Unmarshal(payload.Event, &event); err != nil {
			return
		}
		event.ConnectionGeneration = payload.NativeConnectionGeneration
		event.RequestID = append(json.RawMessage(nil), payload.RequestID...)
		select {
		case events <- event:
		case <-ctx.Done():
			return
		}
	}
}

var _ core.WorkspaceCatalogProvider = (*Backend)(nil)
var _ core.WorkspaceDeviceCatalogProvider = (*Backend)(nil)
var _ core.WorkspaceAccessValidator = (*Backend)(nil)
var _ core.NativeConversationBackend = (*Backend)(nil)
var _ core.NativeConversationSettingsController = (*Backend)(nil)
var _ core.NativeConversationTurnController = (*Backend)(nil)
var _ core.NativeConversationRealtimeController = (*Backend)(nil)

func (b *Backend) HTTPClientForTests() *http.Client { return b.client }
