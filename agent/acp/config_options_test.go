package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testConfigOptions(model, effort string) []configOption {
	options := []configOption{{ID: "provider_model", Category: "model", Type: "select", CurrentValue: model, Options: []configChoice{{Value: "smart", Name: "Smart"}, {Value: "fast", Name: "Fast"}, {Value: "denied", Name: "Denied"}}}}
	if model != "fast" {
		options = append(options, configOption{ID: "reasoning_effort", Category: "thought_level", Type: "select", CurrentValue: effort, Options: []configChoice{{Value: "low"}, {Value: "high"}}})
	}
	return options
}
func optionJSON(model, effort string) json.RawMessage {
	b, _ := json.Marshal(testConfigOptions(model, effort))
	return b
}

func TestConfigOptions_GroupedAndUnknownTypes(t *testing.T) {
	s := &acpSession{}
	err := s.absorbConfigOptions(json.RawMessage(`[
 {"id":"future","type":"boolean","currentValue":true},
 {"id":"vendor-id","category":"model","type":"select","currentValue":"a","options":[{"group":"vendor","name":"Vendor","options":[{"value":"a","name":"Alpha"}]}]},
 {"id":"model","category":"vendor_unknown","type":"select","currentValue":"x","options":[{"value":"x"}]}]`))
	if err != nil {
		t.Fatal(err)
	}
	o, ok := s.option("model")
	if !ok || o.ID != "vendor-id" || len(o.Options) != 1 || o.Options[0].Value != "a" {
		t.Fatalf("%+v", o)
	}
	if _, ok := s.option("thought_level"); ok {
		t.Fatal("invented capability")
	}
}
func TestConfigOptionUpdate_ReplacesSnapshotAndChecksSession(t *testing.T) {
	s := &acpSession{acpSessID: "ours"}
	_ = s.absorbConfigOptions(optionJSON("smart", "high"))
	notify := func(sid, model string) {
		raw, _ := json.Marshal(map[string]any{"sessionId": sid, "update": map[string]any{"sessionUpdate": "config_option_update", "configOptions": testConfigOptions(model, "low")}})
		s.absorbConfigUpdate(raw)
	}
	notify("other", "fast")
	if _, ok := s.option("thought_level"); !ok {
		t.Fatal("foreign session changed configuration")
	}
	notify("ours", "fast")
	if _, ok := s.option("thought_level"); ok {
		t.Fatal("removed option still visible")
	}
	notify("ours", "smart")
	o, _ := s.option("thought_level")
	if o.CurrentValue != "low" {
		t.Fatal(o)
	}
	_ = s.absorbConfigOptions(json.RawMessage(`[]`))
	if _, ok := s.option("model"); ok {
		t.Fatal("empty snapshot not applied")
	}
}

// A subprocess fixture tests initialize/new/load/set_config_option end to end,
// including cancellation, warm-session ownership and actual RPC parameter names.
func TestACPConfigServer(t *testing.T) {
	if os.Getenv("CC_ACP_CONFIG_HELPER") != "1" {
		return
	}
	model, effort := "smart", "low"
	logFile, _ := os.OpenFile(os.Getenv("CC_ACP_CONFIG_AUDIT"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	defer logFile.Close()
	sc := bufio.NewScanner(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for sc.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params map[string]any  `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &req) != nil {
			os.Exit(2)
		}
		fmt.Fprintln(logFile, req.Method)
		var result any = map[string]any{}
		var rpcError any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": true}}
		case "session/new":
			result = map[string]any{"sessionId": "reserved-session", "configOptions": testConfigOptions(model, effort)}
		case "session/load":
			// A conforming load result need not repeat sessionId. History replay must
			// not fill the unconsumed event queue and block the response.
			for i := 0; i < 200; i++ {
				enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": req.Params["sessionId"], "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "history"}}}})
			}
			result = map[string]any{"configOptions": testConfigOptions(model, effort)}
		case "session/set_config_option":
			value, _ := req.Params["value"].(string)
			switch req.Params["configId"] {
			case "provider_model":
				if value == "denied" {
					rpcError = map[string]any{"code": -32000, "message": "selection denied"}
				} else {
					model = value
					effort = "low"
				}
			case "reasoning_effort":
				effort = value
			default:
				rpcError = map[string]any{"code": -32602, "message": "wrong configId"}
			}
			result = map[string]any{"configOptions": testConfigOptions(model, effort)}
		default:
			rpcError = map[string]any{"code": -32601, "message": "unexpected RPC (no prompts allowed)"}
		}
		response := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if rpcError != nil {
			response["error"] = rpcError
		} else {
			response["result"] = result
		}
		enc.Encode(response)
	}
	os.Exit(0)
}
func configTestAgent(t *testing.T) (*Agent, string) {
	t.Helper()
	audit := filepath.Join(t.TempDir(), "audit")
	agent, err := New(map[string]any{"cmd": os.Args[0], "args": []string{"-test.run=^TestACPConfigServer$"}, "work_dir": t.TempDir(), "env": map[string]string{"CC_ACP_CONFIG_HELPER": "1", "CC_ACP_CONFIG_AUDIT": audit}})
	if err != nil {
		t.Fatal(err)
	}
	a := agent.(*Agent)
	t.Cleanup(func() { a.Stop() })
	return a, audit
}
func TestAgent_ConfigBeforeChatReuseAndResume(t *testing.T) {
	a, audit := configTestAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.PrepareRuntimeConfig(ctx); err != nil {
		t.Fatal(err)
	}
	if len(a.AvailableModels(ctx)) != 3 || len(a.AvailableReasoningEfforts()) != 2 || len(a.PermissionModes()) != 0 {
		t.Fatal("wrong advertised capabilities")
	}
	original := a.configSession
	if err := a.SetRuntimeConfig(ctx, "thought_level", "high"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetRuntimeConfig(ctx, "model", "fast"); err != nil {
		t.Fatal(err)
	}
	if got := a.AvailableReasoningEfforts(); len(got) != 0 || a.GetReasoningEffort() != "" {
		t.Fatalf("stale reasoning: %v %s", got, a.GetReasoningEffort())
	}
	if err := a.SetRuntimeConfig(ctx, "model", "smart"); err != nil {
		t.Fatal(err)
	}
	if a.GetReasoningEffort() != "low" {
		t.Fatal("server default was not adopted")
	}
	if err := a.SetRuntimeConfig(ctx, "thought_level", "high"); err != nil {
		t.Fatal(err)
	}
	cancel() // A menu's request context must not kill the reserved connection.
	if !original.Alive() {
		t.Fatal("menu context killed warm session")
	}
	session, err := a.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if session != original {
		t.Fatal("fresh chat did not reuse reserved session")
	}
	_ = session.Close()
	_ = session.Close()
	resumed, err := a.StartSession(context.Background(), "existing-chat")
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if resumed.CurrentSessionID() != "existing-chat" {
		t.Fatal("load without sessionId created a new conversation")
	}
	m, _ := resumed.(*acpSession).option("model")
	r, _ := resumed.(*acpSession).option("thought_level")
	if m.CurrentValue != "smart" || r.CurrentValue != "high" {
		t.Fatalf("preferences not restored: %+v %+v", m, r)
	}
	raw, _ := os.ReadFile(audit)
	if strings.Count(string(raw), "session/new") != 1 || strings.Contains(string(raw), "session/prompt") {
		t.Fatalf("unnecessary sessions/prompts: %s", raw)
	}
}
func TestAgent_ConfigRejectedKeepsSelection(t *testing.T) {
	a, _ := configTestAgent(t)
	ctx := context.Background()
	if err := a.PrepareRuntimeConfig(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.SetRuntimeConfig(ctx, "model", "denied"); err == nil || !strings.Contains(err.Error(), "selection denied") {
		t.Fatal(err)
	}
	if a.GetModel() != "smart" {
		t.Fatal("failed selection reported as current")
	}
	if err := a.SetRuntimeConfig(ctx, "mode", "auto"); err == nil {
		t.Fatal("invented mode support")
	}
}
func TestAgent_ConfigConcurrentDiscoveryAndShutdown(t *testing.T) {
	a, audit := configTestAgent(t)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := a.PrepareRuntimeConfig(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	s := a.configSession
	if err := a.Stop(); err != nil {
		t.Fatal(err)
	}
	if s.Alive() {
		t.Fatal("warm subprocess leaked")
	}
	if err := a.PrepareRuntimeConfig(context.Background()); err == nil {
		t.Fatal("restarted after stop")
	}
	raw, _ := os.ReadFile(audit)
	if strings.Count(string(raw), "session/new") != 1 {
		t.Fatal(string(raw))
	}
}

func TestAgent_ConfigNotificationUpdatesGetters(t *testing.T) {
	a, _ := configTestAgent(t)
	if err := a.PrepareRuntimeConfig(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.SetRuntimeConfig(context.Background(), "thought_level", "high"); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"sessionId": a.configSession.CurrentSessionID(), "update": map[string]any{"sessionUpdate": "config_option_update", "configOptions": testConfigOptions("fast", "")}})
	a.configSession.absorbConfigUpdate(raw)
	if got := a.GetReasoningEffort(); got != "" {
		t.Fatalf("stale effort %q", got)
	}
	if got := a.GetModel(); got != "fast" {
		t.Fatalf("stale model %q", got)
	}
}

func TestSession_ConfigLegacyModelFallbackAndModernMode(t *testing.T) {
	for _, tc := range []struct {
		name, category, method, key, value string
		modern                             bool
	}{
		{"legacy model", "model", "session/set_model", "modelId", "legacy-b", false},
		{"config mode", "mode", "session/set_config_option", "configId", "permission_policy", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, w, r := newTestSession(t, nil)
			target := "legacy-b"
			if tc.modern {
				target = "plan"
				if err := s.absorbConfigOptions(json.RawMessage(`[{"id":"permission_policy","category":"mode","type":"select","currentValue":"code","options":[{"value":"code"},{"value":"plan"}]}]`)); err != nil {
					t.Fatal(err)
				}
			} else {
				raw := json.RawMessage(`{"models":{"currentModelId":"legacy-a","availableModels":[{"modelId":"legacy-a","name":"A"},{"modelId":"legacy-b","name":"B"}]}}`)
				var result sessionConfigResult
				_ = json.Unmarshal(raw, &result)
				_ = s.absorbSessionConfig(result)
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				sc := bufio.NewScanner(r)
				if !sc.Scan() {
					return
				}
				var req struct {
					ID     json.RawMessage   `json:"id"`
					Method string            `json:"method"`
					Params map[string]string `json:"params"`
				}
				_ = json.Unmarshal(sc.Bytes(), &req)
				if req.Method != tc.method || req.Params[tc.key] != tc.value {
					t.Errorf("unexpected request: %+v", req)
				}
				result := `{}`
				if tc.modern {
					result = `{"configOptions":[{"id":"permission_policy","category":"mode","type":"select","currentValue":"plan","options":[{"value":"code"},{"value":"plan"}]}]}`
				}
				fmt.Fprintf(w, "{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":%s}\n", req.ID, result)
			}()
			if err := s.setConfig(context.Background(), tc.category, target); err != nil {
				t.Fatal(err)
			}
			<-done
			o, _ := s.option(tc.category)
			if o.CurrentValue != target {
				t.Fatal(o)
			}
			if !tc.modern {
				_ = s.absorbConfigOptions(json.RawMessage(`[]`))
				if _, ok := s.option("model"); ok {
					t.Fatal("legacy model survived authoritative empty config snapshot")
				}
			}
		})
	}
}
