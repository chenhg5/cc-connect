package acp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestReasoningEffortDisplayDoesNotRoundTrip(t *testing.T) {
	a, err := New(map[string]any{"command": "true"})
	if err != nil {
		t.Fatal(err)
	}
	agent := a.(*Agent)
	agent.reportThoughtLevel("medium", []string{"medium", "high", "max"})
	if agent.GetReasoningEffort() != "medium" {
		t.Fatalf("reported effort = %q", agent.GetReasoningEffort())
	}
	if _, ok := agent.WorkspaceAgentOptions()["reasoning_effort"]; ok {
		t.Fatal("snapshot stored the reported effort")
	}
	if strings.Join(agent.AvailableReasoningEfforts(), ",") != "medium,high,max" {
		t.Fatalf("efforts = %v", agent.AvailableReasoningEfforts())
	}
	agent.SetReasoningEffort("high")
	if agent.GetReasoningEffort() != "high" || agent.WorkspaceAgentOptions()["reasoning_effort"] != "high" {
		t.Fatalf("configured effort snapshot = %#v", agent.WorkspaceAgentOptions())
	}
}

func TestApplyModelThenThoughtLevel(t *testing.T) {
	s, wResp, rReq := newTestSession(t, &Agent{})
	s.absorbConfigOptions([]acpConfigOption{
		{
			ID: "model", Category: "model", CurrentValue: "adaptive",
			Options: []acpConfigOptionChoice{
				{Value: "swe-2-high", Name: "SWE-2"},
				{Value: "adaptive", Name: "Adaptive"},
			},
		},
		{
			ID: "thought_level", Category: "thought_level", CurrentValue: "medium",
			Options: []acpConfigOptionChoice{{Value: "medium"}, {Value: "high"}, {Value: "max"}},
		},
	})
	s.modelResolver = func(input string, _ []string) (string, string) {
		if input == "swe-2-medium" {
			return "swe-2-high", "medium"
		}
		return "", ""
	}
	var mu sync.Mutex
	var got []string
	go scriptACP(t, rReq, wResp, func(method string, params json.RawMessage) (string, string) {
		if method != "session/set_config_option" {
			return "", "unexpected " + method
		}
		var p struct {
			ConfigID string `json:"configId"`
			Value    string `json:"value"`
		}
		_ = json.Unmarshal(params, &p)
		mu.Lock()
		got = append(got, p.ConfigID+"="+p.Value)
		n := len(got)
		mu.Unlock()
		if n == 1 {
			return `{"configOptions":[{"id":"model","category":"model","currentValue":"swe-2-high","options":[{"value":"swe-2-high","name":"SWE-2"},{"value":"adaptive","name":"Adaptive"}]},{"id":"thought_level","category":"thought_level","currentValue":"medium","options":[{"value":"medium"},{"value":"high"},{"value":"max"}]}]}`, ""
		}
		return `{"configOptions":[{"id":"thought_level","category":"thought_level","currentValue":"high","options":[{"value":"medium"},{"value":"high"},{"value":"max"}]}]}`, ""
	})
	s.applyStartupPreferences("", "swe-2-medium", "high")
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(got, ",") != "model=swe-2-high,thought_level=high" {
		t.Fatalf("calls = %v", got)
	}
	if s.StartupWarning() != "" {
		t.Fatalf("warning = %q", s.StartupWarning())
	}
}

func TestSetConfigOptionErrorIsWarned(t *testing.T) {
	s, wResp, rReq := newTestSession(t, nil)
	s.setACPSessionID("s1")
	s.absorbConfigOptions([]acpConfigOption{{
		ID: "model", Category: "model", CurrentValue: "adaptive",
		Options: []acpConfigOptionChoice{{Value: "swe-2-high", Name: "SWE-2"}, {Value: "adaptive", Name: "Adaptive"}},
	}})
	go func() {
		sc := bufio.NewScanner(rReq)
		for sc.Scan() {
			var req struct {
				ID json.RawMessage `json:"id"`
			}
			if json.Unmarshal(sc.Bytes(), &req) != nil || len(req.ID) == 0 {
				continue
			}
			fmt.Fprintf(wResp, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32602,"message":"Invalid value 'swe-2-high' for config option 'model'","data":"rejected"}}`+"\n", req.ID)
		}
	}()
	s.applyInitialModel("swe-2-high")
	warn := s.StartupWarning()
	if !strings.Contains(warn, "Invalid value 'swe-2-high'") || !strings.Contains(warn, "rejected") {
		t.Fatalf("warning = %q", warn)
	}
	if s.GetModel() != "adaptive" {
		t.Fatalf("model changed to %q", s.GetModel())
	}
}

func TestThoughtLevelNotAllowed(t *testing.T) {
	s, wResp, rReq := newTestSession(t, nil)
	s.absorbConfigOptions([]acpConfigOption{
		{ID: "model", Category: "model", CurrentValue: "swe-2-high", Options: []acpConfigOptionChoice{{Value: "swe-2-high"}}},
		{ID: "thought_level", Category: "thought_level", CurrentValue: "medium", Options: []acpConfigOptionChoice{{Value: "medium"}, {Value: "high"}, {Value: "max"}}},
	})
	var calls int
	go scriptACP(t, rReq, wResp, func(string, json.RawMessage) (string, string) {
		calls++
		return `{}`, ""
	})
	s.applyStartupPreferences("", "swe-2-high", "xhigh")
	if calls != 0 {
		t.Fatalf("calls = %d", calls)
	}
	warn := s.StartupWarning()
	if !strings.Contains(warn, "xhigh") || !strings.Contains(warn, "medium, high, max") {
		t.Fatalf("warning = %q", warn)
	}
}

func TestModeDescriptionsFromConfig(t *testing.T) {
	agent := &Agent{}
	s, _, _ := newTestSession(t, agent)
	raw := []byte(`{"modes":{"currentModeId":"accept-edits","availableModes":[{"id":"accept-edits","name":"Code"},{"id":"ask","name":"Ask"}]},"configOptions":[{"id":"mode","category":"mode","options":[{"value":"accept-edits","name":"Code","description":"Write and edit code"},{"value":"ask","name":"Ask","description":"Answer questions"}]}]}`)
	if err := s.absorbSessionResult(raw, false); err != nil {
		t.Fatal(err)
	}
	modes := agent.PermissionModes()
	if len(modes) != 2 || modes[0].Desc != "Write and edit code" || modes[0].DescZh != modes[0].Desc || modes[1].Desc != "Answer questions" {
		t.Fatalf("modes = %#v", modes)
	}
}

func TestPermission_DevinEditableCommand(t *testing.T) {
	s, _, _ := newTestSession(t, nil)
	params := []byte(`{"sessionId":"s","toolCall":{"toolCallId":"call_1","_meta":{"cognition.ai/editableCommand":"for i in ..."}},"options":[{"optionId":"allow_once","name":"Allow","kind":"allow_once"},{"optionId":"allow_session","name":"Yes, allow ` + "`seq`" + ` commands (this session)","kind":"allow_always"},{"optionId":"allow_always","name":"Always","kind":"allow_always"},{"optionId":"allow_always_global","name":"Global","kind":"allow_always"},{"optionId":"switch_bypass","name":"Yes, switch to bypass mode","kind":"allow_always"},{"optionId":"reject_once","name":"Reject","kind":"reject_once"}]}`)
	s.handlePermissionRequest(json.RawMessage(`1`), params)
	ev := readEvent(t, s)
	if ev.Type != core.EventPermissionRequest || ev.ToolName != "Shell" || ev.ToolInput != "for i in ..." {
		t.Fatalf("event = %+v", ev)
	}

	s.cacheToolCallInput([]byte(`{"update":{"sessionUpdate":"tool_call","toolCallId":"call_2","title":"Bash","kind":"execute"}}`))
	params = []byte(`{"sessionId":"s","toolCall":{"toolCallId":"call_2","_meta":{"cognition.ai/editableCommand":"for i in ..."}},"options":[{"optionId":"reject_once","name":"Reject","kind":"reject_once"}]}`)
	s.handlePermissionRequest(json.RawMessage(`2`), params)
	ev = readEvent(t, s)
	if ev.ToolName != "Bash" || ev.ToolInput != "for i in ..." {
		t.Fatalf("cached name event = %+v", ev)
	}
}

func TestDispatchPrompt_CompactionWaits(t *testing.T) {
	s, wResp, rReq := newTestSession(t, nil)
	s.compactCommand = "/compress"
	go scriptACP(t, rReq, wResp, func(method string, _ json.RawMessage) (string, string) {
		if method != "session/prompt" {
			return "", "unexpected " + method
		}
		go func() {
			time.Sleep(40 * time.Millisecond)
			fmt.Fprint(wResp, `{"jsonrpc":"2.0","method":"_cognition.ai/compaction","params":{"status":"started"}}`+"\n")
			fmt.Fprint(wResp, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session-id","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Context compacted"}}}}`+"\n")
			time.Sleep(20 * time.Millisecond)
			fmt.Fprint(wResp, `{"jsonrpc":"2.0","method":"_cognition.ai/compaction","params":{"status":"completed"}}`+"\n")
		}()
		return `{"stopReason":"end_turn"}`, ""
	})
	started := time.Now()
	if err := s.Send("/compress", "m", nil, nil); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("compact wait hung")
	}
	var sawText, sawResult bool
	deadline := time.After(2 * time.Second)
	for !sawResult {
		select {
		case ev := <-s.events:
			if ev.Type == core.EventText && strings.Contains(ev.Content, "Context compacted") {
				if sawResult {
					t.Fatal("compaction text arrived after the result")
				}
				sawText = true
			}
			if ev.Type == core.EventResult {
				sawResult = true
			}
		case <-deadline:
			t.Fatal("missing result")
		}
	}
	if !sawText {
		t.Fatal("missing compaction text")
	}
}

func TestDispatchPrompt_CompactionTextAfterCompleted(t *testing.T) {
	s, wResp, rReq := newTestSession(t, nil)
	go scriptACP(t, rReq, wResp, func(method string, _ json.RawMessage) (string, string) {
		if method != "session/prompt" {
			return "", "unexpected " + method
		}
		go func() {
			time.Sleep(30 * time.Millisecond)
			fmt.Fprint(wResp, `{"jsonrpc":"2.0","method":"_cognition.ai/compaction","params":{"status":"started"}}`+"\n")
			fmt.Fprint(wResp, `{"jsonrpc":"2.0","method":"_cognition.ai/compaction","params":{"status":"completed"}}`+"\n")
			time.Sleep(200 * time.Millisecond)
			fmt.Fprint(wResp, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session-id","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Context compacted"}}}}`+"\n")
		}()
		return `{"stopReason":"end_turn"}`, ""
	})
	if err := s.Send("/compact", "m", nil, nil); err != nil {
		t.Fatal(err)
	}
	var order []core.EventType
	deadline := time.After(3 * time.Second)
	for len(order) < 2 {
		select {
		case ev := <-s.events:
			if ev.Type == core.EventText || ev.Type == core.EventResult {
				order = append(order, ev.Type)
			}
		case <-deadline:
			t.Fatalf("events = %v", order)
		}
	}
	if order[0] != core.EventText || order[1] != core.EventResult {
		t.Fatalf("order = %v", order)
	}
}

func TestDispatchPrompt_CompactionFailed(t *testing.T) {
	s, wResp, rReq := newTestSession(t, nil)
	go scriptACP(t, rReq, wResp, func(method string, _ json.RawMessage) (string, string) {
		if method != "session/prompt" {
			return "", "unexpected " + method
		}
		go func() {
			time.Sleep(30 * time.Millisecond)
			fmt.Fprint(wResp, `{"jsonrpc":"2.0","method":"_cognition.ai/compaction","params":{"status":"started"}}`+"\n")
			time.Sleep(20 * time.Millisecond)
			fmt.Fprint(wResp, `{"jsonrpc":"2.0","method":"_cognition.ai/compaction","params":{"status":"failed"}}`+"\n")
		}()
		return `{"stopReason":"end_turn"}`, ""
	})
	if err := s.Send("/compact", "m", nil, nil); err != nil {
		t.Fatal(err)
	}
	var sawErr, sawResult bool
	deadline := time.After(2 * time.Second)
	for !sawResult {
		select {
		case ev := <-s.events:
			if ev.Type == core.EventError {
				sawErr = true
				if sawResult {
					t.Fatal("error after result")
				}
			}
			if ev.Type == core.EventResult {
				sawResult = true
			}
		case <-deadline:
			t.Fatal("missing result")
		}
	}
	if !sawErr {
		t.Fatal("missing compaction error")
	}
}

func readEvent(t *testing.T, s *acpSession) core.Event {
	t.Helper()
	select {
	case ev := <-s.events:
		return ev
	case <-time.After(time.Second):
		t.Fatal("no event")
		return core.Event{}
	}
}

// TestSessionGetReasoningEffort_ReportsLiveThoughtLevel: the reply footer must
// show the effort the agent actually runs with. In production, switching to a
// model without "high" kept showing "effort:high" while Devin ran "medium".
func TestSessionGetReasoningEffort_ReportsLiveThoughtLevel(t *testing.T) {
	s, _, _ := newTestSession(t, nil)
	if got := s.GetReasoningEffort(); got != "" {
		t.Fatalf("before options: %q, want empty", got)
	}
	s.absorbConfigOptions([]acpConfigOption{{
		ID: "thought_level", Category: "thought_level", CurrentValue: "medium",
		Options: []acpConfigOptionChoice{{Value: "medium"}, {Value: "max"}},
	}})
	if got := s.GetReasoningEffort(); got != "medium" {
		t.Fatalf("GetReasoningEffort = %q, want medium", got)
	}
}
