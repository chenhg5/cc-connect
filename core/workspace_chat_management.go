package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const workspaceWebClientID = "web:admin"

func (m *ManagementServer) workspaceChatService(w http.ResponseWriter) *WorkspaceChatService {
	if m.workspaceChat == nil {
		mgmtError(w, http.StatusServiceUnavailable, ErrWorkspaceChatNotConfigured.Error())
		return nil
	}
	return m.workspaceChat
}

func (m *ManagementServer) handleWorkspaceChatWorkspaces(w http.ResponseWriter, r *http.Request) {
	service := m.workspaceChatService(w)
	if service == nil {
		return
	}
	if r.Method != http.MethodGet {
		mgmtError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	workspaces, err := service.ListWorkspaces(r.Context())
	if err != nil {
		writeWorkspaceChatError(w, err)
		return
	}
	mgmtJSON(w, http.StatusOK, map[string]any{"workspaces": workspaces})
}

func (m *ManagementServer) handleWorkspaceChatWorkspaceRoutes(w http.ResponseWriter, r *http.Request) {
	service := m.workspaceChatService(w)
	if service == nil {
		return
	}
	prefix := "/api/v1/chat/workspaces/"
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, prefix), "/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		mgmtError(w, http.StatusNotFound, "workspace chat route not found")
		return
	}
	workspaceRef, err := url.PathUnescape(parts[0])
	if err != nil || strings.TrimSpace(workspaceRef) == "" {
		mgmtError(w, http.StatusBadRequest, "invalid workspace reference")
		return
	}
	switch parts[1] {
	case "threads":
		m.handleWorkspaceChatThreadRoutes(service, workspaceRef, parts[2:], w, r)
	case "drafts":
		m.handleWorkspaceChatDraftRoutes(service, workspaceRef, parts[2:], w, r)
	case "runtime-catalog":
		if len(parts) != 2 || r.Method != http.MethodGet {
			mgmtError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		catalog, err := service.RuntimeCatalog(r.Context(), workspaceRef)
		if err != nil {
			writeWorkspaceChatError(w, err)
			return
		}
		mgmtJSON(w, http.StatusOK, catalog)
	default:
		mgmtError(w, http.StatusNotFound, "workspace chat route not found")
	}
}

func (m *ManagementServer) handleWorkspaceChatDraftRoutes(service *WorkspaceChatService, workspaceRef string, tail []string, w http.ResponseWriter, r *http.Request) {
	if len(tail) == 0 {
		if r.Method != http.MethodPost {
			mgmtError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		if err := decodeStrictJSON(r.Body, &struct{}{}); err != nil && !errors.Is(err, io.EOF) {
			mgmtError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
		draft, err := service.CreateDraft(r.Context(), workspaceWebClientID, workspaceRef)
		if err != nil {
			writeWorkspaceChatError(w, err)
			return
		}
		mgmtJSON(w, http.StatusCreated, draft)
		return
	}
	if len(tail) < 1 || len(tail) > 2 {
		mgmtError(w, http.StatusNotFound, "workspace chat draft route not found")
		return
	}
	draftID, err := url.PathUnescape(tail[0])
	if err != nil || strings.TrimSpace(draftID) == "" {
		mgmtError(w, http.StatusBadRequest, "invalid draft reference")
		return
	}
	if len(tail) == 1 {
		if r.Method != http.MethodGet {
			mgmtError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		draft, err := service.ReadDraft(r.Context(), workspaceWebClientID, workspaceRef, draftID)
		if err != nil {
			writeWorkspaceChatError(w, err)
			return
		}
		mgmtJSON(w, http.StatusOK, draft)
		return
	}
	if tail[1] != "settings" || r.Method != http.MethodPatch {
		mgmtError(w, http.StatusMethodNotAllowed, "PATCH settings only")
		return
	}
	var patch NativeThreadSettingsPatch
	if err := decodeStrictJSON(r.Body, &patch); err != nil {
		mgmtError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	draft, err := service.UpdateDraftSettings(r.Context(), workspaceWebClientID, workspaceRef, draftID, patch)
	if err != nil {
		writeWorkspaceChatError(w, err)
		return
	}
	mgmtJSON(w, http.StatusOK, draft)
}

func (m *ManagementServer) handleWorkspaceChatThreadRoutes(service *WorkspaceChatService, workspaceRef string, tail []string, w http.ResponseWriter, r *http.Request) {
	if len(tail) == 0 {
		if r.Method != http.MethodGet {
			mgmtError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		page, err := workspaceChatPageRequest(r)
		if err != nil {
			mgmtError(w, http.StatusBadRequest, err.Error())
			return
		}
		threads, err := service.ListThreads(r.Context(), workspaceRef, page)
		if err != nil {
			writeWorkspaceChatError(w, err)
			return
		}
		mgmtJSON(w, http.StatusOK, threads)
		return
	}
	threadID, err := url.PathUnescape(tail[0])
	if err != nil || strings.TrimSpace(threadID) == "" {
		mgmtError(w, http.StatusBadRequest, "invalid thread id")
		return
	}
	if len(tail) == 1 {
		if r.Method != http.MethodGet {
			mgmtError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		snapshot, err := service.ReadThread(r.Context(), workspaceRef, threadID)
		if err != nil {
			writeWorkspaceChatError(w, err)
			return
		}
		mgmtJSON(w, http.StatusOK, snapshot)
		return
	}
	if len(tail) != 2 {
		mgmtError(w, http.StatusNotFound, "workspace chat route not found")
		return
	}
	switch tail[1] {
	case "turns":
		if r.Method != http.MethodGet {
			mgmtError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		page, err := workspaceChatPageRequest(r)
		if err != nil {
			mgmtError(w, http.StatusBadRequest, err.Error())
			return
		}
		turns, err := service.ListTurns(r.Context(), workspaceRef, threadID, page)
		if err != nil {
			writeWorkspaceChatError(w, err)
			return
		}
		mgmtJSON(w, http.StatusOK, turns)
	case "items":
		if r.Method != http.MethodGet {
			mgmtError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		page, err := workspaceChatPageRequest(r)
		if err != nil {
			mgmtError(w, http.StatusBadRequest, err.Error())
			return
		}
		items, err := service.ListItems(r.Context(), workspaceRef, threadID, r.URL.Query().Get("turn_id"), page)
		if err != nil {
			writeWorkspaceChatError(w, err)
			return
		}
		mgmtJSON(w, http.StatusOK, items)
	case "settings":
		switch r.Method {
		case http.MethodGet:
			snapshot, err := service.ReadThread(r.Context(), workspaceRef, threadID)
			if err != nil {
				writeWorkspaceChatError(w, err)
				return
			}
			mgmtJSON(w, http.StatusOK, snapshot.Settings)
		case http.MethodPatch:
			var patch NativeThreadSettingsPatch
			if err := decodeStrictJSON(r.Body, &patch); err != nil {
				mgmtError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
				return
			}
			if emptyNativeSettingsPatch(patch) {
				mgmtError(w, http.StatusBadRequest, "settings patch is empty")
				return
			}
			settings, err := service.UpdateSettings(r.Context(), workspaceRef, threadID, patch)
			if err != nil {
				writeWorkspaceChatError(w, err)
				return
			}
			mgmtJSON(w, http.StatusOK, settings)
		default:
			mgmtError(w, http.StatusMethodNotAllowed, "GET or PATCH only")
		}
	default:
		mgmtError(w, http.StatusNotFound, "workspace chat route not found")
	}
}

func (m *ManagementServer) handleWorkspaceChatSelection(w http.ResponseWriter, r *http.Request) {
	service := m.workspaceChatService(w)
	if service == nil {
		return
	}
	switch r.Method {
	case http.MethodGet:
		selection, err := service.Selection(r.Context(), workspaceWebClientID)
		if err != nil {
			writeWorkspaceChatError(w, err)
			return
		}
		mgmtJSON(w, http.StatusOK, selection)
	case http.MethodPut:
		var body struct {
			WorkspaceRef string          `json:"workspace_ref"`
			Conversation ConversationRef `json:"conversation"`
		}
		if err := decodeStrictJSON(r.Body, &body); err != nil {
			mgmtError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
		selection, err := service.SelectConversation(r.Context(), workspaceWebClientID, body.WorkspaceRef, body.Conversation)
		if err != nil {
			writeWorkspaceChatError(w, err)
			return
		}
		mgmtJSON(w, http.StatusOK, selection)
	default:
		mgmtError(w, http.StatusMethodNotAllowed, "GET or PUT only")
	}
}

type workspaceChatWSRequestBase struct {
	Type         string          `json:"type"`
	RequestID    string          `json:"request_id"`
	WorkspaceRef string          `json:"workspace_ref"`
	Conversation ConversationRef `json:"conversation"`
}

type workspaceChatWSSubscribeRequest struct {
	workspaceChatWSRequestBase
	AfterEpoch    *string `json:"after_epoch,omitempty"`
	AfterSequence *uint64 `json:"after_sequence,omitempty"`
}

type workspaceChatPublicInput struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func nativeTextInputs(input []workspaceChatPublicInput) []NativeUserInput {
	result := make([]NativeUserInput, 0, len(input))
	for _, item := range input {
		result = append(result, NativeUserInput{Type: item.Type, Text: item.Text})
	}
	return result
}

type workspaceChatWSTurnStartRequest struct {
	workspaceChatWSRequestBase
	Input    []workspaceChatPublicInput `json:"input"`
	Payload  json.RawMessage            `json:"payload,omitempty"`
	Settings NativeThreadSettingsPatch  `json:"-"`
}

type workspaceChatWSTurnSteerRequest struct {
	workspaceChatWSRequestBase
	Input          []workspaceChatPublicInput `json:"input"`
	ExpectedTurnID string                     `json:"expected_turn_id"`
}

type workspaceChatWSTurnInterruptRequest struct {
	workspaceChatWSRequestBase
	ExpectedTurnID string `json:"expected_turn_id"`
}

type workspaceChatWSInteractionResponseRequest struct {
	workspaceChatWSRequestBase
	InteractionID string          `json:"interaction_id"`
	Response      json.RawMessage `json:"response"`
}

type workspaceChatWSRealtimeStartRequest struct {
	workspaceChatWSRequestBase
	SDP     string `json:"sdp"`
	Voice   string `json:"voice,omitempty"`
	Version string `json:"version,omitempty"`
}

type workspaceChatWSRealtimeAppendTextRequest struct {
	workspaceChatWSRequestBase
	Text string `json:"text"`
}

type workspaceChatWSRealtimeStopRequest struct {
	workspaceChatWSRequestBase
}

func decodeWorkspaceChatWSRequest(raw []byte) (any, workspaceChatWSRequestBase, error) {
	base, err := inspectWorkspaceChatWSRequest(raw)
	if err != nil {
		return nil, base, err
	}
	switch base.Type {
	case "subscribe":
		var request workspaceChatWSSubscribeRequest
		if err := decodeStrictJSON(bytes.NewReader(raw), &request); err != nil {
			return nil, base, err
		}
		if err := validateWorkspaceChatWSBase(request.workspaceChatWSRequestBase, false); err != nil {
			return nil, base, err
		}
		if (request.AfterEpoch == nil) != (request.AfterSequence == nil) {
			return nil, base, fmt.Errorf("after_epoch and after_sequence must be provided together")
		}
		if request.AfterEpoch != nil && strings.TrimSpace(*request.AfterEpoch) == "" {
			return nil, base, fmt.Errorf("after_epoch must not be empty")
		}
		return request, base, nil
	case "turn_start":
		var request workspaceChatWSTurnStartRequest
		if err := decodeStrictJSON(bytes.NewReader(raw), &request); err != nil {
			return nil, base, err
		}
		if err := validateWorkspaceChatWSBase(request.workspaceChatWSRequestBase, false); err != nil {
			return nil, base, err
		}
		if len(request.Input) == 0 {
			return nil, base, fmt.Errorf("input is required")
		}
		if len(request.Payload) > 0 {
			if request.Conversation.Kind != ConversationKindDraft {
				return nil, base, fmt.Errorf("turn_start payload.settings is only valid for a draft")
			}
			var payload struct {
				Settings *NativeThreadSettingsPatch `json:"settings"`
			}
			if err := decodeStrictJSON(bytes.NewReader(request.Payload), &payload); err != nil {
				return nil, base, fmt.Errorf("payload: %w", err)
			}
			if payload.Settings == nil {
				return nil, base, fmt.Errorf("payload.settings is required")
			}
			request.Settings = *payload.Settings
		}
		return request, base, nil
	case "turn_steer":
		var request workspaceChatWSTurnSteerRequest
		if err := decodeStrictJSON(bytes.NewReader(raw), &request); err != nil {
			return nil, base, err
		}
		if err := validateWorkspaceChatWSBase(request.workspaceChatWSRequestBase, true); err != nil {
			return nil, base, err
		}
		if len(request.Input) == 0 {
			return nil, base, fmt.Errorf("input is required")
		}
		if strings.TrimSpace(request.ExpectedTurnID) == "" {
			return nil, base, fmt.Errorf("expected_turn_id is required")
		}
		return request, base, nil
	case "turn_interrupt":
		var request workspaceChatWSTurnInterruptRequest
		if err := decodeStrictJSON(bytes.NewReader(raw), &request); err != nil {
			return nil, base, err
		}
		if err := validateWorkspaceChatWSBase(request.workspaceChatWSRequestBase, true); err != nil {
			return nil, base, err
		}
		if strings.TrimSpace(request.ExpectedTurnID) == "" {
			return nil, base, fmt.Errorf("expected_turn_id is required")
		}
		return request, base, nil
	case "interaction_response":
		var request workspaceChatWSInteractionResponseRequest
		if err := decodeStrictJSON(bytes.NewReader(raw), &request); err != nil {
			return nil, base, err
		}
		if err := validateWorkspaceChatWSBase(request.workspaceChatWSRequestBase, true); err != nil {
			return nil, base, err
		}
		if strings.TrimSpace(request.InteractionID) == "" {
			return nil, base, fmt.Errorf("interaction_id is required")
		}
		if len(request.Response) == 0 {
			return nil, base, fmt.Errorf("response is required")
		}
		return request, base, nil
	case "realtime_start":
		var request workspaceChatWSRealtimeStartRequest
		if err := decodeStrictJSON(bytes.NewReader(raw), &request); err != nil {
			return nil, base, err
		}
		if err := validateWorkspaceChatWSBase(request.workspaceChatWSRequestBase, true); err != nil {
			return nil, base, err
		}
		if strings.TrimSpace(request.SDP) == "" {
			return nil, base, fmt.Errorf("sdp is required")
		}
		return request, base, nil
	case "realtime_append_text":
		var request workspaceChatWSRealtimeAppendTextRequest
		if err := decodeStrictJSON(bytes.NewReader(raw), &request); err != nil {
			return nil, base, err
		}
		if err := validateWorkspaceChatWSBase(request.workspaceChatWSRequestBase, true); err != nil {
			return nil, base, err
		}
		if strings.TrimSpace(request.Text) == "" {
			return nil, base, fmt.Errorf("text is required")
		}
		return request, base, nil
	case "realtime_stop":
		var request workspaceChatWSRealtimeStopRequest
		if err := decodeStrictJSON(bytes.NewReader(raw), &request); err != nil {
			return nil, base, err
		}
		if err := validateWorkspaceChatWSBase(request.workspaceChatWSRequestBase, true); err != nil {
			return nil, base, err
		}
		return request, base, nil
	default:
		return nil, base, fmt.Errorf("unsupported message type %q", base.Type)
	}
}

func inspectWorkspaceChatWSRequest(raw []byte) (workspaceChatWSRequestBase, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return workspaceChatWSRequestBase{}, err
	}
	if fields == nil {
		return workspaceChatWSRequestBase{}, fmt.Errorf("request must be a JSON object")
	}
	var base workspaceChatWSRequestBase
	if value := fields["type"]; len(value) > 0 {
		if err := json.Unmarshal(value, &base.Type); err != nil {
			return base, fmt.Errorf("type must be a string")
		}
	}
	if value := fields["request_id"]; len(value) > 0 {
		_ = json.Unmarshal(value, &base.RequestID)
	}
	if value := fields["workspace_ref"]; len(value) > 0 {
		_ = json.Unmarshal(value, &base.WorkspaceRef)
	}
	if value := fields["conversation"]; len(value) > 0 {
		_ = json.Unmarshal(value, &base.Conversation)
	}
	if strings.TrimSpace(base.Type) == "" {
		return base, fmt.Errorf("type is required")
	}
	return base, nil
}

func validateWorkspaceChatWSBase(base workspaceChatWSRequestBase, requireThread bool) error {
	if strings.TrimSpace(base.RequestID) == "" {
		return fmt.Errorf("request_id is required")
	}
	if strings.TrimSpace(base.WorkspaceRef) == "" {
		return fmt.Errorf("workspace_ref is required")
	}
	if strings.TrimSpace(base.Conversation.ID) == "" {
		return fmt.Errorf("conversation.id is required")
	}
	if base.Conversation.Kind != ConversationKindDraft && base.Conversation.Kind != ConversationKindThread {
		return fmt.Errorf("conversation.kind must be draft or thread")
	}
	if requireThread && base.Conversation.Kind != ConversationKindThread {
		return fmt.Errorf("%s requires a materialized thread", base.Type)
	}
	return nil
}

func (m *ManagementServer) handleWorkspaceChatWS(w http.ResponseWriter, r *http.Request) {
	service := m.workspaceChatService(w)
	if service == nil {
		return
	}
	if r.Method != http.MethodGet {
		mgmtError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	// 该 handler 只监听 control 可访问的私有 Unix Socket；浏览器 Origin 与
	// CSRF 已由 control 在代理升级前校验。
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() {
		if err := conn.Close(); err != nil {
			slog.Debug("workspace chat websocket close failed", "error", err)
		}
	}()
	var writeMu sync.Mutex
	write := func(value any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteJSON(value)
	}
	var subscription WorkspaceChatSubscription
	var subscriptionWriterDone <-chan struct{}
	var subscribed bool
	var subscribedTarget workspaceChatWSRequestBase
	realtimeOwner := newWorkspaceChatID("realtime")
	var realtimeMu sync.Mutex
	var realtimeWorkspace, realtimeThread string
	ownsRealtime := func(request workspaceChatWSRequestBase) bool {
		realtimeMu.Lock()
		defer realtimeMu.Unlock()
		return realtimeThread != "" && request.Conversation.Kind == ConversationKindThread &&
			realtimeWorkspace == request.WorkspaceRef && realtimeThread == request.Conversation.ID
	}
	stopOwnedRealtime := func(ctx context.Context) error {
		realtimeMu.Lock()
		if realtimeThread == "" {
			realtimeMu.Unlock()
			return nil
		}
		workspaceRef, threadID := realtimeWorkspace, realtimeThread
		realtimeMu.Unlock()
		if err := service.StopRealtime(ctx, realtimeOwner, workspaceRef, threadID); err != nil {
			return err
		}
		realtimeMu.Lock()
		if realtimeWorkspace == workspaceRef && realtimeThread == threadID {
			realtimeWorkspace, realtimeThread = "", ""
		}
		realtimeMu.Unlock()
		return nil
	}
	clearOwnedRealtime := func(workspaceRef, threadID string) {
		realtimeMu.Lock()
		if realtimeWorkspace == workspaceRef && realtimeThread == threadID {
			realtimeWorkspace, realtimeThread = "", ""
		}
		realtimeMu.Unlock()
	}
	stopSubscription := func() {
		if !subscribed {
			return
		}
		subscription.Cancel()
		if subscriptionWriterDone != nil {
			<-subscriptionWriterDone
		}
		subscribed = false
		subscribedTarget = workspaceChatWSRequestBase{}
		subscriptionWriterDone = nil
	}
	writeRequestError := func(request workspaceChatWSRequestBase, message string) {
		if subscribed && request.WorkspaceRef == subscribedTarget.WorkspaceRef && request.Conversation == subscribedTarget.Conversation {
			if err := service.PublishOperationError(r.Context(), workspaceWebClientID, request.WorkspaceRef, request.Conversation, request.RequestID, message); err == nil {
				return
			}
		}
		_ = write(workspaceChatWSProtocolError(request, message))
	}
	defer func() {
		stopSubscription()
		realtimeMu.Lock()
		hasRealtime := realtimeThread != ""
		realtimeMu.Unlock()
		if hasRealtime {
			ctx, cancel := contextWithTimeoutWithoutRequest(5 * time.Second)
			_ = stopOwnedRealtime(ctx)
			cancel()
		}
	}()

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		decoded, errorContext, err := decodeWorkspaceChatWSRequest(raw)
		if err != nil {
			writeRequestError(errorContext, "invalid request: "+err.Error())
			continue
		}
		switch request := decoded.(type) {
		case workspaceChatWSSubscribeRequest:
			realtimeMu.Lock()
			hasRealtime := realtimeThread != ""
			realtimeMu.Unlock()
			if hasRealtime && !ownsRealtime(request.workspaceChatWSRequestBase) {
				if err := stopOwnedRealtime(r.Context()); err != nil {
					writeRequestError(request.workspaceChatWSRequestBase, "stop current realtime before switching conversation: "+err.Error())
					continue
				}
			}
			stopSubscription()
			var afterEpoch string
			var afterSequence uint64
			if request.AfterEpoch != nil {
				afterEpoch = *request.AfterEpoch
				afterSequence = *request.AfterSequence
			}
			next, err := service.Subscribe(r.Context(), workspaceWebClientID, request.WorkspaceRef, request.Conversation, afterEpoch, afterSequence)
			if err != nil {
				writeRequestError(request.workspaceChatWSRequestBase, err.Error())
				continue
			}
			subscription = next
			subscribed = true
			subscribedTarget = request.workspaceChatWSRequestBase
			failed := false
			for _, event := range next.Initial {
				if err := write(event); err != nil {
					failed = true
					break
				}
			}
			if failed {
				return
			}
			writerDone := make(chan struct{})
			subscriptionWriterDone = writerDone
			go func(current WorkspaceChatSubscription) {
				defer close(writerDone)
				for event := range current.Events {
					if workspaceChatEventClosesRealtime(event) {
						clearOwnedRealtime(event.WorkspaceRef, event.ThreadID)
					}
					if err := write(event); err != nil {
						return
					}
				}
			}(next)
		case workspaceChatWSTurnStartRequest:
			if _, err := service.StartTurn(r.Context(), workspaceWebClientID, request.RequestID, request.WorkspaceRef, request.Conversation, nativeTextInputs(request.Input), request.Settings); err != nil {
				writeRequestError(request.workspaceChatWSRequestBase, err.Error())
			}
		case workspaceChatWSTurnSteerRequest:
			if _, err := service.SteerTurn(r.Context(), workspaceWebClientID, request.RequestID, request.WorkspaceRef, request.Conversation.ID, request.ExpectedTurnID, nativeTextInputs(request.Input)); err != nil {
				writeRequestError(request.workspaceChatWSRequestBase, err.Error())
			}
		case workspaceChatWSTurnInterruptRequest:
			if err := service.InterruptTurn(r.Context(), request.WorkspaceRef, request.Conversation.ID, request.ExpectedTurnID); err != nil {
				writeRequestError(request.workspaceChatWSRequestBase, err.Error())
			}
		case workspaceChatWSInteractionResponseRequest:
			if err := service.RespondInteraction(r.Context(), request.WorkspaceRef, request.Conversation.ID, request.InteractionID, request.Response); err != nil {
				writeRequestError(request.workspaceChatWSRequestBase, err.Error())
			}
		case workspaceChatWSRealtimeStartRequest:
			realtimeMu.Lock()
			hasRealtime := realtimeThread != ""
			realtimeMu.Unlock()
			if hasRealtime {
				writeRequestError(request.workspaceChatWSRequestBase, "this WebSocket connection already owns an active realtime conversation")
				continue
			}
			if err := service.StartRealtime(r.Context(), realtimeOwner, request.WorkspaceRef, request.Conversation.ID, NativeRealtimeStartRequest{SDP: request.SDP, Voice: request.Voice, Version: request.Version}); err != nil {
				writeRequestError(request.workspaceChatWSRequestBase, err.Error())
				continue
			}
			realtimeMu.Lock()
			realtimeWorkspace, realtimeThread = request.WorkspaceRef, request.Conversation.ID
			realtimeMu.Unlock()
		case workspaceChatWSRealtimeAppendTextRequest:
			if !ownsRealtime(request.workspaceChatWSRequestBase) {
				writeRequestError(request.workspaceChatWSRequestBase, "this WebSocket connection does not own realtime for the requested conversation")
				continue
			}
			if err := service.AppendRealtimeText(r.Context(), realtimeOwner, request.WorkspaceRef, request.Conversation.ID, request.Text); err != nil {
				writeRequestError(request.workspaceChatWSRequestBase, err.Error())
			}
		case workspaceChatWSRealtimeStopRequest:
			if !ownsRealtime(request.workspaceChatWSRequestBase) {
				writeRequestError(request.workspaceChatWSRequestBase, "this WebSocket connection does not own realtime for the requested conversation")
				continue
			}
			if err := stopOwnedRealtime(r.Context()); err != nil {
				writeRequestError(request.workspaceChatWSRequestBase, err.Error())
				continue
			}
		default:
			writeRequestError(errorContext, "invalid decoded request")
		}
	}
}

func workspaceChatEventClosesRealtime(event WorkspaceChatEvent) bool {
	if event.Type != "native_event" {
		return false
	}
	var envelope NativeEventEnvelope
	if json.Unmarshal(event.Payload, &envelope) != nil {
		return false
	}
	return envelope.Method == "thread/realtime/closed" || envelope.Method == "thread/realtime/error"
}

// protocol_error 是唯一不属于 actor stream 的服务端事件，只用于尚未订阅或
// 无法验证 conversation 的协议错误；客户端不得用它更新 stream cursor。
func workspaceChatWSProtocolError(request workspaceChatWSRequestBase, message string) WorkspaceChatEvent {
	return WorkspaceChatEvent{
		Type: "protocol_error", WorkspaceRef: request.WorkspaceRef, Conversation: request.Conversation,
		ThreadID: threadIDFromConversation(request.Conversation), RequestID: request.RequestID,
		Error: message, OccurredAt: time.Now(),
	}
}

func threadIDFromConversation(conversation ConversationRef) string {
	if conversation.Kind == ConversationKindThread {
		return conversation.ID
	}
	return ""
}

func workspaceChatPageRequest(r *http.Request) (NativePageRequest, error) {
	request := NativePageRequest{Cursor: r.URL.Query().Get("cursor"), SortDirection: r.URL.Query().Get("sort_direction")}
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil {
			return NativePageRequest{}, fmt.Errorf("limit must be an integer")
		}
		request.Limit = limit
	}
	return normalizeNativePage(request)
}

func decodeStrictJSON(reader io.Reader, destination any) error {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func writeWorkspaceChatError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, ErrWorkspaceChatNotConfigured), errors.Is(err, ErrWorkspaceChatClosed):
		status = http.StatusServiceUnavailable
	case errors.Is(err, ErrWorkspaceNotFound), errors.Is(err, ErrNativeThreadNotFound):
		status = http.StatusNotFound
	case errors.Is(err, ErrWorkspaceTurnRunning), errors.Is(err, ErrWorkspaceTurnNotRunning), errors.Is(err, ErrWorkspaceStaleTurn), errors.Is(err, ErrWorkspaceInteractionStale), errors.Is(err, ErrWorkspaceDraftMaterialized), errors.Is(err, ErrWorkspaceDraftMaterializationUncertain):
		status = http.StatusConflict
	case errors.Is(err, ErrNativeCapabilityUnavailable):
		status = http.StatusNotImplemented
	}
	mgmtError(w, status, err.Error())
}

func contextWithTimeoutWithoutRequest(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), timeout)
}
