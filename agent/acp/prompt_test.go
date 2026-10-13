package acp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestWorkspaceAgentOptions_PreservesPromptsModeAndCommandArguments(t *testing.T) {
	parent, err := New(map[string]any{
		"cmd": []string{"true", "--wrapper"}, "args": []string{"acp", "serve"},
		"mode": "plan", "system_prompt": "Plan only.", "append_system_prompt": "Report evidence.",
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := New(parent.(*Agent).WorkspaceAgentOptions())
	if err != nil {
		t.Fatal(err)
	}
	got := child.(*Agent)
	if got.systemPrompt != "Plan only." || got.appendPrompt != "Report evidence." || got.mode != "plan" || !reflect.DeepEqual(got.cliExtraArgs, []string{"--wrapper"}) {
		t.Fatalf("workspace clone lost role configuration: %+v", got)
	}
}

func TestSession_RolePromptNewResumeAndFailedFirstTurn(t *testing.T) {
	for _, resumed := range []bool{false, true} {
		t.Run(fmt.Sprintf("resumed=%v", resumed), func(t *testing.T) {
			s, response, request := newTestSession(t, nil)
			if resumed {
				s.acpSessID = "existing-session"
			}
			s.promptPreamble = buildPromptPreamble("Plan only.", "Report evidence.")
			observed := make(chan string, 3)
			go func() {
				scanner := bufio.NewScanner(request)
				for i := 0; scanner.Scan(); i++ {
					var req struct {
						ID     json.RawMessage `json:"id"`
						Params struct {
							Prompt []struct {
								Text string `json:"text"`
							} `json:"prompt"`
						} `json:"params"`
					}
					if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
						return
					}
					if len(req.Params.Prompt) == 0 {
						return
					}
					observed <- req.Params.Prompt[0].Text
					if i == 0 {
						fmt.Fprintf(response, "{\"jsonrpc\":\"2.0\",\"id\":%s,\"error\":{\"code\":-32000,\"message\":\"retry\"}}\n", req.ID)
					} else {
						fmt.Fprintf(response, "{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"stopReason\":\"end_turn\"}}\n", req.ID)
					}
				}
			}()
			if err := s.Send("first task", "1", nil, nil); err == nil {
				t.Fatal("expected first RPC failure")
			}
			if err := s.Send("first task", "2", nil, nil); err != nil {
				t.Fatal(err)
			}
			if err := s.Send("follow up", "3", nil, nil); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				got := <-observed
				if i < 2 {
					if !strings.Contains(got, "Plan only.") || !strings.Contains(got, "Report evidence.") || !strings.Contains(got, "first task") {
						t.Fatalf("lost instructions on attempt %d: %s", i, got)
					}
				} else if got != "follow up" {
					t.Fatalf("instructions repeated on established session: %s", got)
				}
			}
		})
	}
}

func TestSession_NoPromptLeavesMessageUnchanged(t *testing.T) {
	s, response, request := newTestSession(t, nil)
	observed := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(request).ReadBytes('\n')
		var req map[string]json.RawMessage
		_ = json.Unmarshal(line, &req)
		observed <- string(req["params"])
		_, _ = io.WriteString(response, fmt.Sprintf("{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{}}\n", req["id"]))
	}()
	if err := s.Send("original message", "1", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := <-observed; !strings.Contains(got, `"text":"original message"`) {
		t.Fatalf("legacy message changed: %s", got)
	}
}
