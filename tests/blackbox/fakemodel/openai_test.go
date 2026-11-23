package fakemodel

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func postResponses(t *testing.T, s *Server, body map[string]any) (*http.Response, string) {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(s.URL()+"/v1/responses", "application/json", strings.NewReader(string(b)))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp, string(out)
}

// responsesStream collects text deltas, the completed output items and the
// event types of a Responses SSE body.
func responsesStream(t *testing.T, body string) (text string, items []map[string]any, types []string) {
	t.Helper()
	for _, e := range sseEvents(t, body) {
		typ, _ := e["type"].(string)
		types = append(types, typ)
		switch typ {
		case "response.output_text.delta":
			text += e["delta"].(string)
		case "response.completed":
			for _, it := range e["response"].(map[string]any)["output"].([]any) {
				items = append(items, it.(map[string]any))
			}
		}
	}
	return text, items, types
}

func userItem(text string) map[string]any {
	return map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": text}}}
}

func TestResponsesReplyStreamsEvents(t *testing.T) {
	s := New()
	defer s.Close()
	_, body := postResponses(t, s, map[string]any{"model": "m", "stream": true, "input": []any{
		map[string]any{"type": "message", "role": "developer", "content": []any{map[string]any{"type": "input_text", "text": "instructions"}}},
		userItem("<environment_context>cwd</environment_context>"),
		userItem("FAKE:reply hello codex"),
	}})
	text, items, types := responsesStream(t, body)
	if text != "hello codex" {
		t.Fatalf("text %q", text)
	}
	if types[0] != "response.created" || types[len(types)-1] != "response.completed" {
		t.Fatalf("event order %v", types)
	}
	if len(items) != 1 || items[0]["type"] != "message" {
		t.Fatalf("output %v", items)
	}
	if r := s.TurnRequests(); len(r) != 1 || r[0].Wire != "openai" || r[0].Directive != "FAKE:reply hello codex" {
		t.Fatalf("recorded %+v", r)
	}
}

func TestResponsesToolCallAndOutput(t *testing.T) {
	s := New()
	defer s.Close()
	_, body := postResponses(t, s, map[string]any{"model": "m", "stream": true, "input": []any{userItem("FAKE:escalate touch x")}})
	_, items, _ := responsesStream(t, body)
	if len(items) != 1 || items[0]["type"] != "function_call" || items[0]["name"] != codexShellTool {
		t.Fatalf("output %v", items)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(items[0]["arguments"].(string)), &args); err != nil {
		t.Fatal(err)
	}
	if args["cmd"] != "touch x" || args["sandbox_permissions"] != "require_escalated" {
		t.Fatalf("arguments %v", args)
	}

	_, body = postResponses(t, s, map[string]any{"model": "m", "stream": true, "input": []any{
		userItem("FAKE:escalate touch x"),
		items[0],
		map[string]any{"type": "function_call_output", "call_id": items[0]["call_id"], "output": "Output:\n42"},
	}})
	if text, _, _ := responsesStream(t, body); text != "TOOL_DONE: Output:\n42" {
		t.Fatalf("after tool output: %q", text)
	}
}

func TestResponsesRecallAndImages(t *testing.T) {
	s := New()
	defer s.Close()
	_, body := postResponses(t, s, map[string]any{"model": "m", "stream": true, "input": []any{
		userItem("FAKE:reply apple"),
		map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "apple"}}},
		map[string]any{"type": "message", "role": "user", "content": []any{
			map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AA=="},
			map[string]any{"type": "input_text", "text": "FAKE:recall"},
		}},
	}})
	text, _, _ := responsesStream(t, body)
	if !strings.HasPrefix(text, "HISTORY: ") || !strings.Contains(text, "apple") {
		t.Fatalf("got %q", text)
	}
	r := s.TurnRequests()
	if len(r) != 1 || r[0].Images != 1 {
		t.Fatalf("recorded %+v", r)
	}
}

func TestResponsesNonStreaming(t *testing.T) {
	s := New()
	defer s.Close()
	resp, body := postResponses(t, s, map[string]any{"model": "m", "input": []any{userItem("FAKE:reply plain")}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"text":"plain"`) || !strings.Contains(body, `"status":"completed"`) {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if len(s.TurnRequests()) != 0 {
		t.Fatalf("non-streaming request counted as a turn")
	}
}
