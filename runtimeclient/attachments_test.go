package runtimeclient

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	"github.com/chenhg5/cc-connect/runtimeprotocol"
)

func TestHandlerMaterializesAttachmentsAndCleansAtTurnTerminal(t *testing.T) {
	handler := &Handler{turnArtifacts: make(map[string][]string), terminalTurns: make(map[string]struct{})}
	handler.SetAttachmentFetcher(func(_ context.Context, ref string) (runtimeprotocol.AttachmentContent, error) {
		contents := map[string]runtimeprotocol.AttachmentContent{
			"image-ref": {WorkspaceRef: "workspace-1", Type: "image", FileName: "screen.png", Data: []byte("png")},
			"file-ref":  {WorkspaceRef: "workspace-1", Type: "file", FileName: "report.txt", Data: []byte("report")},
		}
		return contents[ref], nil
	})
	inputs, directories, err := handler.materializeInputs(context.Background(), core.Workspace{Ref: "workspace-1"}, []core.NativeUserInput{
		{Type: "text", Text: "inspect"},
		{Type: "image", AttachmentRef: "image-ref"},
		{Type: "file", AttachmentRef: "file-ref"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(directories) != 1 || inputs[1].LocalPath == "" || inputs[1].AttachmentRef != "" || inputs[2].Type != "text" || !strings.Contains(inputs[2].Text, directories[0]) {
		t.Fatalf("materialized inputs = %#v, directories = %#v", inputs, directories)
	}
	if _, err := os.Stat(inputs[1].LocalPath); err != nil {
		t.Fatal(err)
	}
	handler.bindTurnArtifacts("workspace-1", "thread-1", "turn-1", directories)
	handler.completeTurnArtifacts("workspace-1", "thread-1", "turn-1", "turn/completed")
	if _, err := os.Stat(directories[0]); !os.IsNotExist(err) {
		t.Fatalf("terminal turn left Runtime attachment directory: %v", err)
	}
}

func TestHandlerCleansAttachmentsOnDisconnectAndRejectsCrossWorkspaceContent(t *testing.T) {
	handler := &Handler{turnArtifacts: make(map[string][]string), terminalTurns: make(map[string]struct{})}
	handler.SetAttachmentFetcher(func(_ context.Context, _ string) (runtimeprotocol.AttachmentContent, error) {
		return runtimeprotocol.AttachmentContent{WorkspaceRef: "workspace-1", Type: "image", Data: []byte("png")}, nil
	})
	_, _, err := handler.materializeInputs(context.Background(), core.Workspace{Ref: "workspace-2"}, []core.NativeUserInput{{Type: "image", AttachmentRef: "ref"}})
	if err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("cross-workspace materialization error = %v", err)
	}
	inputs, directories, err := handler.materializeInputs(context.Background(), core.Workspace{Ref: "workspace-1"}, []core.NativeUserInput{{Type: "image", AttachmentRef: "ref"}})
	if err != nil {
		t.Fatal(err)
	}
	handler.bindTurnArtifacts("workspace-1", "thread-1", "turn-1", directories)
	handler.ReleaseConnectionArtifacts()
	if _, err := os.Stat(inputs[0].LocalPath); !os.IsNotExist(err) {
		t.Fatalf("disconnect left Runtime attachment file: %v", err)
	}
}
