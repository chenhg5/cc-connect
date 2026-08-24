package runtimeidentity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/runtimeprotocol"
)

func TestRuntimeStatePersistsOnlyEventMetadataAndAcknowledgements(t *testing.T) {
	directory := t.TempDir()
	store := &Store{directory: directory}
	payload := []byte(`{"message":"conversation body must not be persisted"}`)
	resource := runtimeprotocol.Resource{WorkspaceRef: "workspace-local", ConversationRef: "thread-1"}
	if err := store.RecordUnconfirmed(7, 3, runtimeprotocol.MethodNativeEvent, resource, payload); err != nil {
		t.Fatalf("RecordUnconfirmed() error = %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(directory, "runtime-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "conversation body") {
		t.Fatalf("runtime state persisted native payload: %s", raw)
	}
	state, err := store.State()
	if err != nil || len(state.PendingEvents) != 1 || state.PendingEvents[0].PayloadSHA256 == "" {
		t.Fatalf("State() = %#v, %v", state, err)
	}
	if err := store.Confirm(7, 3); err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	state, err = store.State()
	if err != nil || len(state.PendingEvents) != 0 || state.ConfirmedGeneration != 7 || state.ConfirmedSequence != 3 {
		t.Fatalf("confirmed State() = %#v, %v", state, err)
	}
}

func TestRuntimeStateRetainsOlderGenerationWithoutResending(t *testing.T) {
	store := &Store{directory: t.TempDir()}
	if err := store.RecordUnconfirmed(1, 1, runtimeprotocol.MethodNativeEvent, runtimeprotocol.Resource{}, []byte("old")); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUnconfirmed(2, 1, runtimeprotocol.MethodNativeEvent, runtimeprotocol.Resource{}, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if err := store.Confirm(2, 1); err != nil {
		t.Fatal(err)
	}
	state, err := store.State()
	if err != nil || len(state.PendingEvents) != 1 || state.PendingEvents[0].ConnectionGeneration != 1 {
		t.Fatalf("State() = %#v, %v", state, err)
	}
}
