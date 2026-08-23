package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
		mgmtError(w, http.StatusBadGateway, err.Error())
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
	if len(parts) < 2 || parts[1] != "threads" {
		mgmtError(w, http.StatusNotFound, "workspace chat route not found")
		return
	}
	workspaceRef, err := url.PathUnescape(parts[0])
	if err != nil || workspaceRef == "" {
		mgmtError(w, http.StatusBadRequest, "invalid workspace reference")
		return
	}
	if len(parts) == 2 {
		switch r.Method {
		case http.MethodGet:
			threads, err := service.ListThreads(r.Context(), workspaceRef)
			if err != nil {
				mgmtError(w, http.StatusBadGateway, err.Error())
				return
			}
			mgmtJSON(w, http.StatusOK, map[string]any{"threads": threads})
		case http.MethodPost:
			var body struct {
				Name string `json:"name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				mgmtError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
			thread, err := service.StartThread(r.Context(), workspaceWebClientID, workspaceRef, body.Name)
			if err != nil {
				mgmtError(w, http.StatusBadGateway, err.Error())
				return
			}
			mgmtJSON(w, http.StatusCreated, thread)
		default:
			mgmtError(w, http.StatusMethodNotAllowed, "GET or POST only")
		}
		return
	}
	if len(parts) != 3 || r.Method != http.MethodGet {
		mgmtError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	threadID, err := url.PathUnescape(parts[2])
	if err != nil || threadID == "" {
		mgmtError(w, http.StatusBadRequest, "invalid thread id")
		return
	}
	detail, err := service.ReadThread(r.Context(), workspaceRef, threadID)
	if err != nil {
		mgmtError(w, http.StatusNotFound, err.Error())
		return
	}
	mgmtJSON(w, http.StatusOK, detail)
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
			mgmtError(w, http.StatusBadGateway, err.Error())
			return
		}
		mgmtJSON(w, http.StatusOK, selection)
	case http.MethodPut:
		var body struct {
			WorkspaceRef string `json:"workspace_ref"`
			ThreadID     string `json:"thread_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			mgmtError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		var selection WorkspaceChatSelection
		var err error
		if strings.TrimSpace(body.ThreadID) == "" {
			selection, err = service.SelectWorkspace(r.Context(), workspaceWebClientID, body.WorkspaceRef)
		} else {
			selection, err = service.SelectThread(r.Context(), workspaceWebClientID, body.WorkspaceRef, body.ThreadID)
		}
		if err != nil {
			mgmtError(w, http.StatusBadRequest, err.Error())
			return
		}
		mgmtJSON(w, http.StatusOK, selection)
	default:
		mgmtError(w, http.StatusMethodNotAllowed, "GET or PUT only")
	}
}

type workspaceChatWSRequest struct {
	Type         string `json:"type"`
	RequestID    string `json:"request_id,omitempty"`
	WorkspaceRef string `json:"workspace_ref,omitempty"`
	ThreadID     string `json:"thread_id,omitempty"`
	Content      string `json:"content,omitempty"`
	Decision     string `json:"decision,omitempty"`
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
	upgrader := websocket.Upgrader{CheckOrigin: func(req *http.Request) bool {
		origin := req.Header.Get("Origin")
		if origin == "" {
			return true
		}
		parsed, err := url.Parse(origin)
		if err != nil {
			return false
		}
		if strings.EqualFold(parsed.Host, req.Host) {
			return true
		}
		for _, allowed := range m.corsOrigins {
			if allowed == "*" || allowed == origin {
				return true
			}
		}
		return false
	}}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	var writeMu sync.Mutex
	write := func(value any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteJSON(value)
	}
	var unsubscribe func()
	defer func() {
		if unsubscribe != nil {
			unsubscribe()
		}
	}()
	for {
		var request workspaceChatWSRequest
		if err := conn.ReadJSON(&request); err != nil {
			return
		}
		switch request.Type {
		case "subscribe":
			if _, err := service.ReadThread(r.Context(), request.WorkspaceRef, request.ThreadID); err != nil {
				_ = write(map[string]any{"type": "error", "request_id": request.RequestID, "error": err.Error()})
				continue
			}
			if unsubscribe != nil {
				unsubscribe()
			}
			events, cancel := service.Subscribe(request.ThreadID)
			unsubscribe = cancel
			go func() {
				for event := range events {
					if err := write(event); err != nil {
						return
					}
				}
			}()
			_ = write(map[string]any{"type": "subscribed", "request_id": request.RequestID, "workspace_ref": request.WorkspaceRef, "thread_id": request.ThreadID})
		case "turn_start":
			msg := &Message{Platform: "web", Content: request.Content, Scope: ConversationScopeDirect, MessageID: request.RequestID, UserID: workspaceWebClientID, UserName: "Web"}
			if err := service.Send(r.Context(), workspaceWebClientID, request.RequestID, request.WorkspaceRef, request.ThreadID, nil, msg, nil); err != nil {
				_ = write(map[string]any{"type": "error", "request_id": request.RequestID, "error": err.Error()})
			}
		case "approval_response":
			if err := service.RespondApproval(r.Context(), request.WorkspaceRef, request.ThreadID, request.Decision); err != nil {
				_ = write(map[string]any{"type": "error", "request_id": request.RequestID, "error": err.Error()})
			}
		case "cancel":
			if err := service.Cancel(r.Context(), request.WorkspaceRef, request.ThreadID); err != nil {
				_ = write(map[string]any{"type": "error", "request_id": request.RequestID, "error": err.Error()})
			}
		default:
			_ = write(map[string]any{"type": "error", "request_id": request.RequestID, "error": fmt.Sprintf("unsupported message type %q", request.Type), "occurred_at": time.Now()})
		}
	}
}
