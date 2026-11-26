package fakemodel

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// post sends a Messages API request and returns the response body.
func post(t *testing.T, s *Server, body map[string]any) (*http.Response, string) {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(s.URL()+"/v1/messages?beta=true", "application/json", strings.NewReader(string(b)))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp, string(out)
}

// sseEvents parses an SSE body into its data payloads.
func sseEvents(t *testing.T, body string) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &m); err != nil {
			t.Fatalf("bad SSE data %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// streamedText concatenates text deltas and returns the stop reason.
func streamedText(events []map[string]any) (text, stop string, toolInput string) {
	for _, e := range events {
		switch e["type"] {
		case "content_block_delta":
			d := e["delta"].(map[string]any)
			if s, ok := d["text"].(string); ok {
				text += s
			}
			if s, ok := d["partial_json"].(string); ok {
				toolInput += s
			}
		case "message_delta":
			stop, _ = e["delta"].(map[string]any)["stop_reason"].(string)
		}
	}
	return text, stop, toolInput
}

func userTurn(content any) map[string]any {
	return map[string]any{"model": "m", "stream": true, "messages": []any{
		map[string]any{"role": "user", "content": content},
	}}
}

func TestReplyDirectiveStreamsText(t *testing.T) {
	s := New()
	defer s.Close()
	resp, body := post(t, s, userTurn("<system-reminder>x</system-reminder>\nFAKE:reply hello there"))
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	text, stop, _ := streamedText(sseEvents(t, body))
	if text != "hello there" || stop != "end_turn" {
		t.Fatalf("got text=%q stop=%q", text, stop)
	}
	reqs := s.TurnRequests()
	if len(reqs) != 1 || reqs[0].Directive != "FAKE:reply hello there" {
		t.Fatalf("recorded %+v", reqs)
	}
}

func TestToolDirectiveThenToolResult(t *testing.T) {
	s := New()
	defer s.Close()
	_, body := post(t, s, userTurn([]any{map[string]any{"type": "text", "text": "FAKE:tool Bash echo hi"}}))
	_, stop, input := streamedText(sseEvents(t, body))
	if stop != "tool_use" || !strings.Contains(input, `"command":"echo hi"`) {
		t.Fatalf("got stop=%q input=%q", stop, input)
	}

	_, body = post(t, s, userTurn([]any{map[string]any{
		"type": "tool_result", "tool_use_id": "toolu_1", "content": []any{map[string]any{"type": "text", "text": "hi\n"}},
	}}))
	text, stop, _ := streamedText(sseEvents(t, body))
	if text != "TOOL_DONE: hi" || stop != "end_turn" {
		t.Fatalf("got text=%q stop=%q", text, stop)
	}

	_, body = post(t, s, userTurn([]any{map[string]any{
		"type": "tool_result", "tool_use_id": "toolu_2", "is_error": true, "content": "denied",
	}}))
	if text, _, _ := streamedText(sseEvents(t, body)); text != "TOOL_ERROR: denied" {
		t.Fatalf("error result: got %q", text)
	}
}

func TestRecallReturnsEarlierTurns(t *testing.T) {
	s := New()
	defer s.Close()
	_, body := post(t, s, map[string]any{"model": "m", "stream": true, "messages": []any{
		map[string]any{"role": "user", "content": "FAKE:reply apple"},
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "apple"}}},
		map[string]any{"role": "user", "content": "FAKE:recall"},
	}})
	text, _, _ := streamedText(sseEvents(t, body))
	if !strings.HasPrefix(text, "HISTORY: ") || !strings.Contains(text, "apple") {
		t.Fatalf("got %q", text)
	}
}

func TestNoDirectiveEchoesAndCountsImages(t *testing.T) {
	s := New()
	defer s.Close()
	_, body := post(t, s, userTurn([]any{
		map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "AA=="}},
		map[string]any{"type": "text", "text": "what is this\nsecond line"},
	}))
	if text, _, _ := streamedText(sseEvents(t, body)); text != "ECHO: what is this" {
		t.Fatalf("got %q", text)
	}
	if r := s.TurnRequests(); len(r) != 1 || r[0].Images != 1 {
		t.Fatalf("recorded %+v", r)
	}
}

func TestSideRequestsAreShortAndNotTurns(t *testing.T) {
	s := New()
	defer s.Close()
	req := userTurn("summarise")
	req["stream"] = false
	resp, body := post(t, s, req)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"text":"ok"`) {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if len(s.TurnRequests()) != 0 || len(s.Requests()) != 1 {
		t.Fatalf("side request counted as a turn: %+v", s.Requests())
	}
}

func TestCountTokensAndUnknownPaths(t *testing.T) {
	s := New()
	defer s.Close()
	resp, err := http.Post(s.URL()+"/v1/messages/count_tokens", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("count_tokens status %d", resp.StatusCode)
	}
	resp, err = http.Get(s.URL() + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET status %d, want 404", resp.StatusCode)
	}
}
