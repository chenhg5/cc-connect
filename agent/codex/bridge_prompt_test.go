package codex

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestAppServerBridgePrompt_NativeNewAndResumedThread(t *testing.T) {
	for _, tc := range []struct {
		name, resume string
		rpcCode      int
		rpcMessage   string
		fail         bool
	}{
		{name: "new"},
		{name: "resume", resume: "old-thread"},
		{name: "legacy method not found", resume: "old-thread", rpcCode: -32601, rpcMessage: "method not found"},
		{name: "legacy unknown variant", resume: "old-thread", rpcCode: -32600, rpcMessage: "unknown variant thread/inject_items"},
		{name: "real injection failure", resume: "old-thread", rpcCode: -32000, rpcMessage: "permission denied", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			r, w := io.Pipe()
			s := &appServerSession{ctx: ctx, cancel: cancel, stdin: w, workDir: t.TempDir(),
				promptPreamble: core.AgentSessionPrompt(core.LangEnglish, "", ""),
				events:         make(chan core.Event, 16), pending: make(map[int64]chan rpcResponseEnvelope)}
			s.alive.Store(true)
			requests := make(chan map[string]any, 8)
			go func() {
				dec := json.NewDecoder(r)
				for {
					var req map[string]any
					if dec.Decode(&req) != nil {
						return
					}
					requests <- req
					if req["method"] == "thread/inject_items" && tc.rpcCode != 0 {
						s.handleResponse(rpcResponseEnvelope{ID: req["id"], Error: &rpcError{Code: tc.rpcCode, Message: tc.rpcMessage}})
						continue
					}
					result := `{}`
					switch req["method"] {
					case "config/read":
						result = `{"config":{"developer_instructions":"keep-global-instructions"}}`
					case "thread/start", "thread/resume":
						result = `{"thread":{"id":"chosen-thread"}}`
					case "turn/start":
						result = `{"turn":{"id":"next-turn"}}`
					}
					s.handleResponse(rpcResponseEnvelope{ID: req["id"], Result: json.RawMessage(result)})
				}
			}()
			t.Cleanup(func() { cancel(); _ = r.Close(); _ = w.Close() })
			err := s.ensureThread(tc.resume)
			if tc.fail {
				if err == nil || s.promptAsContext {
					t.Fatal("a real injection failure must not silently fall back")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if req := <-requests; req["method"] != "config/read" {
				t.Fatal(req)
			}
			req := <-requests
			params := req["params"].(map[string]any)
			instructions, _ := params["developerInstructions"].(string)
			if !strings.Contains(instructions, "cc-connect send --file") || !strings.Contains(instructions, "keep-global-instructions") {
				t.Fatalf("native instructions lost bridge or existing config: %v", params)
			}
			if _, exists := params["baseInstructions"]; exists {
				t.Fatal("must not replace Codex base prompt")
			}
			if tc.resume != "" {
				req = <-requests
				if req["method"] != "thread/inject_items" {
					t.Fatal("resumed thread missing immediate developer item", req)
				}
				items := req["params"].(map[string]any)["items"].([]any)
				if items[0].(map[string]any)["role"] != "developer" {
					t.Fatal(items)
				}
			}
			if err := s.Send("send the existing report", "", nil, nil); err != nil {
				t.Fatal(err)
			}
			req = <-requests
			input := req["params"].(map[string]any)["input"].([]any)
			text, _ := input[0].(map[string]any)["text"].(string)
			if req["method"] != "turn/start" {
				t.Fatal(req)
			}
			if tc.rpcCode == 0 && text != "send the existing report" {
				t.Fatal("native instructions were mixed into the user message", req)
			}
			if tc.rpcCode != 0 && (!strings.Contains(text, "cc-connect send --file") || !strings.Contains(text, "User message:\nsend the existing report")) {
				t.Fatal("older server lost bridge context", req)
			}
			select {
			case req := <-requests:
				t.Fatal("duplicate injection on normal turn", req)
			default:
			}
		})
	}
}

func TestCodexBridgePrompt_AutomaticallyLoadedWithoutCustomOptions(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(map[string]any{"cmd": bin, "work_dir": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	s, err := a.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if !strings.Contains(s.(*codexSession).promptPreamble, "cc-connect send --file") {
		t.Fatal("worker with no custom prompt did not load bridge instructions")
	}
}

func TestCurrentBridgePrompt_DeduplicatesOnlyEffectiveDeveloperContext(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "sessions", "probe-thread.jsonl")
	prompt := core.AgentSessionPrompt(core.LangEnglish, "", "")
	message := map[string]any{"type": "message", "role": "developer", "content": []any{map[string]any{"type": "input_text", "text": prompt}}}
	for _, tc := range []struct {
		name    string
		records []any
		want    bool
	}{
		{"already injected", []any{map[string]any{"type": "response_item", "payload": message}}, true},
		{"compaction removed it", []any{map[string]any{"type": "response_item", "payload": message}, map[string]any{"type": "compacted", "payload": map[string]any{"replacement_history": []any{}}}}, false},
		{"compaction retained it", []any{map[string]any{"type": "compacted", "payload": map[string]any{"replacement_history": []any{message}}}}, true},
		{"new bridge version", []any{map[string]any{"type": "response_item", "payload": message}, map[string]any{"type": "response_item", "payload": map[string]any{"role": "developer", "content": []any{map[string]any{"text": "You are running inside cc-connect: different instructions"}}}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var content strings.Builder
			for _, record := range tc.records {
				data, _ := json.Marshal(record)
				content.Write(data)
				content.WriteByte('\n')
			}
			if err := os.WriteFile(path, []byte(content.String()), 0600); err != nil {
				t.Fatal(err)
			}
			if got := hasCurrentBridgePrompt("probe-thread", root, prompt); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
