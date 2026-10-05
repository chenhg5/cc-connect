package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/daemon"
)

type pluginTestManager struct {
	daemon.Manager
	starts, stops int
	startErr      error
}

func (m *pluginTestManager) Start() error { m.starts++; return m.startErr }
func (m *pluginTestManager) Stop() error  { m.stops++; return nil }
func (m *pluginTestManager) Status() (*daemon.Status, error) {
	return &daemon.Status{Installed: true, Running: true, PID: 123}, nil
}

func TestCodexPlugin_InitializeDiscoverStatusAndDisconnect(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":"init","method":"initialize","params":{"protocolVersion":"2025-06-18"}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}
{"jsonrpc":"2.0","id":3,"method":"tools/list"}
{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"connection_status","arguments":{}}}
{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"stop"}}
{"jsonrpc":"2.0","id":6,"method":"unknown"}`
	mgr := &pluginTestManager{}
	var output bytes.Buffer
	if err := serveCodexPlugin(strings.NewReader(input), &output, mgr); err != nil {
		t.Fatal(err)
	}
	if mgr.starts != 1 || mgr.stops != 0 {
		t.Fatalf("starts=%d stops=%d: duplicate initialize or EOF affected shared daemon", mgr.starts, mgr.stops)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 6 {
		t.Fatalf("notifications must not produce responses: %s", output.String())
	}
	for i, line := range lines {
		var response map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatal(err)
		}
		if i < 4 && response["error"] != nil {
			t.Fatalf("request %d failed: %s", i, line)
		}
	}
	if !strings.Contains(lines[0], `"id":"init"`) || !strings.Contains(lines[0], `"protocolVersion":"2025-06-18"`) {
		t.Fatal("initialize did not preserve request ID or supported protocol")
	}
	if !strings.Contains(lines[2], `"name":"connection_status"`) || !strings.Contains(lines[3], `\"Running\":true`) {
		t.Fatalf("missing tool or status: %s", output.String())
	}
	if !strings.Contains(lines[4], `"code":-32602`) || !strings.Contains(lines[5], `"code":-32601`) {
		t.Fatal("unsupported tools and methods must return MCP errors")
	}
}

func TestCodexPlugin_FailedStartDoesNotClaimReady(t *testing.T) {
	mgr := &pluginTestManager{startErr: errors.New("task not installed")}
	var output bytes.Buffer
	if err := serveCodexPlugin(strings.NewReader(`{"id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`), &output, mgr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"error"`) || strings.Contains(output.String(), `"result"`) {
		t.Fatalf("startup failure advertised successful initialization: %s", output.String())
	}
}
