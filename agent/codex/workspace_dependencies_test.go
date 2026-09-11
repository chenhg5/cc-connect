package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppServerSession_ThreadStartRegistersWorkspaceDependenciesTool(t *testing.T) {
	s := &appServerSession{
		workspaceDependencies: workspaceDependenciesConfig{Enabled: true},
	}

	params := s.threadRequestParams(true)
	dynamicTools, ok := params["dynamicTools"].([]map[string]any)
	if !ok || len(dynamicTools) != 1 {
		t.Fatalf("dynamicTools = %#v, want one tool", params["dynamicTools"])
	}
	tool := dynamicTools[0]
	if tool["type"] != "function" || tool["name"] != workspaceDependenciesToolName {
		t.Fatalf("tool = %#v", tool)
	}
	if _, ok := tool["inputSchema"].(map[string]any); !ok {
		t.Fatalf("inputSchema = %#v, want object schema", tool["inputSchema"])
	}
}

func TestAppServerSession_ThreadResumeOmitsWorkspaceDependenciesTool(t *testing.T) {
	s := &appServerSession{
		workspaceDependencies: workspaceDependenciesConfig{Enabled: true},
	}

	if params := s.threadRequestParams(false); params["dynamicTools"] != nil {
		t.Fatalf("dynamicTools = %#v, want omitted for thread/resume", params["dynamicTools"])
	}
}

func TestAppServerSession_WorkspaceDependenciesDisabledByDefault(t *testing.T) {
	s := &appServerSession{}
	if params := s.threadRequestParams(true); params["dynamicTools"] != nil {
		t.Fatalf("dynamicTools = %#v, want omitted", params["dynamicTools"])
	}
}

func TestAppServerSession_HandleWorkspaceDependenciesTool(t *testing.T) {
	runtimeRoot := writeTestWorkspaceDependenciesRuntime(t)
	stdin := &lockedWriteCloser{}
	s := &appServerSession{
		stdin: stdin,
		workspaceDependencies: workspaceDependenciesConfig{
			Enabled:     true,
			RuntimeRoot: runtimeRoot,
		},
	}

	namespace := "codex_app"
	s.handleDynamicToolCall(json.RawMessage(`"dyn-1"`), mustJSON(t, appServerDynamicToolCallParams{
		ThreadID:  "thread-1",
		TurnID:    "turn-1",
		CallID:    "call-1",
		Namespace: &namespace,
		Tool:      workspaceDependenciesToolName,
		Arguments: map[string]any{},
	}))

	response := decodeDynamicToolResponse(t, waitForWrittenJSONLine(t, stdin))
	if !response.Result.Success {
		t.Fatalf("success = false, content = %q", response.Result.ContentItems[0].Text)
	}
	text := response.Result.ContentItems[0].Text
	for _, want := range []string{
		"Bundle version: `test-bundle`",
		"@oai/artifact-tool version: `test-artifact`",
		filepath.Join(runtimeRoot, "dependencies", "node", "bin", "node"),
		filepath.Join(runtimeRoot, "dependencies", "node", "node_modules"),
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("response missing %q:\n%s", want, text)
		}
	}
}

func TestAppServerSession_HandleWorkspaceDependenciesToolFailsClosed(t *testing.T) {
	stdin := &lockedWriteCloser{}
	s := &appServerSession{
		stdin: stdin,
		workspaceDependencies: workspaceDependenciesConfig{
			Enabled:     true,
			RuntimeRoot: t.TempDir(),
		},
	}

	s.handleDynamicToolCall(json.RawMessage(`2`), mustJSON(t, appServerDynamicToolCallParams{
		Tool:      workspaceDependenciesToolName,
		Arguments: map[string]any{},
	}))

	response := decodeDynamicToolResponse(t, waitForWrittenJSONLine(t, stdin))
	if response.Result.Success {
		t.Fatal("success = true, want false for missing runtime")
	}
	if got := response.Result.ContentItems[0].Text; !strings.Contains(got, "runtime unavailable") {
		t.Fatalf("content = %q, want runtime unavailable", got)
	}
}

func TestAppServerSession_UnknownDynamicToolStillFailsClosed(t *testing.T) {
	stdin := &lockedWriteCloser{}
	s := &appServerSession{
		stdin: stdin,
		workspaceDependencies: workspaceDependenciesConfig{
			Enabled: true,
		},
	}

	s.handleDynamicToolCall(json.RawMessage(`3`), mustJSON(t, appServerDynamicToolCallParams{
		Tool:      "unknown_tool",
		Arguments: map[string]any{},
	}))

	response := decodeDynamicToolResponse(t, waitForWrittenJSONLine(t, stdin))
	if response.Result.Success {
		t.Fatal("success = true, want false")
	}
	if got := response.Result.ContentItems[0].Text; got != "tool not available on this client" {
		t.Fatalf("content = %q", got)
	}
}

func TestAppServerSession_WorkspaceDependenciesRejectsUnknownNamespace(t *testing.T) {
	stdin := &lockedWriteCloser{}
	s := &appServerSession{
		stdin: stdin,
		workspaceDependencies: workspaceDependenciesConfig{
			Enabled: true,
		},
	}

	namespace := "untrusted_client"
	s.handleDynamicToolCall(json.RawMessage(`4`), mustJSON(t, appServerDynamicToolCallParams{
		Namespace: &namespace,
		Tool:      workspaceDependenciesToolName,
		Arguments: map[string]any{},
	}))

	response := decodeDynamicToolResponse(t, waitForWrittenJSONLine(t, stdin))
	if response.Result.Success {
		t.Fatal("success = true, want false")
	}
	if got := response.Result.ContentItems[0].Text; got != "tool not available on this client" {
		t.Fatalf("content = %q", got)
	}
}

func TestLoadWorkspaceDependencies_WindowsRuntimeLayout(t *testing.T) {
	runtimeRoot := writeWorkspaceDependenciesRuntime(t, []string{
		"dependencies/node/node.exe",
		"dependencies/node/node_modules/@oai/artifact-tool/package.json",
		"dependencies/python/python.exe",
		"dependencies/bin/override/soffice.exe",
		"dependencies/bin/fallback/git.exe",
		"dependencies/bin/fallback/pnpm.cmd",
	})

	text, err := loadWorkspaceDependencies(runtimeRoot)
	if err != nil {
		t.Fatalf("loadWorkspaceDependencies() error = %v", err)
	}
	for _, want := range []string{"node.exe", "python.exe", "git.exe", "pnpm.cmd"} {
		if !strings.Contains(text, want) {
			t.Fatalf("response missing %q:\n%s", want, text)
		}
	}
}

func writeTestWorkspaceDependenciesRuntime(t *testing.T) string {
	t.Helper()
	return writeWorkspaceDependenciesRuntime(t, []string{
		"dependencies/node/bin/node",
		"dependencies/node/node_modules/@oai/artifact-tool/package.json",
		"dependencies/python/bin/python3",
		"dependencies/bin/override/soffice",
		"dependencies/bin/fallback/git",
		"dependencies/bin/fallback/pnpm",
	})
}

func writeWorkspaceDependenciesRuntime(t *testing.T, paths []string) string {
	t.Helper()
	root := t.TempDir()
	for _, rel := range paths {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte("test"), 0o755); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	manifest := []byte(`{"bundleVersion":"test-bundle","artifactToolVersion":"test-artifact"}`)
	if err := os.WriteFile(filepath.Join(root, "runtime.json"), manifest, 0o644); err != nil {
		t.Fatalf("write runtime.json: %v", err)
	}
	return root
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return b
}

func decodeDynamicToolResponse(t *testing.T, line string) struct {
	Result struct {
		Success      bool `json:"success"`
		ContentItems []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"contentItems"`
	} `json:"result"`
} {
	t.Helper()
	var response struct {
		Result struct {
			Success      bool `json:"success"`
			ContentItems []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"contentItems"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &response); err != nil {
		t.Fatalf("decode response %q: %v", line, err)
	}
	if len(response.Result.ContentItems) != 1 || response.Result.ContentItems[0].Type != "inputText" {
		t.Fatalf("contentItems = %#v", response.Result.ContentItems)
	}
	return response
}
