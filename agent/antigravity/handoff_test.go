package antigravity

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// Builders for gemini_coder.Step payloads (field numbers as in handoff.go).

func pbMsg(fields ...[]byte) []byte {
	var b []byte
	for _, f := range fields {
		b = append(b, f...)
	}
	return b
}

func pbField(num protowire.Number, v []byte) []byte {
	b := protowire.AppendTag(nil, num, protowire.BytesType)
	return protowire.AppendBytes(b, v)
}

func pbVarint(num protowire.Number, v uint64) []byte {
	b := protowire.AppendTag(nil, num, protowire.VarintType)
	return protowire.AppendVarint(b, v)
}

func pbTS(num protowire.Number, t time.Time) []byte {
	return pbField(num, pbMsg(pbVarint(tsFieldSeconds, uint64(t.Unix())), pbVarint(tsFieldNanos, uint64(t.Nanosecond()))))
}

var t0 = time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)

func userStep(at time.Time, text string) []byte {
	return pbMsg(
		pbVarint(1, stepTypeUserInput),
		pbField(stepFieldMetadata, pbTS(metaFieldCreatedAt, at)),
		pbField(stepFieldUserInput, pbMsg(
			pbField(userFieldItems, pbField(itemFieldText, []byte(text))),
			pbField(userFieldUserResponse, []byte(text)),
		)),
	)
}

func plannerStep(at time.Time, input, cached uint64, text string) []byte {
	return pbMsg(
		pbVarint(1, stepTypePlannerResponse),
		pbField(stepFieldMetadata, pbMsg(
			pbTS(metaFieldCreatedAt, at.Add(-time.Second)),
			pbTS(metaFieldCompletedAt, at),
			pbField(metaFieldModelUsage, pbMsg(pbVarint(usageFieldInputTokens, input), pbVarint(usageFieldCacheReadTokens, cached))),
		)),
		pbField(stepFieldPlannerResponse, pbMsg(
			pbField(plannerFieldResponse, []byte(text)),
			pbField(plannerFieldModifiedResponse, []byte(text)),
		)),
	)
}

func checkpointStep(at time.Time, summary string) []byte {
	return pbMsg(
		pbVarint(1, stepTypeCheckpoint),
		pbField(stepFieldMetadata, pbTS(metaFieldCreatedAt, at)),
		pbField(stepFieldCheckpoint, pbField(checkpointFieldSessionSummary, []byte(summary))),
	)
}

type fixtureStep struct {
	typ     int
	payload []byte
}

func writeConversationDB(t *testing.T, path string, steps []fixtureStep) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Schema copied from a real agy 1.3.5 conversation database.
	if _, err := db.Exec("CREATE TABLE `steps` (`idx` integer,`step_type` integer NOT NULL DEFAULT 0,`status` integer NOT NULL DEFAULT 0,`has_subtrajectory` numeric NOT NULL DEFAULT false,`metadata` blob,`error_details` blob,`permissions` blob,`task_details` blob,`render_info` blob,`step_payload` blob,`step_format` integer NOT NULL DEFAULT 0,PRIMARY KEY (`idx`))"); err != nil {
		t.Fatal(err)
	}
	for i, s := range steps {
		if _, err := db.Exec("INSERT INTO steps (idx, step_type, status, step_payload) VALUES (?, ?, 3, ?)", i, s.typ, s.payload); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadHandoffDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conv.db")
	toolOnly := plannerStep(t0.Add(3*time.Second), 900, 0, "")
	writeConversationDB(t, path, []fixtureStep{
		{stepTypeUserInput, userStep(t0, "old question")},
		{stepTypePlannerResponse, plannerStep(t0.Add(time.Second), 1000, 0, "old answer")},
		{stepTypeCheckpoint, checkpointStep(t0.Add(2*time.Second), "summary of the old part")},
		{stepTypeUserInput, userStep(t0.Add(3*time.Second), "new question")},
		{stepTypePlannerResponse, plannerStep(t0.Add(4*time.Second), 1200, 0, "let me look")},
		{stepTypePlannerResponse, toolOnly},
		{132, []byte("tool output is skipped")},
		{stepTypePlannerResponse, plannerStep(t0.Add(6*time.Second), 300, 5000, "found it")},
	})
	h, err := readHandoffDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if !h.LastActivity.Equal(t0.Add(6 * time.Second)) {
		t.Errorf("LastActivity = %v", h.LastActivity)
	}
	if h.ContextTokens != 5300 {
		t.Errorf("ContextTokens = %d, want input + cache_read = 5300", h.ContextTokens)
	}
	if h.CompactSummary != "summary of the old part" || !h.CompactedAt.Equal(t0.Add(2*time.Second)) {
		t.Errorf("summary = %q at %v", h.CompactSummary, h.CompactedAt)
	}
	if len(h.Tail) != 2 {
		t.Fatalf("Tail = %+v, want the user turn and its reply after the checkpoint", h.Tail)
	}
	if h.Tail[0].Role != "user" || h.Tail[0].Content != "new question" || !h.Tail[0].Timestamp.Equal(t0.Add(3*time.Second)) {
		t.Errorf("Tail[0] = %+v", h.Tail[0])
	}
	if h.Tail[1].Role != "assistant" || h.Tail[1].Content != "let me look\nfound it" || !h.Tail[1].Timestamp.Equal(t0.Add(6*time.Second)) {
		t.Errorf("Tail[1] = %+v, want the turn's texts joined", h.Tail[1])
	}
}

func TestReadSessionHandoffFindsConversation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	id := "67e8f22c-7891-4d93-ab88-772c6f72fb96"
	dir := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeConversationDB(t, filepath.Join(dir, id+".db"), []fixtureStep{
		{stepTypeUserInput, userStep(t0, "hi")},
		{stepTypePlannerResponse, plannerStep(t0.Add(time.Second), 11829, 0, "你好")},
	})
	a := &Agent{}
	h, err := a.ReadSessionHandoff(id)
	if err != nil {
		t.Fatal(err)
	}
	if h.Path != filepath.Join(dir, id+".db") || h.ContextTokens != 11829 || len(h.Tail) != 2 {
		t.Errorf("handoff = %+v", h)
	}
	for _, bad := range []string{"", "../x", "missing", "00000000-0000-0000-0000-000000000000"} {
		if _, err := a.ReadSessionHandoff(bad); err == nil {
			t.Errorf("ReadSessionHandoff(%q) succeeded", bad)
		}
	}
}

func TestParseStreamLine(t *testing.T) {
	s := &antigravitySession{}
	ev, ok := s.parseStreamLine([]byte(`{"event":"init","conversation_id":"conv-new","init":{"model":"m"}}` + "\n"))
	if !ok || ev.SessionID != "conv-new" || s.CurrentSessionID() != "conv-new" {
		t.Fatalf("init: ev=%+v ok=%v id=%q", ev, ok, s.CurrentSessionID())
	}
	if _, ok := s.parseStreamLine([]byte(`{"event":"init","conversation_id":"conv-new"}`)); ok {
		t.Error("same id again should emit nothing")
	}
	ev, ok = s.parseStreamLine([]byte(`{"event":"step_update","step_update":{"step_type":"agent_response","text_delta":"你好"}}`))
	if !ok || ev.Content != "你好" {
		t.Errorf("text delta: %+v %v", ev, ok)
	}
	if _, ok := s.parseStreamLine([]byte(`{"event":"step_update","step_update":{"step_type":"tool","tool_name":"view_file"}}`)); ok {
		t.Error("tool steps should emit nothing")
	}
	if _, ok := s.parseStreamLine([]byte(`{"event":"result","result":{"status":"SUCCESS"}}`)); ok {
		t.Error("a successful result should emit nothing")
	}
	ev, ok = s.parseStreamLine([]byte(`{"event":"result","result":{"status":"ERROR"}}`))
	if !ok || ev.Error == nil || !strings.Contains(ev.Error.Error(), "ERROR") {
		t.Errorf("failed result: %+v %v", ev, ok)
	}
	if _, ok := s.parseStreamLine([]byte(`{"event":"step_update","step_update":{"step_type":"agent_re`)); ok {
		t.Error("a cut-off JSON line should be dropped, not sent as text")
	}
	ev, ok = s.parseStreamLine([]byte("plain text\n"))
	if !ok || ev.Content != "plain text\n" {
		t.Errorf("plain line: %+v %v", ev, ok)
	}
}
