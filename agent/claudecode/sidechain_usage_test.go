package claudecode

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// sidechainLineJSON is one assistant line of a subagent transcript, shaped
// like what claude 2.1.280 writes.
func sidechainLineJSON(requestID, model string, in, out, cacheCreate, cacheRead int) string {
	return fmt.Sprintf(`{"type":"assistant","isSidechain":true,"agentId":"a1","requestId":%q,`+
		`"message":{"id":"msg_%s","model":%q,"stop_reason":"tool_use","usage":{"input_tokens":%d,"output_tokens":%d,`+
		`"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d}}}`,
		requestID, requestID, model, in, out, cacheCreate, cacheRead)
}

func appendLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	for _, line := range lines {
		if _, err := f.WriteString(line + "\n"); err != nil {
			t.Fatal(err)
		}
	}
}

// sessionTranscript lays out <project>/<sid>.jsonl and returns its path and
// the session's subagents directory.
func sessionTranscript(t *testing.T) (string, string) {
	t.Helper()
	project := t.TempDir()
	transcript := filepath.Join(project, "sess-1.jsonl")
	appendLines(t, transcript, `{"type":"user"}`)
	return transcript, subagentsDir(transcript)
}

func TestSidechainMeterCountsAgentToolAndWorkflowAgents(t *testing.T) {
	_, dir := sessionTranscript(t)
	// An Agent tool subagent; one response written as two content-block
	// lines with the same requestId, the second with the final output count.
	appendLines(t, filepath.Join(dir, "agent-aaa.jsonl"),
		`{"type":"user","isSidechain":true,"message":{"content":"go"}}`,
		sidechainLineJSON("req_1", "claude-haiku-4-5", 10, 1, 17596, 0),
		sidechainLineJSON("req_1", "claude-haiku-4-5", 10, 47, 17596, 0),
	)
	// Two workflow agents, plus the files a workflow run keeps beside them.
	run := filepath.Join(dir, "workflows", "wf_1")
	appendLines(t, filepath.Join(run, "agent-bbb.jsonl"), sidechainLineJSON("req_2", "claude-haiku-4-5", 10, 49, 15472, 0))
	appendLines(t, filepath.Join(run, "agent-ccc.jsonl"), sidechainLineJSON("req_3", "claude-opus-4-8", 10, 40, 0, 15472))
	appendLines(t, filepath.Join(run, "journal.jsonl"), sidechainLineJSON("req_journal", "x", 1000, 1000, 1000, 1000))
	appendLines(t, filepath.Join(run, "agent-bbb.meta.json"), `{"agentType":"workflow-subagent"}`)

	got := newSidechainMeter().collect(dir)
	want := &core.SidechainUsage{
		Requests:                 3,
		InputTokens:              30,
		OutputTokens:             47 + 49 + 40,
		CacheCreationInputTokens: 17596 + 15472,
		CacheReadInputTokens:     15472,
		Models:                   []string{"claude-haiku-4-5", "claude-opus-4-8"},
		ByModel: map[string]core.TokenUsage{
			"claude-haiku-4-5": {InputTokens: 20, OutputTokens: 96, CacheCreationInputTokens: 33068},
			"claude-opus-4-8":  {InputTokens: 10, OutputTokens: 40, CacheReadInputTokens: 15472},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("collect = %+v, want %+v", got, want)
	}
}

func TestSidechainMeterReportsEachCallOnce(t *testing.T) {
	_, dir := sessionTranscript(t)
	path := filepath.Join(dir, "workflows", "wf_1", "agent-aaa.jsonl")
	appendLines(t, path, sidechainLineJSON("req_1", "m", 10, 1, 100, 0))
	m := newSidechainMeter()
	if got := m.collect(dir); got == nil || got.Requests != 1 || got.OutputTokens != 1 {
		t.Fatalf("first collect = %+v", got)
	}
	if got := m.collect(dir); got != nil {
		t.Fatalf("nothing new was written, collect = %+v", got)
	}
	// The same request grows (its last content block), and a new one starts.
	appendLines(t, path,
		sidechainLineJSON("req_1", "m", 10, 30, 100, 0),
		sidechainLineJSON("req_2", "m", 5, 7, 0, 110),
	)
	got := m.collect(dir)
	want := &core.SidechainUsage{Requests: 1, InputTokens: 5, OutputTokens: 29 + 7, CacheReadInputTokens: 110, Models: []string{"m"}}
	want.ByModel = map[string]core.TokenUsage{"m": {InputTokens: 5, OutputTokens: 36, CacheReadInputTokens: 110}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("second collect = %+v, want %+v", got, want)
	}
}

func TestSidechainMeterWaitsForAPartialLine(t *testing.T) {
	_, dir := sessionTranscript(t)
	path := filepath.Join(dir, "agent-aaa.jsonl")
	line := sidechainLineJSON("req_1", "m", 10, 20, 30, 40)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(line[:len(line)/2]), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newSidechainMeter()
	if got := m.collect(dir); got != nil {
		t.Fatalf("a half-written line must not be read, collect = %+v", got)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line[len(line)/2:] + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if got := m.collect(dir); got == nil || got.Requests != 1 || got.CacheReadInputTokens != 40 {
		t.Fatalf("completed line, collect = %+v", got)
	}
}

func TestSidechainMeterBaselineSkipsWhatAPreviousProcessMetered(t *testing.T) {
	_, dir := sessionTranscript(t)
	path := filepath.Join(dir, "agent-aaa.jsonl")
	appendLines(t, path, sidechainLineJSON("req_old", "m", 10, 20, 30, 40))
	m := newSidechainMeter()
	m.baseline(dir)
	if got := m.collect(dir); got != nil {
		t.Fatalf("baselined usage reported again: %+v", got)
	}
	appendLines(t, path, sidechainLineJSON("req_new", "m", 1, 2, 3, 4))
	if got := m.collect(dir); got == nil || got.Requests != 1 || got.InputTokens != 1 {
		t.Fatalf("usage after the baseline, collect = %+v", got)
	}
}

func TestSidechainMeterWithoutSubagents(t *testing.T) {
	_, dir := sessionTranscript(t)
	if got := newSidechainMeter().collect(dir); got != nil {
		t.Fatalf("no subagents directory, collect = %+v", got)
	}
	if got := newSidechainMeter().collect(""); got != nil {
		t.Fatalf("unknown transcript, collect = %+v", got)
	}
	var nilMeter *sidechainMeter
	if got := nilMeter.collect(dir); got != nil {
		t.Fatalf("nil meter, collect = %+v", got)
	}
}

// The turn's own fields stay the main loop's: the engine's context indicator
// reads them, and subagents run in context windows of their own.
func TestHandleResultAttachesSubagentUsage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transcript, dir := sessionTranscript(t)
	appendLines(t, filepath.Join(dir, "workflows", "wf_1", "agent-aaa.jsonl"),
		sidechainLineJSON("req_1", "m", 10, 47, 17596, 0))

	cs := &claudeSession{events: make(chan core.Event, 8), ctx: ctx, workDir: t.TempDir(), sidechain: newSidechainMeter()}
	cs.sessionID.Store("sess-1")
	cs.alive.Store(true)
	cs.transcriptOverride = transcript

	cs.handleResult(map[string]any{"type": "result", "subtype": "compact", "usage": map[string]any{"input_tokens": float64(1)}})
	if evt := <-cs.events; evt.Sidechain != nil {
		t.Fatalf("a compaction is not the end of the turn, Sidechain = %+v", evt.Sidechain)
	}

	cs.handleResult(map[string]any{
		"type": "result", "result": "done", "session_id": "sess-1",
		"usage": map[string]any{"input_tokens": float64(18), "output_tokens": float64(219)},
	})
	evt := <-cs.events
	if evt.InputTokens != 18 || evt.OutputTokens != 219 {
		t.Fatalf("main-loop usage changed: in=%d out=%d", evt.InputTokens, evt.OutputTokens)
	}
	if evt.Sidechain == nil || evt.Sidechain.Requests != 1 || evt.Sidechain.CacheCreationInputTokens != 17596 {
		t.Fatalf("Sidechain = %+v", evt.Sidechain)
	}
}

// Behind an Anthropic-compatible gateway a response's lines before the last
// are provisional and there is no requestId; written as qwen3.8-max via
// ai-gateway wrote them.
func TestSidechainMeterCountsOnlyTheFinalLineOfAGatewayResponse(t *testing.T) {
	_, dir := sessionTranscript(t)
	path := filepath.Join(dir, "workflows", "wf_1", "agent-aaa.jsonl")
	provisional := `{"type":"assistant","isSidechain":true,"message":{"id":"msg_q1","model":"qwen3.8-max",` +
		`"stop_reason":null,"usage":{"input_tokens":18139,"output_tokens":0}}}`
	final := `{"type":"assistant","isSidechain":true,"message":{"id":"msg_q1","model":"qwen3.8-max",` +
		`"stop_reason":"tool_use","usage":{"input_tokens":6,"output_tokens":228,` +
		`"cache_creation_input_tokens":6935,"cache_read_input_tokens":12010}}}`
	m := newSidechainMeter()
	appendLines(t, path, provisional, provisional)
	if got := m.collect(dir); got != nil {
		t.Fatalf("provisional lines must not be billed, collect = %+v", got)
	}
	appendLines(t, path, final)
	got := m.collect(dir)
	want := &core.SidechainUsage{Requests: 1, InputTokens: 6, OutputTokens: 228,
		CacheCreationInputTokens: 6935, CacheReadInputTokens: 12010, Models: []string{"qwen3.8-max"}}
	want.ByModel = map[string]core.TokenUsage{"qwen3.8-max": {InputTokens: 6, OutputTokens: 228, CacheCreationInputTokens: 6935, CacheReadInputTokens: 12010}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("collect = %+v, want %+v", got, want)
	}
}

// A resumed process carries on from the meter its predecessor saved: what
// the subagents wrote after the last report -- a background workflow that
// outlived the process -- is billed, and nothing is billed twice.
func TestSidechainMeterResumesFromItsSavedState(t *testing.T) {
	_, dir := sessionTranscript(t)
	path := filepath.Join(dir, "workflows", "wf_1", "agent-aaa.jsonl")
	state := sidechainStatePath(t.TempDir(), "sess-1")

	first := newSidechainMeter()
	if first.persistTo(state) {
		t.Fatal("no state was saved yet")
	}
	appendLines(t, path, sidechainLineJSON("req_1", "m", 10, 20, 30, 40))
	if got := first.collect(dir); got == nil || got.Requests != 1 {
		t.Fatalf("first process, collect = %+v", got)
	}
	// Written after the first process's last report; then it is replaced.
	appendLines(t, path, sidechainLineJSON("req_2", "m", 1, 2, 3, 4))

	second := newSidechainMeter()
	if !second.persistTo(state) {
		t.Fatal("the saved state was not found")
	}
	got := second.collect(dir)
	want := &core.SidechainUsage{Requests: 1, InputTokens: 1, OutputTokens: 2, CacheCreationInputTokens: 3, CacheReadInputTokens: 4, Models: []string{"m"}}
	want.ByModel = map[string]core.TokenUsage{"m": {InputTokens: 1, OutputTokens: 2, CacheCreationInputTokens: 3, CacheReadInputTokens: 4}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resumed process, collect = %+v, want %+v", got, want)
	}
	if sidechainStatePath("", "sess-1") != "" || sidechainStatePath("/d", "") != "" {
		t.Fatal("a state path needs both the data directory and the session")
	}
}
