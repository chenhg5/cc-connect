package copilot

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"testing"
	"time"
)

// TestMain intercepts test binary re-executions used as a fake copilot process.
// When CC_MOCK_COPILOT_MODE is set, we run the mock server instead of tests.
func TestMain(m *testing.M) {
	mode := os.Getenv("CC_MOCK_COPILOT_MODE")
	switch mode {
	case "list_sessions":
		runMockCopilot(handleListSessions)
		os.Exit(0)
	case "delete_session":
		runMockCopilot(handleDeleteSession)
		os.Exit(0)
	case "empty_assistant_message":
		runMockCopilot(handleEmptyAssistantMessage)
		os.Exit(0)
	default:
		os.Exit(m.Run())
	}
}

// mockCopilotHandler is called for each incoming JSON-RPC request.
// It should write a framed JSON-RPC response to stdout.
type mockCopilotHandler func(method string, id json.RawMessage, params json.RawMessage, w *lspWriter)

// runMockCopilot reads Content-Length framed JSON-RPC messages from stdin,
// dispatches them to handler, and exits on EOF.
func runMockCopilot(handler mockCopilotHandler) {
	reader := newLSPReader(os.Stdin)
	writer := newLSPWriter(os.Stdout)

	for {
		body, err := reader.readMessage()
		if err != nil {
			if err == io.EOF {
				return
			}
			return
		}

		var probe struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(body, &probe) != nil {
			continue
		}
		// Only handle requests (have ID), skip notifications
		if len(probe.ID) == 0 {
			continue
		}
		handler(probe.Method, probe.ID, body, writer)
	}
}

func writeResponse(w *lspWriter, id json.RawMessage, result any) {
	res, _ := json.Marshal(result)
	_ = w.writeMessage(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  json.RawMessage(res),
	})
}

func writeError(w *lspWriter, id json.RawMessage, code int, msg string) {
	_ = w.writeMessage(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": msg},
	})
}

// handleListSessions is the mock that responds to ping + session.list.
func handleListSessions(method string, id json.RawMessage, _ json.RawMessage, w *lspWriter) {
	switch method {
	case "ping":
		writeResponse(w, id, map[string]any{"pong": true})
	case "session.list":
		summary1 := "First session"
		summary2 := "Second session"
		writeResponse(w, id, map[string]any{
			"sessions": []map[string]any{
				{
					"sessionId":    "sess-001",
					"startTime":    time.Now().Format(time.RFC3339),
					"modifiedTime": time.Now().Format(time.RFC3339),
					"summary":      &summary1,
				},
				{
					"sessionId":    "sess-002",
					"startTime":    time.Now().Format(time.RFC3339),
					"modifiedTime": time.Now().Format(time.RFC3339),
					"summary":      &summary2,
				},
			},
		})
	default:
		writeError(w, id, -32601, fmt.Sprintf("method not found: %s", method))
	}
}

// handleDeleteSession is the mock that responds to ping + session.delete.
func handleDeleteSession(method string, id json.RawMessage, _ json.RawMessage, w *lspWriter) {
	switch method {
	case "ping":
		writeResponse(w, id, map[string]any{"pong": true})
	case "session.delete":
		writeResponse(w, id, map[string]any{"success": true})
	default:
		writeError(w, id, -32601, fmt.Sprintf("method not found: %s", method))
	}
}

// handleEmptyAssistantMessage simulates the bug scenario:
// 1. Empty assistant.message (during tool call) - should NOT complete turn
// 2. Tool execution start
// 3. Tool execution complete
// 4. Proper assistant.message with content - SHOULD complete turn
func handleEmptyAssistantMessage(method string, id json.RawMessage, params json.RawMessage, w *lspWriter) {
	switch method {
	case "ping":
		writeResponse(w, id, map[string]any{"pong": true})
	case "session.create", "session.resume":
		writeResponse(w, id, map[string]any{
			"sessionId": "test-session-123",
		})
	case "session.send":
		writeResponse(w, id, map[string]any{"success": true})
		// 1. Empty assistant.message (during tool call) - should NOT complete turn
		writeSessionEvent(w, map[string]any{
			"type": "assistant.message",
			"data": map[string]any{"outputTokens": 0},
		})
		// 2. Tool execution start
		writeSessionEvent(w, map[string]any{
			"type": "tool.execution_start",
			"data": map[string]any{
				"toolCallId": "call-1",
				"toolName":   "shell",
			},
		})
		// 3. Tool execution complete
		writeSessionEvent(w, map[string]any{
			"type": "tool.execution_complete",
			"data": map[string]any{"toolCallId": "call-1"},
		})
		// 4. Proper assistant.message with content - SHOULD complete turn
		writeSessionEvent(w, map[string]any{
			"type": "assistant.message",
			"data": map[string]any{
				"content":      "Hello from Copilot!",
				"outputTokens": 5,
			},
		})
		// 5. Assistant usage
		writeSessionEvent(w, map[string]any{
			"type": "assistant.usage",
			"data": map[string]any{
				"inputTokens":  10,
				"outputTokens": 5,
			},
		})
	default:
		writeError(w, id, -32601, fmt.Sprintf("method not found: %s", method))
	}
}

func writeNotification(w *lspWriter, method string, params json.RawMessage) {
	_ = w.writeMessage(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

func writeSessionEvent(w *lspWriter, event map[string]any) {
	params, _ := json.Marshal(map[string]any{
		"sessionId": "test-session-123",
		"event":     event,
	})
	writeNotification(w, "session.event", params)
}
