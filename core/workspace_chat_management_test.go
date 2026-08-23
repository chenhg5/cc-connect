package core

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type workspaceChatManagementEnvelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error string          `json:"error"`
}

func TestDecodeWorkspaceChatWSRequestAcceptsOnlyEightCurrentShapes(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "subscribe", raw: `{"type":"subscribe","request_id":"r1","workspace_ref":"workspace-a","conversation":{"kind":"draft","id":"draft-a"},"after_epoch":"epoch-a","after_sequence":0}`},
		{name: "turn_start", raw: `{"type":"turn_start","request_id":"r2","workspace_ref":"workspace-a","conversation":{"kind":"draft","id":"draft-a"},"input":[{"type":"text","text":"hello"}],"payload":{"settings":{"mode":"plan"}}}`},
		{name: "turn_steer", raw: `{"type":"turn_steer","request_id":"r3","workspace_ref":"workspace-a","conversation":{"kind":"thread","id":"thread-a"},"input":[{"type":"text","text":"more"}],"expected_turn_id":"turn-a"}`},
		{name: "turn_interrupt", raw: `{"type":"turn_interrupt","request_id":"r4","workspace_ref":"workspace-a","conversation":{"kind":"thread","id":"thread-a"},"expected_turn_id":"turn-a"}`},
		{name: "interaction_response", raw: `{"type":"interaction_response","request_id":"r5","workspace_ref":"workspace-a","conversation":{"kind":"thread","id":"thread-a"},"interaction_id":"interaction-a","response":{"decision":"allow"}}`},
		{name: "realtime_start", raw: `{"type":"realtime_start","request_id":"r6","workspace_ref":"workspace-a","conversation":{"kind":"thread","id":"thread-a"},"sdp":"v=0","voice":"alloy","version":"v2"}`},
		{name: "realtime_append_text", raw: `{"type":"realtime_append_text","request_id":"r7","workspace_ref":"workspace-a","conversation":{"kind":"thread","id":"thread-a"},"text":"hello"}`},
		{name: "realtime_stop", raw: `{"type":"realtime_stop","request_id":"r8","workspace_ref":"workspace-a","conversation":{"kind":"thread","id":"thread-a"}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decoded, base, err := decodeWorkspaceChatWSRequest([]byte(test.raw))
			if err != nil {
				t.Fatalf("decodeWorkspaceChatWSRequest() error = %v", err)
			}
			if decoded == nil || base.Type != test.name || base.RequestID == "" {
				t.Fatalf("decoded request = %T, base = %#v", decoded, base)
			}
		})
	}
}

func TestDecodeWorkspaceChatWSRequestRejectsFieldsFromOtherTypes(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		unexpected string
	}{
		{name: "subscribe rejects input", raw: `{"type":"subscribe","request_id":"r1","workspace_ref":"workspace-a","conversation":{"kind":"draft","id":"draft-a"},"input":[]}`, unexpected: "input"},
		{name: "turn_start rejects cursor", raw: `{"type":"turn_start","request_id":"r2","workspace_ref":"workspace-a","conversation":{"kind":"draft","id":"draft-a"},"input":[{"type":"text","text":"hello"}],"after_sequence":2}`, unexpected: "after_sequence"},
		{name: "turn_steer rejects settings payload", raw: `{"type":"turn_steer","request_id":"r3","workspace_ref":"workspace-a","conversation":{"kind":"thread","id":"thread-a"},"input":[{"type":"text","text":"more"}],"expected_turn_id":"turn-a","payload":{"settings":{}}}`, unexpected: "payload"},
		{name: "turn_interrupt rejects input", raw: `{"type":"turn_interrupt","request_id":"r4","workspace_ref":"workspace-a","conversation":{"kind":"thread","id":"thread-a"},"expected_turn_id":"turn-a","input":[]}`, unexpected: "input"},
		{name: "interaction_response rejects expected turn", raw: `{"type":"interaction_response","request_id":"r5","workspace_ref":"workspace-a","conversation":{"kind":"thread","id":"thread-a"},"interaction_id":"interaction-a","response":{"decision":"allow"},"expected_turn_id":"turn-a"}`, unexpected: "expected_turn_id"},
		{name: "realtime_start rejects text", raw: `{"type":"realtime_start","request_id":"r6","workspace_ref":"workspace-a","conversation":{"kind":"thread","id":"thread-a"},"sdp":"v=0","text":"hello"}`, unexpected: "text"},
		{name: "realtime_append_text rejects sdp", raw: `{"type":"realtime_append_text","request_id":"r7","workspace_ref":"workspace-a","conversation":{"kind":"thread","id":"thread-a"},"text":"hello","sdp":"v=0"}`, unexpected: "sdp"},
		{name: "realtime_stop rejects voice", raw: `{"type":"realtime_stop","request_id":"r8","workspace_ref":"workspace-a","conversation":{"kind":"thread","id":"thread-a"},"voice":"alloy"}`, unexpected: "voice"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := decodeWorkspaceChatWSRequest([]byte(test.raw))
			if err == nil || !strings.Contains(err.Error(), `unknown field "`+test.unexpected+`"`) {
				t.Fatalf("decodeWorkspaceChatWSRequest() error = %v", err)
			}
		})
	}
}

func TestDecodeWorkspaceChatWSRequestRejectsMissingRequiredFields(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "subscribe conversation", raw: `{"type":"subscribe","request_id":"r1","workspace_ref":"workspace-a"}`, want: "conversation.id is required"},
		{name: "turn_start input", raw: `{"type":"turn_start","request_id":"r2","workspace_ref":"workspace-a","conversation":{"kind":"draft","id":"draft-a"}}`, want: "input is required"},
		{name: "turn_steer expected turn", raw: `{"type":"turn_steer","request_id":"r3","workspace_ref":"workspace-a","conversation":{"kind":"thread","id":"thread-a"},"input":[{"type":"text","text":"more"}]}`, want: "expected_turn_id is required"},
		{name: "turn_interrupt expected turn", raw: `{"type":"turn_interrupt","request_id":"r4","workspace_ref":"workspace-a","conversation":{"kind":"thread","id":"thread-a"}}`, want: "expected_turn_id is required"},
		{name: "interaction_response response", raw: `{"type":"interaction_response","request_id":"r5","workspace_ref":"workspace-a","conversation":{"kind":"thread","id":"thread-a"},"interaction_id":"interaction-a"}`, want: "response is required"},
		{name: "realtime_start sdp", raw: `{"type":"realtime_start","request_id":"r6","workspace_ref":"workspace-a","conversation":{"kind":"thread","id":"thread-a"}}`, want: "sdp is required"},
		{name: "realtime_append_text text", raw: `{"type":"realtime_append_text","request_id":"r7","workspace_ref":"workspace-a","conversation":{"kind":"thread","id":"thread-a"}}`, want: "text is required"},
		{name: "realtime_stop request id", raw: `{"type":"realtime_stop","workspace_ref":"workspace-a","conversation":{"kind":"thread","id":"thread-a"}}`, want: "request_id is required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := decodeWorkspaceChatWSRequest([]byte(test.raw))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("decodeWorkspaceChatWSRequest() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDecodeWorkspaceChatWSRequestRejectsNestedUnknownFieldsAndInvalidCursor(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "conversation", raw: `{"type":"subscribe","request_id":"r1","workspace_ref":"workspace-a","conversation":{"kind":"draft","id":"draft-a","cwd":"/tmp"}}`, want: `unknown field "cwd"`},
		{name: "input", raw: `{"type":"turn_start","request_id":"r2","workspace_ref":"workspace-a","conversation":{"kind":"draft","id":"draft-a"},"input":[{"type":"text","text":"hello","local_path":"/tmp/file"}]}`, want: `unknown field "local_path"`},
		{name: "input URL", raw: `{"type":"turn_start","request_id":"r2b","workspace_ref":"workspace-a","conversation":{"kind":"draft","id":"draft-a"},"input":[{"type":"text","text":"hello","url":"https://example.com/image.png"}]}`, want: `unknown field "url"`},
		{name: "input detail", raw: `{"type":"turn_start","request_id":"r2c","workspace_ref":"workspace-a","conversation":{"kind":"draft","id":"draft-a"},"input":[{"type":"text","text":"hello","detail":"high"}]}`, want: `unknown field "detail"`},
		{name: "settings", raw: `{"type":"turn_start","request_id":"r3","workspace_ref":"workspace-a","conversation":{"kind":"draft","id":"draft-a"},"input":[{"type":"text","text":"hello"}],"payload":{"settings":{"mode":"plan","cwd":"/tmp"}}}`, want: `unknown field "cwd"`},
		{name: "payload settings required", raw: `{"type":"turn_start","request_id":"r4","workspace_ref":"workspace-a","conversation":{"kind":"draft","id":"draft-a"},"input":[{"type":"text","text":"hello"}],"payload":{}}`, want: "payload.settings is required"},
		{name: "thread settings", raw: `{"type":"turn_start","request_id":"r4b","workspace_ref":"workspace-a","conversation":{"kind":"thread","id":"thread-a"},"input":[{"type":"text","text":"hello"}],"payload":{"settings":{"effort":"high"}}}`, want: "only valid for a draft"},
		{name: "cursor pair", raw: `{"type":"subscribe","request_id":"r5","workspace_ref":"workspace-a","conversation":{"kind":"draft","id":"draft-a"},"after_epoch":"epoch-a"}`, want: "after_epoch and after_sequence must be provided together"},
		{name: "thread only", raw: `{"type":"realtime_stop","request_id":"r6","workspace_ref":"workspace-a","conversation":{"kind":"draft","id":"draft-a"}}`, want: "requires a materialized thread"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := decodeWorkspaceChatWSRequest([]byte(test.raw))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("decodeWorkspaceChatWSRequest() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestWorkspaceChatManagementRESTUsesOnlyNativeConversationResources(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	management := NewManagementServer(0, "", nil)
	management.SetWorkspaceChat(fixture.service)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/chat/workspaces", management.wrap(management.handleWorkspaceChatWorkspaces))
	mux.HandleFunc("/api/v1/chat/workspaces/", management.wrap(management.handleWorkspaceChatWorkspaceRoutes))
	mux.HandleFunc("/api/v1/chat/selection", management.wrap(management.handleWorkspaceChatSelection))
	server := httptest.NewServer(mux)
	defer server.Close()

	status, envelope := workspaceChatManagementRequest(t, http.MethodGet, server.URL+"/api/v1/chat/workspaces", nil)
	if status != http.StatusOK || !envelope.OK {
		t.Fatalf("GET workspaces = %d %#v", status, envelope)
	}
	var workspaces struct {
		Workspaces []Workspace `json:"workspaces"`
	}
	workspaceChatDecodeData(t, envelope, &workspaces)
	if len(workspaces.Workspaces) != 2 || workspaces.Workspaces[0].Ref != fixture.workspaceB.Ref || workspaces.Workspaces[1].Ref != fixture.workspaceA.Ref {
		t.Fatalf("ordered workspaces = %#v", workspaces.Workspaces)
	}

	workspaceBase := server.URL + "/api/v1/chat/workspaces/" + url.PathEscape(fixture.workspaceA.Ref)
	status, envelope = workspaceChatManagementRequest(t, http.MethodGet,
		workspaceBase+"/threads?cursor=thread-cursor&limit=7&sort_direction=asc", nil)
	if status != http.StatusOK || !envelope.OK {
		t.Fatalf("GET threads = %d %#v", status, envelope)
	}
	var threads NativeThreadPage
	workspaceChatDecodeData(t, envelope, &threads)
	if len(threads.Data) != 1 || threads.Data[0].ID != fixture.threadA || threads.NextCursor != "threads-next" {
		t.Fatalf("threads page = %#v", threads)
	}

	threadBase := workspaceBase + "/threads/" + url.PathEscape(fixture.threadA)
	status, envelope = workspaceChatManagementRequest(t, http.MethodGet, threadBase, nil)
	if status != http.StatusOK || !envelope.OK {
		t.Fatalf("GET thread snapshot = %d %#v", status, envelope)
	}
	var snapshot NativeConversationSnapshot
	workspaceChatDecodeData(t, envelope, &snapshot)
	if snapshot.Thread.ID != fixture.threadA || snapshot.DeepLink != workspaceChatTestDeepLink(fixture.threadA) {
		t.Fatalf("thread snapshot = %#v", snapshot)
	}

	status, envelope = workspaceChatManagementRequest(t, http.MethodGet,
		threadBase+"/turns?cursor=turn-cursor&limit=8&sort_direction=desc", nil)
	if status != http.StatusOK || !envelope.OK {
		t.Fatalf("GET turns = %d %#v", status, envelope)
	}
	var turns NativeTurnPage
	workspaceChatDecodeData(t, envelope, &turns)
	if len(turns.Data) != 1 || turns.Data[0].ID != "turn-history" || turns.NextCursor != "turns-next" {
		t.Fatalf("turns page = %#v", turns)
	}

	status, envelope = workspaceChatManagementRequest(t, http.MethodGet,
		threadBase+"/items?turn_id=turn-history&cursor=item-cursor&limit=9&sort_direction=asc", nil)
	if status != http.StatusOK || !envelope.OK {
		t.Fatalf("GET items = %d %#v", status, envelope)
	}
	var items NativeItemPage
	workspaceChatDecodeData(t, envelope, &items)
	if len(items.Data) != 1 || items.Data[0].TurnID != "turn-history" || items.NextCursor != "items-next" {
		t.Fatalf("items page = %#v", items)
	}
	status, envelope = workspaceChatManagementRequest(t, http.MethodGet,
		threadBase+"/items?cursor=all-items&limit=11&sort_direction=asc", nil)
	if status != http.StatusOK || !envelope.OK {
		t.Fatalf("GET all thread items = %d %#v", status, envelope)
	}
	workspaceChatDecodeData(t, envelope, &items)
	if len(items.Data) != 2 || items.NextCursor != "all-items-next" {
		t.Fatalf("all thread items page = %#v", items)
	}

	status, envelope = workspaceChatManagementRequest(t, http.MethodGet, threadBase+"/settings", nil)
	if status != http.StatusOK || !envelope.OK {
		t.Fatalf("GET settings = %d %#v", status, envelope)
	}
	var settings NativeThreadSettings
	workspaceChatDecodeData(t, envelope, &settings)
	if settings.Model != "gpt-5" || settings.Effort != "low" {
		t.Fatalf("initial settings = %#v", settings)
	}

	status, envelope = workspaceChatManagementRequest(t, http.MethodPatch, threadBase+"/settings", []byte(`{"effort":"high"}`))
	if status != http.StatusOK || !envelope.OK {
		t.Fatalf("PATCH settings = %d %#v", status, envelope)
	}
	workspaceChatDecodeData(t, envelope, &settings)
	if settings.Effort != "high" || settings.Revision == "" {
		t.Fatalf("updated settings = %#v", settings)
	}

	status, envelope = workspaceChatManagementRequest(t, http.MethodGet, workspaceBase+"/runtime-catalog", nil)
	if status != http.StatusOK || !envelope.OK {
		t.Fatalf("GET runtime catalog = %d %#v", status, envelope)
	}
	var catalog NativeRuntimeCatalog
	workspaceChatDecodeData(t, envelope, &catalog)
	if len(catalog.Models) != 1 || catalog.Models[0].ID != "gpt-5" {
		t.Fatalf("runtime catalog = %#v", catalog)
	}

	status, envelope = workspaceChatManagementRequest(t, http.MethodPost, workspaceBase+"/drafts", []byte(`{}`))
	if status != http.StatusCreated || !envelope.OK {
		t.Fatalf("POST draft = %d %#v", status, envelope)
	}
	var draft WorkspaceChatDraft
	workspaceChatDecodeData(t, envelope, &draft)
	if draft.ID == "" || draft.State != "draft" || draft.ThreadID != "" {
		t.Fatalf("created draft = %#v", draft)
	}
	status, envelope = workspaceChatManagementRequest(t, http.MethodGet,
		workspaceBase+"/drafts/"+url.PathEscape(draft.ID), nil)
	if status != http.StatusOK || !envelope.OK {
		t.Fatalf("GET draft = %d %#v", status, envelope)
	}
	var restoredDraft WorkspaceChatDraft
	workspaceChatDecodeData(t, envelope, &restoredDraft)
	if restoredDraft.ID != draft.ID || restoredDraft.OwnerClientID != workspaceWebClientID {
		t.Fatalf("restored draft = %#v", restoredDraft)
	}

	fixture.agent.mu.Lock()
	threadPage := fixture.agent.lastThreadPage
	turnPage := fixture.agent.lastTurnPage
	itemPage := fixture.agent.lastItemPage
	itemTurnID := fixture.agent.lastItemTurnID
	settingsCalls := append([]NativeThreadSettingsPatch(nil), fixture.agent.settingsCalls...)
	fixture.agent.mu.Unlock()
	if threadPage != (NativePageRequest{Cursor: "thread-cursor", Limit: 7, SortDirection: "asc"}) {
		t.Fatalf("thread page request = %#v", threadPage)
	}
	if turnPage != (NativePageRequest{Cursor: "turn-cursor", Limit: 8, SortDirection: "desc"}) {
		t.Fatalf("turn page request = %#v", turnPage)
	}
	if itemPage != (NativePageRequest{Cursor: "all-items", Limit: 11, SortDirection: "asc"}) || itemTurnID != "" {
		t.Fatalf("item request = %#v turn=%q", itemPage, itemTurnID)
	}
	if len(settingsCalls) != 1 || settingsCalls[0].Effort == nil || *settingsCalls[0].Effort != "high" {
		t.Fatalf("settings calls = %#v", settingsCalls)
	}
}

func TestWorkspaceChatWebSocketRejectsUnknownProtocolShapes(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	management := NewManagementServer(0, "", nil)
	management.SetWorkspaceChat(fixture.service)
	server := httptest.NewServer(http.HandlerFunc(management.handleWorkspaceChatWS))
	defer server.Close()

	connection, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := connection.Close(); err != nil {
			t.Errorf("close websocket: %v", err)
		}
	}()
	if err := connection.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	conversation := map[string]any{"kind": "thread", "id": fixture.threadA}

	if err := connection.WriteJSON(map[string]any{
		"type": "unknown_request", "request_id": "unknown-request", "workspace_ref": fixture.workspaceA.Ref,
		"conversation": conversation,
	}); err != nil {
		t.Fatal(err)
	}
	var event WorkspaceChatEvent
	if err := connection.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "protocol_error" || event.RequestID != "unknown-request" || !strings.Contains(event.Error, "unsupported message type") {
		t.Fatalf("unknown request result = %#v", event)
	}

	if err := connection.WriteJSON(map[string]any{
		"type": "turn_start", "request_id": "unknown-field", "workspace_ref": fixture.workspaceA.Ref,
		"conversation": conversation, "unexpected": true,
	}); err != nil {
		t.Fatal(err)
	}
	event = WorkspaceChatEvent{}
	if err := connection.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "protocol_error" || event.RequestID != "unknown-field" || !strings.Contains(event.Error, `unknown field "unexpected"`) {
		t.Fatalf("unknown turn_start field result = %#v", event)
	}

	fixture.agent.mu.Lock()
	startCalls := len(fixture.agent.startTurnCalls)
	fixture.agent.mu.Unlock()
	if startCalls != 0 {
		t.Fatalf("native turn starts from rejected messages = %d", startCalls)
	}
}

func TestWorkspaceChatWebSocketOperationErrorsUseActorSequence(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	management := NewManagementServer(0, "", nil)
	management.SetWorkspaceChat(fixture.service)
	server := httptest.NewServer(http.HandlerFunc(management.handleWorkspaceChatWS))
	defer server.Close()

	connection, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := connection.Close(); err != nil {
			t.Errorf("close websocket: %v", err)
		}
	}()
	if err := connection.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	conversation := ConversationRef{Kind: ConversationKindThread, ID: fixture.threadA}
	if err := connection.WriteJSON(map[string]any{
		"type": "subscribe", "request_id": "subscribe-sequenced-error", "workspace_ref": fixture.workspaceA.Ref,
		"conversation": conversation,
	}); err != nil {
		t.Fatal(err)
	}
	var subscribed WorkspaceChatEvent
	for index := 0; index < 4; index++ {
		var event WorkspaceChatEvent
		if err := connection.ReadJSON(&event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "subscribed" {
			subscribed = event
			break
		}
	}
	if subscribed.Epoch == "" {
		t.Fatal("did not receive subscribed event")
	}
	if err := connection.WriteJSON(map[string]any{
		"type": "turn_start", "request_id": "sequenced-error", "workspace_ref": fixture.workspaceA.Ref,
		"conversation": conversation, "content": "removed string input",
	}); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 8; index++ {
		var event WorkspaceChatEvent
		if err := connection.ReadJSON(&event); err != nil {
			t.Fatal(err)
		}
		if event.RequestID != "sequenced-error" {
			continue
		}
		if event.Type != "error" || event.Epoch != subscribed.Epoch || event.Sequence <= subscribed.Sequence || !strings.Contains(event.Error, `unknown field "content"`) {
			t.Fatalf("sequenced operation error = %#v, subscribed = %#v", event, subscribed)
		}
		return
	}
	t.Fatal("did not receive sequenced operation error")
}

func TestWorkspaceChatWebSocketOwnsOnlyOneRealtimeConversation(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	threadC := "thread-c"
	fixture.agent.mu.Lock()
	snapshot := cloneWorkspaceChatSnapshot(fixture.agent.snapshots[fixture.threadA])
	snapshot.Thread.ID = threadC
	snapshot.Thread.Preview = "second thread in workspace A"
	snapshot.DeepLink = workspaceChatTestDeepLink(threadC)
	fixture.agent.snapshots[threadC] = snapshot
	fixture.agent.events[threadC] = make(chan NativeEventEnvelope, 64)
	fixture.agent.generations[threadC] = 1
	fixture.agent.mu.Unlock()

	management := NewManagementServer(0, "", nil)
	management.SetWorkspaceChat(fixture.service)
	server := httptest.NewServer(http.HandlerFunc(management.handleWorkspaceChatWS))
	defer server.Close()

	connection, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	writeRealtime := func(requestID, operation, threadID string) {
		t.Helper()
		request := map[string]any{
			"type": operation, "request_id": requestID, "workspace_ref": fixture.workspaceA.Ref,
			"conversation": ConversationRef{Kind: ConversationKindThread, ID: threadID},
		}
		if operation == "realtime_start" {
			request["sdp"] = "v=0"
		}
		if err := connection.WriteJSON(request); err != nil {
			t.Fatal(err)
		}
	}
	readProtocolError := func(requestID, contains string) {
		t.Helper()
		var event WorkspaceChatEvent
		if err := connection.ReadJSON(&event); err != nil {
			t.Fatal(err)
		}
		if event.Type != "protocol_error" || event.RequestID != requestID || !strings.Contains(event.Error, contains) {
			t.Fatalf("realtime ownership error = %#v", event)
		}
	}

	writeRealtime("start-a", "realtime_start", fixture.threadA)
	writeRealtime("start-c", "realtime_start", threadC)
	readProtocolError("start-c", "already owns")
	writeRealtime("stop-c", "realtime_stop", threadC)
	readProtocolError("stop-c", "does not own")
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}

	if !eventuallyWorkspaceChat(2*time.Second, func() bool {
		fixture.agent.mu.Lock()
		defer fixture.agent.mu.Unlock()
		return len(fixture.agent.realtimeStopCalls) == 1
	}) {
		t.Fatal("WebSocket disconnect did not stop its realtime conversation")
	}
	fixture.agent.mu.Lock()
	startCalls := append([]workspaceChatRealtimeStartCall(nil), fixture.agent.realtimeStartCalls...)
	stopCalls := append([]workspaceChatRealtimeStopCall(nil), fixture.agent.realtimeStopCalls...)
	fixture.agent.mu.Unlock()
	if len(startCalls) != 1 || startCalls[0].ThreadID != fixture.threadA {
		t.Fatalf("realtime start calls = %#v", startCalls)
	}
	if len(stopCalls) != 1 || stopCalls[0].ThreadID != fixture.threadA {
		t.Fatalf("realtime stop calls = %#v", stopCalls)
	}
}

func TestWorkspaceChatWebSocketCanRestartRealtimeAfterNativeClose(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	management := NewManagementServer(0, "", nil)
	management.SetWorkspaceChat(fixture.service)
	server := httptest.NewServer(http.HandlerFunc(management.handleWorkspaceChatWS))
	defer server.Close()
	connection, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := connection.Close(); err != nil {
			t.Errorf("close websocket: %v", err)
		}
	}()
	if err := connection.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	conversation := ConversationRef{Kind: ConversationKindThread, ID: fixture.threadA}
	if err := connection.WriteJSON(map[string]any{
		"type": "subscribe", "request_id": "subscribe", "workspace_ref": fixture.workspaceA.Ref, "conversation": conversation,
	}); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 4; index++ {
		var event WorkspaceChatEvent
		if err := connection.ReadJSON(&event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "snapshot" {
			break
		}
	}
	start := func(requestID string) {
		t.Helper()
		if err := connection.WriteJSON(map[string]any{
			"type": "realtime_start", "request_id": requestID, "workspace_ref": fixture.workspaceA.Ref,
			"conversation": conversation, "sdp": "v=0",
		}); err != nil {
			t.Fatal(err)
		}
	}
	start("start-first")
	if !eventuallyWorkspaceChat(2*time.Second, func() bool {
		fixture.agent.mu.Lock()
		defer fixture.agent.mu.Unlock()
		return len(fixture.agent.realtimeStartCalls) == 1
	}) {
		t.Fatal("first realtime did not start")
	}
	if err := fixture.agent.emit(fixture.threadA, NativeEventEnvelope{
		Method: "thread/realtime/closed", ThreadID: fixture.threadA, Payload: json.RawMessage(`{"reason":"remote_closed"}`), OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	for {
		var event WorkspaceChatEvent
		if err := connection.ReadJSON(&event); err != nil {
			t.Fatal(err)
		}
		if workspaceChatEventClosesRealtime(event) {
			break
		}
	}
	start("start-second")
	if !eventuallyWorkspaceChat(2*time.Second, func() bool {
		fixture.agent.mu.Lock()
		defer fixture.agent.mu.Unlock()
		return len(fixture.agent.realtimeStartCalls) == 2
	}) {
		t.Fatal("realtime could not restart after native closed event")
	}
}

func workspaceChatManagementRequest(t *testing.T, method, requestURL string, body []byte) (int, workspaceChatManagementEnvelope) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequest(method, requestURL, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var envelope workspaceChatManagementEnvelope
	decodeErr := json.NewDecoder(response.Body).Decode(&envelope)
	closeErr := response.Body.Close()
	if decodeErr != nil {
		t.Fatalf("decode %s %s response: %v", method, requestURL, decodeErr)
	}
	if closeErr != nil {
		t.Fatalf("close %s %s response: %v", method, requestURL, closeErr)
	}
	return response.StatusCode, envelope
}

func workspaceChatDecodeData(t *testing.T, envelope workspaceChatManagementEnvelope, destination any) {
	t.Helper()
	if len(envelope.Data) == 0 {
		t.Fatalf("response has no data: %s", envelope.Error)
	}
	if err := json.Unmarshal(envelope.Data, destination); err != nil {
		t.Fatalf("decode response data as %T: %v; raw=%s", destination, err, string(envelope.Data))
	}
}
