package copilot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestLSPWriter_WriteMessage(t *testing.T) {
	var buf bytes.Buffer
	w := newLSPWriter(&buf)

	msg := jsonRPCRequest{JSONRPC: "2.0", ID: 1, Method: "ping"}
	if err := w.writeMessage(msg); err != nil {
		t.Fatalf("writeMessage: %v", err)
	}

	output := buf.String()
	if !strings.HasPrefix(output, "Content-Length: ") {
		t.Fatalf("output missing Content-Length header: %q", output)
	}
	if !strings.Contains(output, "\r\n\r\n") {
		t.Fatalf("output missing header separator: %q", output)
	}
	// Verify JSON body
	parts := strings.SplitN(output, "\r\n\r\n", 2)
	var decoded jsonRPCRequest
	if err := json.Unmarshal([]byte(parts[1]), &decoded); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if decoded.Method != "ping" {
		t.Fatalf("method = %q, want ping", decoded.Method)
	}
}

func TestLSPReader_ReadMessage(t *testing.T) {
	body := `{"jsonrpc":"2.0","method":"test"}`
	frame := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)

	r := newLSPReader(strings.NewReader(frame))
	got, err := r.readMessage()
	if err != nil {
		t.Fatalf("readMessage: %v", err)
	}
	if string(got) != body {
		t.Fatalf("body = %q, want %q", got, body)
	}
}

func TestLSPReader_MultipleMessages(t *testing.T) {
	body1 := `{"id":1}`
	body2 := `{"id":2}`
	frame := fmt.Sprintf("Content-Length: %d\r\n\r\n%sContent-Length: %d\r\n\r\n%s",
		len(body1), body1, len(body2), body2)

	r := newLSPReader(strings.NewReader(frame))

	got1, err := r.readMessage()
	if err != nil {
		t.Fatalf("readMessage 1: %v", err)
	}
	if string(got1) != body1 {
		t.Fatalf("msg1 = %q, want %q", got1, body1)
	}

	got2, err := r.readMessage()
	if err != nil {
		t.Fatalf("readMessage 2: %v", err)
	}
	if string(got2) != body2 {
		t.Fatalf("msg2 = %q, want %q", got2, body2)
	}
}

func TestLSPReader_MissingContentLength(t *testing.T) {
	frame := "Some-Header: value\r\n\r\n{}"
	r := newLSPReader(strings.NewReader(frame))
	_, err := r.readMessage()
	if err == nil {
		t.Fatal("expected error for missing Content-Length")
	}
}

func TestLSPReader_TooLargeContentLength(t *testing.T) {
	frame := "Content-Length: 99999999999\r\n\r\n"
	r := newLSPReader(strings.NewReader(frame))
	_, err := r.readMessage()
	if err == nil {
		t.Fatal("expected error for oversized Content-Length")
	}
}

func TestRPCClient_CallAndDispatch(t *testing.T) {
	var buf bytes.Buffer
	c := newRPCClient(&buf)

	id, ch := c.call("test.method", map[string]string{"key": "val"})

	// Simulate response
	resp := &jsonRPCResponse{
		JSONRPC: "2.0",
		Result:  json.RawMessage(`{"ok":true}`),
	}
	idBytes, _ := json.Marshal(id)
	resp.ID = idBytes

	if !c.dispatch(resp) {
		t.Fatal("dispatch returned false")
	}

	got := <-ch
	if got.Error != nil {
		t.Fatalf("unexpected error: %v", got.Error)
	}
	if string(got.Result) != `{"ok":true}` {
		t.Fatalf("result = %s, want {\"ok\":true}", got.Result)
	}
}

func TestRPCClient_CancelAll(t *testing.T) {
	var buf bytes.Buffer
	c := newRPCClient(&buf)

	_, ch := c.call("foo", nil)
	c.cancelAll(fmt.Errorf("test cancel"))

	got := <-ch
	if got.Error == nil {
		t.Fatal("expected error after cancelAll")
	}
}

func TestRPCClient_Notify(t *testing.T) {
	var buf bytes.Buffer
	c := newRPCClient(&buf)

	err := c.notify("permission.respond", map[string]string{"requestId": "123"})
	if err != nil {
		t.Fatalf("notify: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "permission.respond") {
		t.Fatalf("output missing method: %q", output)
	}
	// Notifications should NOT have an id field
	if strings.Contains(output, `"id"`) {
		t.Fatalf("notification should not have id field: %q", output)
	}
}

func TestSummarizeToolInput(t *testing.T) {
	got := summarizeToolInput("shell", map[string]any{"command": "ls -la"})
	if !strings.Contains(got, "ls -la") {
		t.Fatalf("summarizeToolInput = %q, want to contain 'ls -la'", got)
	}

	// Nil input
	if got := summarizeToolInput("test", nil); got != "" {
		t.Fatalf("summarizeToolInput(nil) = %q, want empty", got)
	}

	// Long input gets truncated
	longInput := map[string]any{"data": strings.Repeat("x", 300)}
	got = summarizeToolInput("test", longInput)
	if len(got) > 210 {
		t.Fatalf("summarizeToolInput length = %d, should be truncated", len(got))
	}
}

// Verify interface compliance at compile time
var _ core.ContextUsageReporter = (*copilotSession)(nil)
