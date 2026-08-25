package remotenative

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/chenhg5/cc-connect/runtimeprotocol"
)

func TestStreamEventsRestoresPrivateNativeRoutingFields(t *testing.T) {
	requestID := json.RawMessage(`91`)
	publicEvent, err := json.Marshal(core.NativeEventEnvelope{
		Method: "item/commandExecution/requestApproval", ThreadID: "thread-1", TurnID: "turn-1",
		InteractionID: "interaction-1", RequestID: requestID, ConnectionGeneration: 7,
		Payload: json.RawMessage(`{"command":"go test"}`), OccurredAt: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := runtimeprotocol.MarshalPayload(runtimeprotocol.NativeEventPayload{
		Event: publicEvent, RequestID: requestID, NativeConnectionGeneration: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	envelope := runtimeprotocol.Envelope{
		ContractHash: runtimeprotocol.ContractHash, DeviceID: "device-1", ConnectionGeneration: 3, Sequence: 1,
		Method:   runtimeprotocol.MethodNativeEvent,
		Resource: runtimeprotocol.Resource{WorkspaceRef: "workspace-1", ConversationRef: "thread-1"}, Payload: payload,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("workspace_ref") != "workspace-1" || r.URL.Query().Get("thread_id") != "thread-1" {
			t.Errorf("event stream query = %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		writer := bufio.NewWriter(w)
		if err := json.NewEncoder(writer).Encode(envelope); err != nil {
			t.Error(err)
		}
		_ = writer.Flush()
	}))
	defer server.Close()

	backend := &Backend{client: server.Client(), base: server.URL}
	events := make(chan core.NativeEventEnvelope, 1)
	ready := make(chan error, 1)
	backend.streamEvents(context.Background(), "workspace-1", "thread-1", events, ready)
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	event, open := <-events
	if !open {
		t.Fatal("原生事件流未返回事件")
	}
	if event.ConnectionGeneration != 7 || string(event.RequestID) != "91" || event.InteractionID != "interaction-1" {
		t.Fatalf("原生私有路由字段 = %#v", event)
	}
	if strings.Contains(string(publicEvent), "connection_generation") || strings.Contains(string(publicEvent), "request_id") {
		t.Fatalf("公开事件泄露了私有路由字段: %s", publicEvent)
	}
}

func TestStageInputsReplacesVerifiedBytesWithOpaqueReferences(t *testing.T) {
	var captured runtimeprotocol.AttachmentStageRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true,"data":[{"ref":"att_one","type":"image","mime_type":"image/png","file_name":"screen.png"}]}`))
	}))
	defer server.Close()
	backend := &Backend{client: server.Client(), base: server.URL}
	inputs, err := backend.stageInputs(context.Background(), core.Workspace{Ref: "global-workspace", DeviceID: "device-1"}, []core.NativeUserInput{
		{Type: "text", Text: "inspect"},
		{Type: "image", MimeType: "image/png", FileName: "screen.png", Data: []byte("png")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if captured.DeviceID != "device-1" || captured.WorkspaceRef != "global-workspace" || len(captured.Attachments) != 1 {
		t.Fatalf("captured stage request = %#v", captured)
	}
	if inputs[1].AttachmentRef != "att_one" || len(inputs[1].Data) != 0 || inputs[1].LocalPath != "" {
		t.Fatalf("staged inputs = %#v", inputs)
	}
	raw, err := json.Marshal(inputs)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "cG5n") || strings.Contains(string(raw), "local_path") || strings.Contains(string(raw), "server/private") {
		t.Fatalf("runtime input leaked attachment bytes or path: %s", raw)
	}
}

func TestStageInputsRejectsPreexistingOrServerLocalReferences(t *testing.T) {
	backend := &Backend{}
	for _, input := range []core.NativeUserInput{
		{Type: "image", AttachmentRef: "att_forged"},
		{Type: "file", LocalPath: "/var/lib/private"},
	} {
		if _, err := backend.stageInputs(context.Background(), core.Workspace{}, []core.NativeUserInput{input}); err == nil {
			t.Fatalf("stageInputs() accepted %#v", input)
		}
	}
}
