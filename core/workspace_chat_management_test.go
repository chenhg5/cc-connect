package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWorkspaceChatManagementRESTAndWebSocket(t *testing.T) {
	service, _, agent, _, workspace := newWorkspaceChatTestService(t)
	management := NewManagementServer(0, "secret", nil)
	management.SetWorkspaceChat(service)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/chat/workspaces", management.wrap(management.handleWorkspaceChatWorkspaces))
	mux.HandleFunc("/api/v1/chat/workspaces/", management.wrap(management.handleWorkspaceChatWorkspaceRoutes))
	mux.HandleFunc("/api/v1/chat/selection", management.wrap(management.handleWorkspaceChatSelection))
	mux.HandleFunc("/api/v1/chat/ws", management.wrap(management.handleWorkspaceChatWS))
	server := httptest.NewServer(mux)
	defer server.Close()

	request, _ := http.NewRequest(http.MethodGet, server.URL+"/api/v1/chat/workspaces", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("workspace status = %d", response.StatusCode)
	}
	var workspacesResponse struct {
		OK   bool `json:"ok"`
		Data struct {
			Workspaces []Workspace `json:"workspaces"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&workspacesResponse); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if !workspacesResponse.OK || len(workspacesResponse.Data.Workspaces) != 1 || workspacesResponse.Data.Workspaces[0].Ref != workspace.Ref {
		t.Fatalf("workspaces response = %#v", workspacesResponse)
	}

	selectionBody, _ := json.Marshal(map[string]any{"workspace_ref": workspace.Ref, "thread_id": "thread-1"})
	request, _ = http.NewRequest(http.MethodPut, server.URL+"/api/v1/chat/selection", bytes.NewReader(selectionBody))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("selection status = %d", response.StatusCode)
	}
	_ = response.Body.Close()

	request, _ = http.NewRequest(http.MethodGet, server.URL+"/api/v1/chat/workspaces/"+workspace.Ref+"/threads/thread-1", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("thread read status = %d", response.StatusCode)
	}
	_ = response.Body.Close()

	request, _ = http.NewRequest(http.MethodGet, server.URL+"/api/v1/chat/workspaces/"+workspace.Ref+"/threads/forged", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("forged thread status = %d", response.StatusCode)
	}
	_ = response.Body.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/chat/ws?token=secret"
	connection, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := connection.Close(); err != nil {
			t.Errorf("close websocket: %v", err)
		}
	})
	if err := connection.WriteJSON(map[string]any{"type": "subscribe", "request_id": "subscribe-1", "workspace_ref": workspace.Ref, "thread_id": "thread-1"}); err != nil {
		t.Fatal(err)
	}
	var event WorkspaceChatEvent
	if err := connection.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "subscribed" || event.ThreadID != "thread-1" {
		t.Fatalf("subscribe event = %#v", event)
	}
	if err := connection.WriteJSON(map[string]any{"type": "turn_start", "request_id": "web-turn-1", "workspace_ref": workspace.Ref, "thread_id": "thread-1", "content": "from-web"}); err != nil {
		t.Fatal(err)
	}
	if got := <-agent.started; got != "from-web" {
		t.Fatalf("web prompt = %q", got)
	}
	agent.release <- struct{}{}
	deadline := time.Now().Add(2 * time.Second)
	for event.Type != "turn_completed" && time.Now().Before(deadline) {
		_ = connection.SetReadDeadline(deadline)
		if err := connection.ReadJSON(&event); err != nil {
			t.Fatal(err)
		}
	}
	if event.Type != "turn_completed" || event.RequestID != "web-turn-1" {
		t.Fatalf("terminal event = %#v", event)
	}
}

func TestWorkspaceChatWebSocketRejectsCrossWorkspaceThread(t *testing.T) {
	service, _, agent, _, workspace := newWorkspaceChatTestService(t)
	otherRoot := t.TempDir()
	agent.mu.Lock()
	agent.threads["other-thread"] = NativeThreadDetail{NativeThread: NativeThread{ID: "other-thread", Cwd: otherRoot}}
	agent.mu.Unlock()
	management := NewManagementServer(0, "", nil)
	management.SetWorkspaceChat(service)
	server := httptest.NewServer(http.HandlerFunc(management.handleWorkspaceChatWS))
	defer server.Close()
	connection, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := connection.Close(); err != nil {
			t.Errorf("close websocket: %v", err)
		}
	})
	if err := connection.WriteJSON(map[string]any{"type": "subscribe", "workspace_ref": workspace.Ref, "thread_id": "other-thread"}); err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := connection.ReadJSON(&response); err != nil {
		t.Fatal(err)
	}
	if response["type"] != "error" || !strings.Contains(response["error"].(string), "does not belong") {
		t.Fatalf("cross-workspace response = %#v", response)
	}
}
