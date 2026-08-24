package remotenative

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	"github.com/chenhg5/cc-connect/runtimeprotocol"
)

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
