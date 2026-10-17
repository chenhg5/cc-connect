package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestParseConfiguredModels(t *testing.T) {
	got := parseConfiguredModels([]any{
		map[string]any{"model": "claude-opus", "alias": "opus"},
		map[string]any{"value": "swe-1-7", "name": "SWE-1.7"},
		"haiku",
		map[string]string{"name": "plain", "desc": "Plain"},
	})
	if len(got) != 4 {
		t.Fatalf("got %#v", got)
	}
	if got[0].Name != "claude-opus" || got[0].Alias != "opus" {
		t.Fatalf("provider shape: %#v", got[0])
	}
	if got[1].Name != "swe-1-7" || got[1].Desc != "SWE-1.7" {
		t.Fatalf("acp shape: %#v", got[1])
	}
	if got[2].Name != "haiku" || got[3].Name != "plain" || got[3].Desc != "Plain" {
		t.Fatalf("rest: %#v", got[2:])
	}
	if parseConfiguredModels(nil) != nil {
		t.Fatal("nil should stay nil")
	}
}

func TestModelSwitcher_CacheThenConfigThenEmpty(t *testing.T) {
	a, err := New(map[string]any{
		"command": "true",
		"model":   "opus",
		"models":  []any{map[string]any{"model": "opus", "alias": "o"}, "haiku"},
	})
	if err != nil {
		t.Fatal(err)
	}
	agent := a.(*Agent)
	if agent.GetModel() != "opus" {
		t.Fatalf("GetModel = %q", agent.GetModel())
	}
	models := agent.AvailableModels(context.Background())
	if len(models) != 2 || models[0].Name != "opus" || models[1].Name != "haiku" {
		t.Fatalf("configured models = %#v", models)
	}
	agent.reportModelOptions("live", []core.ModelOption{{Name: "from-session"}})
	models = agent.AvailableModels(context.Background())
	if len(models) != 1 || models[0].Name != "from-session" {
		t.Fatalf("cached models = %#v", models)
	}
	agent.SetModel("next")
	if agent.GetModel() != "next" {
		t.Fatalf("SetModel GetModel = %q", agent.GetModel())
	}

	snap := agent.WorkspaceAgentOptions()
	if snap["model"] != "next" {
		t.Fatalf("snapshot model = %#v", snap["model"])
	}
	round, err := New(snap)
	if err != nil {
		t.Fatal(err)
	}
	roundAgent := round.(*Agent)
	if roundAgent.GetModel() != "next" {
		t.Fatalf("round-trip model = %q", roundAgent.GetModel())
	}
	roundModels := roundAgent.AvailableModels(context.Background())
	if len(roundModels) != 2 || roundModels[0].Name != "opus" || roundModels[0].Alias != "o" || roundModels[1].Name != "haiku" {
		t.Fatalf("round-trip models = %#v", roundModels)
	}
	// Fresh agent with neither cache nor config returns nothing.
	empty := &Agent{}
	if got := empty.AvailableModels(context.Background()); len(got) != 0 {
		t.Fatalf("empty AvailableModels = %#v", got)
	}
}

func TestSessionDeleteSupported(t *testing.T) {
	var with acpInitializeResult
	if err := json.Unmarshal([]byte(`{"agentCapabilities":{"sessionCapabilities":{"delete":{}}}}`), &with); err != nil {
		t.Fatal(err)
	}
	if !sessionDeleteSupported(&with) {
		t.Fatal("empty object should count as supported")
	}
	var without acpInitializeResult
	if err := json.Unmarshal([]byte(`{"agentCapabilities":{"sessionCapabilities":{}}}`), &without); err != nil {
		t.Fatal(err)
	}
	if sessionDeleteSupported(&without) {
		t.Fatal("absent delete must be unsupported")
	}
	var nullCap acpInitializeResult
	if err := json.Unmarshal([]byte(`{"agentCapabilities":{"sessionCapabilities":{"delete":null}}}`), &nullCap); err != nil {
		t.Fatal(err)
	}
	if sessionDeleteSupported(&nullCap) {
		t.Fatal("null delete must be unsupported")
	}
	if sessionLoadSupported(nil) || sessionLoadSupported(&without) {
		t.Fatal("missing loadSession must be unsupported")
	}
	var loadable acpInitializeResult
	if err := json.Unmarshal([]byte(`{"agentCapabilities":{"loadSession":true}}`), &loadable); err != nil {
		t.Fatal(err)
	}
	if !sessionLoadSupported(&loadable) {
		t.Fatal("loadSession true must be supported")
	}
}

func TestDeleteSession_ShortCircuits(t *testing.T) {
	a := &Agent{}
	if err := a.DeleteSession(context.Background(), "  "); err == nil || !strings.Contains(err.Error(), "session id") {
		t.Fatalf("empty id: %v", err)
	}
	a.deleteUnsupported.Store(true)
	err := a.DeleteSession(context.Background(), "abc")
	if !errors.Is(err, errACPDeleteUnsupported) {
		t.Fatalf("got %v", err)
	}
}

func TestDeleteSessionOnTransport(t *testing.T) {
	rResp, wResp := io.Pipe()
	rReq, wReq := io.Pipe()
	tr := newTransport(rResp, wReq, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tr.readLoop(ctx)
	defer func() { _ = wResp.Close() }()
	defer func() { _ = rReq.Close() }()

	go func() {
		sc := bufio.NewScanner(rReq)
		for sc.Scan() {
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params struct {
					SessionID string `json:"sessionId"`
				} `json:"params"`
			}
			if json.Unmarshal(sc.Bytes(), &req) != nil {
				continue
			}
			if req.Method != "session/delete" || req.Params.SessionID != "sid-1" {
				_, _ = fmt.Fprintf(wResp, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"no"}}`+"\n", req.ID)
				return
			}
			_, _ = fmt.Fprintf(wResp, `{"jsonrpc":"2.0","id":%s,"result":{}}`+"\n", req.ID)
			return
		}
	}()
	if err := deleteSessionOnTransport(ctx, tr, "sid-1"); err != nil {
		t.Fatal(err)
	}
}

func TestValidateSessionID(t *testing.T) {
	a := &Agent{}
	if a.ValidateSessionID(context.Background(), "") || a.ValidateSessionID(context.Background(), " ") {
		t.Fatal("empty id must be false")
	}
	a.listUnsupported.Store(true)
	if !a.ValidateSessionID(context.Background(), "keep-me") {
		t.Fatal("unsupported list must not destroy the session")
	}
	if !containsSessionID([]core.AgentSessionInfo{{ID: "keep-me"}}, "keep-me") {
		t.Fatal("contains")
	}
	if containsSessionID([]core.AgentSessionInfo{{ID: "other"}}, "keep-me") {
		t.Fatal("missing id must be false")
	}

	// A probe that cannot even start is a transient failure: keep the id.
	broken := &Agent{cmd: "acp-binary-does-not-exist-xyz", workDir: t.TempDir()}
	if !broken.ValidateSessionID(context.Background(), "keep-me") {
		t.Fatal("probe error must return true")
	}
}

func TestCanonicalCwdAndSessionList(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	resolved := canonicalCwd(link)
	if resolved != canonicalCwd(real) {
		t.Fatalf("link %q real %q", resolved, canonicalCwd(real))
	}
	entries := []acpSessionListEntry{
		{SessionID: "a", Cwd: resolved, Title: "T"},
		{SessionID: "b", Cwd: filepath.Join(resolved, "other")},
	}
	got := convertSessionList(entries, link)
	if len(got) != 1 || got[0].ID != "a" || got[0].Summary != "T" {
		t.Fatalf("got %#v", got)
	}
	agent := &Agent{}
	agent.rememberTitles(entries)
	if agent.GetSessionTitle("a") != "T" || agent.GetSessionTitle("missing") != "" {
		t.Fatalf("titles = %#v", agent.titles)
	}
}

func TestMapSessionUpdate_QuietKindsDoNotLeakText(t *testing.T) {
	kinds := []string{
		"available_commands_update",
		"config_option_update",
		"session_info_update",
		"usage_update",
		"current_mode_update",
		"_cognition.ai/compaction",
	}
	for _, kind := range kinds {
		params := fmt.Sprintf(`{"sessionId":"s","update":{"sessionUpdate":%q,"text":"leak","message":"leak","content":{"text":"leak"},"title":"Hello"}}`, kind)
		if evs := mapSessionUpdate("s", json.RawMessage(params)); len(evs) != 0 {
			t.Fatalf("%s leaked %+v", kind, evs)
		}
	}
}

func TestUsage_UpdateAndPromptResult(t *testing.T) {
	s := &acpSession{}
	if s.GetContextUsage() != nil {
		t.Fatal("usage must be nil until observed")
	}
	if s.GetUsageSource() != "event" {
		t.Fatalf("source = %q", s.GetUsageSource())
	}
	s.absorbUsageUpdate(json.RawMessage(`{
		"sessionUpdate":"usage_update",
		"used":14411,
		"size":202752,
		"_meta":{"cognition.ai/inputTokens":1,"cognition.ai/outputTokens":2,"cognition.ai/cachedReadTokens":3}
	}`))
	u := s.GetContextUsage()
	if u == nil || u.UsedTokens != 14411 || u.ContextWindow != 202752 || u.InputTokens != 1 || u.OutputTokens != 2 || u.CachedInputTokens != 3 {
		t.Fatalf("after usage_update: %+v", u)
	}
	in, out, cacheRead := s.absorbPromptResult(json.RawMessage(`{
		"stopReason":"end_turn",
		"usage":{"totalTokens":14411,"inputTokens":14388,"outputTokens":23,"cachedReadTokens":9600,"cachedWriteTokens":4,"thoughtTokens":5}
	}`))
	if in != 14388 || out != 23 || cacheRead != 9600 {
		t.Fatalf("result tokens = %d %d %d", in, out, cacheRead)
	}
	u = s.GetContextUsage()
	if u.UsedTokens != 14411 || u.ContextWindow != 202752 {
		t.Fatalf("usage_update fields lost: %+v", u)
	}
	if u.InputTokens != 14388 || u.OutputTokens != 23 || u.CachedInputTokens != 9600 {
		t.Fatalf("prompt usage not applied: %+v", u)
	}
	if u.CacheCreationInputTokens != 4 || u.ReasoningOutputTokens != 5 || u.TotalTokens != 14411 {
		t.Fatalf("extended usage: %+v", u)
	}

	only := &acpSession{}
	only.absorbPromptResult(json.RawMessage(`{"usage":{"totalTokens":9,"inputTokens":7,"outputTokens":2}}`))
	if got := only.GetContextUsage(); got == nil || got.UsedTokens != 9 || got.InputTokens != 7 {
		t.Fatalf("prompt-only backfill: %+v", got)
	}
}

func TestBuildPromptBlocks_NativeImages(t *testing.T) {
	img := core.ImageAttachment{MimeType: "image/png", Data: []byte("hi")}
	blocks := buildPromptBlocks("look", []core.ImageAttachment{img}, true)
	if len(blocks) != 2 {
		t.Fatalf("blocks = %#v", blocks)
	}
	image := blocks[1].(map[string]any)
	if image["type"] != "image" || image["mimeType"] != "image/png" || image["data"] != base64.StdEncoding.EncodeToString([]byte("hi")) {
		t.Fatalf("image block = %#v", image)
	}
	bare := buildPromptBlocks("", []core.ImageAttachment{{Data: []byte("x")}}, true)
	if len(bare) != 1 || bare[0].(map[string]any)["mimeType"] != "image/png" {
		t.Fatalf("empty mime = %#v", bare)
	}
	pathBlocks := buildPromptBlocks("look", []core.ImageAttachment{img}, false)
	if len(pathBlocks) != 1 {
		t.Fatalf("fallback blocks = %#v", pathBlocks)
	}
	if _, ok := pathBlocks[0].(map[string]any)["data"]; ok {
		t.Fatal("path fallback must not embed image bytes")
	}
}

func TestHistoryMergeAndLoad(t *testing.T) {
	if historyLoadGrace < 200*time.Millisecond || historyLoadGrace > time.Second {
		t.Fatalf("historyLoadGrace = %s, want about 300ms", historyLoadGrace)
	}
	ts := "2026-10-15T13:50:30.568222+00:00"
	parsed, ok := parseMetaTimestamp(json.RawMessage(`{"_meta":{"cognition.ai/timestamp":"` + ts + `"}}`))
	if !ok {
		t.Fatal("timestamp not parsed")
	}
	wantTS, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.Equal(wantTS.Local()) {
		t.Fatalf("ts = %s, want %s", parsed, wantTS.Local())
	}

	user := func(text, stamp string) historyChunk {
		ch, ok := historyChunkFromUpdate("sid", json.RawMessage(fmt.Sprintf(
			`{"sessionId":"sid","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":%q},"_meta":{"cognition.ai/timestamp":%q}}}`,
			text, stamp)))
		if !ok {
			t.Fatalf("user chunk %q not parsed", text)
		}
		return ch
	}
	assistant, ok := historyChunkFromUpdate("sid", json.RawMessage(`{"sessionId":"sid","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"yo"}}}`))
	if !ok {
		t.Fatal("assistant chunk")
	}
	if _, ok := historyChunkFromUpdate("sid", json.RawMessage(`{"sessionId":"sid","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"secret"}}}`)); ok {
		t.Fatal("thought must be ignored")
	}
	if _, ok := historyChunkFromUpdate("sid", json.RawMessage(`{"sessionId":"other","update":{"sessionUpdate":"user_message_chunk","content":{"text":"no"}}}`)); ok {
		t.Fatal("other session must be ignored")
	}
	merged := mergeHistoryChunks([]historyChunk{user("hel", ts), user("lo", ts), assistant})
	if len(merged) != 2 || merged[0].Role != "user" || merged[0].Content != "hello" || merged[1].Content != "yo" {
		t.Fatalf("merged = %#v", merged)
	}
	if !merged[0].Timestamp.Equal(wantTS.Local()) {
		t.Fatalf("merged ts = %s", merged[0].Timestamp)
	}
	limited := limitHistory(merged, 1)
	if len(limited) != 1 || limited[0].Role != "assistant" {
		t.Fatalf("limit = %#v", limited)
	}

	entries := loadHistoryFromScript(t, 0, false)
	if len(entries) != 2 || entries[0].Content != "hello" || entries[1].Role != "assistant" {
		t.Fatalf("loaded = %#v", entries)
	}
	late := loadHistoryFromScript(t, 400*time.Millisecond, true)
	if len(late) != 3 || late[2].Content != "later" {
		t.Fatalf("grace load = %#v", late)
	}

	agent := &Agent{}
	if _, err := agent.GetSessionHistory(context.Background(), " ", 0); err == nil {
		t.Fatal("empty id should error")
	}
}

func loadHistoryFromScript(t *testing.T, grace time.Duration, lateChunk bool) []core.HistoryEntry {
	t.Helper()
	rResp, wResp := io.Pipe()
	rReq, wReq := io.Pipe()
	col := &historyCollector{wantID: "sid"}
	tr := newTransport(rResp, wReq, col.onNotification, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = wResp.Close()
		_ = rReq.Close()
	})
	go tr.readLoop(ctx)

	go func() {
		sc := bufio.NewScanner(rReq)
		if !sc.Scan() {
			return
		}
		var req struct {
			ID json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(sc.Bytes(), &req)
		writeUpdate := func(kind, text string) {
			_, _ = fmt.Fprintf(wResp, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sid","update":{"sessionUpdate":%q,"content":{"type":"text","text":%q}}}}`+"\n", kind, text)
		}
		writeUpdate("user_message_chunk", "hel")
		writeUpdate("user_message_chunk", "lo")
		writeUpdate("agent_thought_chunk", "secret")
		writeUpdate("tool_call", "nope")
		writeUpdate("agent_message_chunk", "answer")
		_, _ = fmt.Fprintf(wResp, `{"jsonrpc":"2.0","id":%s,"result":{}}`+"\n", req.ID)
		if lateChunk {
			time.Sleep(30 * time.Millisecond)
			writeUpdate("user_message_chunk", "later")
		}
	}()
	entries, err := loadHistory(ctx, tr, col, "sid", t.TempDir(), grace)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestNotifications_ConfigUsageTitleAndVendor(t *testing.T) {
	agent := &Agent{}
	s, wResp, _ := newTestSession(t, agent)
	_, _ = fmt.Fprint(wResp, `{"jsonrpc":"2.0","method":"_cognition.ai/compaction","params":{"text":"leak"}}`+"\n")
	_, _ = fmt.Fprint(wResp, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"config_option_update","text":"leak","configOptions":[{"id":"model","category":"model","currentValue":"swe-1-7-lightning-medium","options":[{"value":"swe-1-7-lightning-medium","name":"SWE"}]}]}}}`+"\n")
	_, _ = fmt.Fprint(wResp, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"session_info_update","title":"Hello","text":"leak"}}}`+"\n")
	_, _ = fmt.Fprint(wResp, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"usage_update","used":10,"size":20,"text":"leak","_meta":{"cognition.ai/inputTokens":4}}}}`+"\n")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		u := s.GetContextUsage()
		if s.GetModel() == "swe-1-7-lightning-medium" && agent.GetSessionTitle("s1") == "Hello" && u != nil && u.UsedTokens == 10 && u.ContextWindow == 20 && u.InputTokens == 4 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if s.GetModel() != "swe-1-7-lightning-medium" {
		t.Fatalf("model = %q", s.GetModel())
	}
	if agent.GetSessionTitle("s1") != "Hello" {
		t.Fatalf("title = %q", agent.GetSessionTitle("s1"))
	}
	models := agent.AvailableModels(context.Background())
	if len(models) != 1 || models[0].Name != "swe-1-7-lightning-medium" || models[0].Desc != "SWE" {
		t.Fatalf("models = %#v", models)
	}
	u := s.GetContextUsage()
	if u == nil || u.UsedTokens != 10 || u.ContextWindow != 20 || u.InputTokens != 4 {
		t.Fatalf("usage = %+v", u)
	}
	select {
	case ev := <-s.events:
		t.Fatalf("quiet notification leaked %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHandshake_LoadKeepsIDAndAppliesModel(t *testing.T) {
	agent := &Agent{}
	s, wResp, rReq := newTestSession(t, agent)
	s.setACPSessionID("mire-tortellini")
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	s.workDir = link
	wantCwd := canonicalCwd(link)

	var mu sync.Mutex
	var methods []string
	var loadCwd, setValue, setConfigID string
	go scriptACP(t, rReq, wResp, func(method string, params json.RawMessage) (string, string) {
		mu.Lock()
		methods = append(methods, method)
		mu.Unlock()
		switch method {
		case "initialize":
			return `{"protocolVersion":1,"agentCapabilities":{"loadSession":true,"promptCapabilities":{"image":true},"sessionCapabilities":{"list":{},"delete":{}}}}`, ""
		case "session/load":
			var p struct {
				Cwd string `json:"cwd"`
			}
			_ = json.Unmarshal(params, &p)
			mu.Lock()
			loadCwd = p.Cwd
			mu.Unlock()
			// One line: the ACP transport is newline-delimited JSON-RPC.
			return `{"modes":{"currentModeId":"accept-edits","availableModes":[{"id":"accept-edits","name":"Code"},{"id":"ask","name":"Ask"}]},"configOptions":[{"id":"mode","category":"mode","currentValue":"accept-edits","options":[{"value":"accept-edits","name":"Code"}]},{"id":"model","name":"Model","category":"model","currentValue":"claude-opus-5-5-medium","options":[{"value":"adaptive","name":"Adaptive"},{"value":"swe-1-7-lightning-medium","name":"SWE-1.7 Lightning"},{"value":"claude-opus-5-5-medium","name":"Claude Opus 5.5"}]}]}`, ""
		case "session/set_mode":
			return `{}`, ""
		case "session/set_config_option":
			var p struct {
				ConfigID string `json:"configId"`
				Value    string `json:"value"`
			}
			_ = json.Unmarshal(params, &p)
			mu.Lock()
			setConfigID = p.ConfigID
			setValue = p.Value
			mu.Unlock()
			return `{"configOptions":[{"id":"model","category":"model","currentValue":"swe-1-7-lightning-medium","options":[{"value":"swe-1-7-lightning-medium","name":"SWE"}]}]}`, ""
		default:
			return "", "unexpected " + method
		}
	})

	if err := s.handshake("mire-tortellini", ""); err != nil {
		t.Fatal(err)
	}
	if s.currentACPSessionID() != "mire-tortellini" {
		t.Fatalf("session id = %q", s.currentACPSessionID())
	}
	if !s.promptImages {
		t.Fatal("prompt images not enabled")
	}
	if s.GetModel() != "claude-opus-5-5-medium" {
		t.Fatalf("reported model = %q", s.GetModel())
	}
	if loadCwd != wantCwd {
		t.Fatalf("load cwd = %q, want %q", loadCwd, wantCwd)
	}
	if agent.listUnsupported.Load() || agent.deleteUnsupported.Load() {
		t.Fatal("capabilities should be supported")
	}
	models := agent.AvailableModels(context.Background())
	if len(models) != 3 || models[2].Name != "claude-opus-5-5-medium" || models[2].Desc != "Claude Opus 5.5" {
		t.Fatalf("models = %#v", models)
	}

	s.applyStartupPreferences("ask", "swe-1-7-lightning-medium", "")
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(methods, ",") != "initialize,session/load,session/set_mode,session/set_config_option" {
		t.Fatalf("methods = %v", methods)
	}
	if setConfigID != "model" || setValue != "swe-1-7-lightning-medium" {
		t.Fatalf("set_config_option id=%q value=%q", setConfigID, setValue)
	}
	if s.GetModel() != "swe-1-7-lightning-medium" {
		t.Fatalf("model after set = %q", s.GetModel())
	}
}

func TestApplyInitialModel_LegacyAndFailure(t *testing.T) {
	s, wResp, rReq := newTestSession(t, nil)
	s.absorbModelsBlock(&acpModelsBlock{
		CurrentModelID: "old",
		AvailableModels: []acpLegacyModel{
			{ModelID: "old", Name: "Old"},
			{ModelID: "new", Name: "New"},
		},
	})
	go scriptACP(t, rReq, wResp, func(method string, params json.RawMessage) (string, string) {
		if method != "session/set_model" {
			return "", "want set_model, got " + method
		}
		var p struct {
			ModelID string `json:"modelId"`
		}
		_ = json.Unmarshal(params, &p)
		if p.ModelID != "new" {
			return "", "bad model " + p.ModelID
		}
		return `{}`, ""
	})
	s.applyInitialModel("new")
	if s.GetModel() != "new" {
		t.Fatalf("legacy model = %q", s.GetModel())
	}

	s2, w2, r2 := newTestSession(t, nil)
	s2.absorbConfigOptions([]acpConfigOption{{
		ID:           "model",
		Category:     "model",
		CurrentValue: "keep",
		Options:      []acpConfigOptionChoice{{Value: "keep", Name: "Keep"}},
	}})
	var rejected int
	go scriptACP(t, r2, w2, func(method string, _ json.RawMessage) (string, string) {
		if method == "session/set_config_option" {
			rejected++
			return "", "rejected"
		}
		return "", "unexpected " + method
	})
	s2.applyInitialModel("other")
	if s2.GetModel() != "keep" || rejected != 0 {
		t.Fatalf("unlisted model model=%q calls=%d warning=%q", s2.GetModel(), rejected, s2.StartupWarning())
	}
	if !strings.Contains(s2.StartupWarning(), "other") {
		t.Fatalf("warning = %q", s2.StartupWarning())
	}

	// category is absent: id "model" is still the config option.
	// A value outside the advertised list is not sent.
	s3, w3, r3 := newTestSession(t, nil)
	s3.absorbConfigOptions([]acpConfigOption{{
		ID:           "model",
		CurrentValue: "old",
		Options:      []acpConfigOptionChoice{{Value: "old", Name: "Old"}},
	}})
	var calls int
	go scriptACP(t, r3, w3, func(method string, _ json.RawMessage) (string, string) {
		if method == "session/set_config_option" {
			calls++
		}
		return `{}`, ""
	})
	s3.applyInitialModel("claude-opus-5-5-high")
	if calls != 0 || s3.GetModel() != "old" || !strings.Contains(s3.StartupWarning(), "claude-opus-5-5-high") {
		t.Fatalf("calls=%d reported=%q warning=%q", calls, s3.GetModel(), s3.StartupWarning())
	}
}

func TestSend_NativeImageAndUsage(t *testing.T) {
	s, wResp, rReq := newTestSession(t, nil)
	s.promptImages = true
	go scriptACP(t, rReq, wResp, func(method string, params json.RawMessage) (string, string) {
		if method != "session/prompt" {
			return "", "unexpected " + method
		}
		var body struct {
			Prompt []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				MimeType string `json:"mimeType"`
				Data     string `json:"data"`
			} `json:"prompt"`
		}
		if err := json.Unmarshal(params, &body); err != nil {
			return "", err.Error()
		}
		if len(body.Prompt) != 2 || body.Prompt[0].Type != "text" || body.Prompt[0].Text != "see" {
			return "", fmt.Sprintf("text block %#v", body.Prompt)
		}
		if body.Prompt[1].Type != "image" || body.Prompt[1].MimeType != "image/jpeg" || body.Prompt[1].Data != base64.StdEncoding.EncodeToString([]byte("img")) {
			return "", fmt.Sprintf("image block %#v", body.Prompt[1])
		}
		if strings.Contains(string(params), "Image files saved locally") {
			return "", "native image fell back to a path reference"
		}
		return `{"stopReason":"end_turn","usage":{"inputTokens":11,"outputTokens":3,"cachedReadTokens":5}}`, ""
	})
	err := s.Send("see", "m1", []core.ImageAttachment{{MimeType: "image/jpeg", Data: []byte("img")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-s.events:
		if ev.Type != core.EventResult || !ev.Done || ev.InputTokens != 11 || ev.OutputTokens != 3 || ev.CacheReadInputTokens != 5 {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no result event")
	}
}

func TestSend_ImagePathFallbackWhenUnsupported(t *testing.T) {
	s, wResp, rReq := newTestSession(t, nil)
	go scriptACP(t, rReq, wResp, func(method string, params json.RawMessage) (string, string) {
		if method != "session/prompt" {
			return "", "unexpected " + method
		}
		if !bytes.Contains(params, []byte("Image files saved locally")) {
			return "", "missing path reference: " + string(params)
		}
		if bytes.Contains(params, []byte(`"type":"image"`)) || bytes.Contains(params, []byte(`"type": "image"`)) {
			return "", "unsupported agent got an image block"
		}
		return `{}`, ""
	})
	if err := s.Send("see", "m1", []core.ImageAttachment{{MimeType: "image/png", Data: []byte("img")}}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestHandshake_NewSessionUsesCanonicalCwd(t *testing.T) {
	agent := &Agent{}
	s, wResp, rReq := newTestSession(t, agent)
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	s.workDir = link
	want := canonicalCwd(link)
	var got string
	go scriptACP(t, rReq, wResp, func(method string, params json.RawMessage) (string, string) {
		switch method {
		case "initialize":
			return `{"protocolVersion":1,"agentCapabilities":{"loadSession":false}}`, ""
		case "session/new":
			var p struct {
				Cwd string `json:"cwd"`
			}
			_ = json.Unmarshal(params, &p)
			got = p.Cwd
			return `{"sessionId":"fresh","configOptions":[{"id":"model","category":"model","currentValue":"adaptive","options":[{"value":"adaptive","name":"Adaptive"}]}]}`, ""
		default:
			return "", "unexpected " + method
		}
	})
	if err := s.handshake("", ""); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("new cwd = %q, want %q", got, want)
	}
	if s.currentACPSessionID() != "fresh" {
		t.Fatalf("id = %q", s.currentACPSessionID())
	}
	if s.GetModel() != "adaptive" {
		t.Fatalf("new-session model = %q", s.GetModel())
	}
	models := agent.AvailableModels(context.Background())
	if len(models) != 1 || models[0].Name != "adaptive" || models[0].Desc != "Adaptive" {
		t.Fatalf("new-session models = %#v", models)
	}
}

type scriptedRPC func(method string, params json.RawMessage) (result string, errMsg string)

func scriptACP(t *testing.T, rReq *io.PipeReader, wResp *io.PipeWriter, handle scriptedRPC) {
	t.Helper()
	go func() {
		sc := bufio.NewScanner(rReq)
		sc.Buffer(make([]byte, 64*1024), 2*1024*1024)
		for sc.Scan() {
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(sc.Bytes(), &req) != nil || len(bytes.TrimSpace(req.ID)) == 0 {
				continue
			}
			result, errMsg := handle(req.Method, req.Params)
			if errMsg != "" {
				_, _ = fmt.Fprintf(wResp, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32000,"message":%q}}`+"\n", req.ID, errMsg)
				continue
			}
			if result == "" {
				result = "{}"
			}
			var compact bytes.Buffer
			if json.Compact(&compact, []byte(result)) == nil {
				result = compact.String()
			}
			_, _ = fmt.Fprintf(wResp, `{"jsonrpc":"2.0","id":%s,"result":%s}`+"\n", req.ID, result)
		}
	}()
}
