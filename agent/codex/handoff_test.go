package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Lines trimmed from real rollouts (codex-cli 0.162: a cc-connect thread on
// the relay and a Codex Desktop thread that compacted), texts shortened.
var handoffFixture = []string{
	`{"timestamp":"2026-10-13T18:00:54.535Z","ordinal":0,"type":"session_meta","payload":{"session_id":"01a1366c-ac29-7030-b3cb-b2dc0f6d9188","id":"01a1366c-ac29-7030-b3cb-b2dc0f6d9188","cwd":"/Users/x/im-bridge/jingyu","originator":"cc-connect-codex-agent","cli_version":"0.162.0-alpha.17.2"}}`,
	`{"timestamp":"2026-10-13T18:00:54.535Z","ordinal":1,"type":"event_msg","payload":{"type":"task_started","turn_id":"t1","model_context_window":258400}}`,
	`{"timestamp":"2026-10-13T18:00:54.560Z","ordinal":2,"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions instructions>\nsandbox"}],"internal_chat_message_metadata_passthrough":{"turn_id":"t1","content_item_kinds":["permissions.instructions"]}}}`,
	`{"timestamp":"2026-10-13T18:00:54.560Z","ordinal":3,"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions for /Users/x/im-bridge/jingyu\n\n<INSTRUCTIONS>\n# 角色"},{"type":"input_text","text":"<environment_context>\n  <cwd>/Users/x</cwd>"}],"internal_chat_message_metadata_passthrough":{"turn_id":"t1","content_item_kinds":["agents_md.instructions","environments.environment_context"]}}}`,
	`{"timestamp":"2026-10-13T18:00:54.562Z","ordinal":6,"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<@1468663018463301632> 你们看看"}],"internal_chat_message_metadata_passthrough":{"turn_id":"t1","content_item_kinds":["user.text"]}}}`,
	`{"timestamp":"2026-10-13T18:00:54.563Z","ordinal":7,"type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","content":[{"type":"text","text":"<@1468663018463301632> 你们看看"}]}}}`,
	`{"timestamp":"2026-10-13T18:04:00.708Z","ordinal":9,"type":"response_item","payload":{"type":"reasoning","summary":[],"encrypted_content":"x"}}`,
	`{"timestamp":"2026-10-13T18:04:00.900Z","ordinal":10,"type":"response_item","payload":{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"我先查一下"}]}}`,
	`{"timestamp":"2026-10-13T18:04:01.056Z","ordinal":11,"type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{}","call_id":"c1"}}`,
	`{"timestamp":"2026-10-13T18:04:01.061Z","ordinal":12,"type":"token_usage_record","payload":{"usage":{"input_tokens":9937}}}`,
	`{"timestamp":"2026-10-13T18:04:01.127Z","ordinal":17,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":9937},"last_token_usage":{"input_tokens":9937,"cached_input_tokens":0},"model_context_window":258400}}}`,
	`{"timestamp":"2026-10-13T18:04:01.129Z","ordinal":18,"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"【橘】打就打"}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["user.text"]}}}`,
	`{"timestamp":"2026-10-13T18:10:00.000Z","ordinal":30,"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"看了一半"}]}}`,
	`{"timestamp":"2026-10-13T18:15:10.946Z","ordinal":53,"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"看完了，蓝的是我"}]}}`,
	`{"timestamp":"2026-10-13T18:15:10.950Z","ordinal":54,"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":30000}}}}`,
	`{"timestamp":"2026-10-13T18:15:10.954Z","ordinal":56,"type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","last_agent_message":"看完了，蓝的是我"}}`,
	`{"timestamp":"2026-10-13T18:20:00.000Z","ordinal":60,"type":"event_msg","payload":{"type":"task_started","turn_id":"t2"}}`,
	`{"timestamp":"2026-10-13T18:20:00.010Z","ordinal":61,"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"（可能相关的 wiki 页，需要时去读）\n- ~/wiki/x.md\n\n看图"},{"type":"input_text","text":"<image name=[Image #1] path=\"/x.png\">"},{"type":"input_image","image_url":"data:image/png;base64,AAAA"},{"type":"input_text","text":"</image>"}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["user.text","user.text","user.image","user.text"]}}}`,
	`{"timestamp":"2026-10-13T18:20:05.000Z","ordinal":62,"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"NO_REPLY"}]}}`,
	`{"timestamp":"2026-10-13T18:20:05.100Z","ordinal":63,"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":31000}}}}`,
}

func TestParseHandoff(t *testing.T) {
	h, err := parseHandoff(strings.NewReader(strings.Join(handoffFixture, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	if h.CompactSummary != "" || !h.CompactedAt.IsZero() {
		t.Fatalf("never compacted, got %q at %v", h.CompactSummary, h.CompactedAt)
	}
	if h.ContextTokens != 31000 || h.LastActivity.Format("15:04:05.000") != "18:20:05.100" {
		t.Fatalf("usage = %d at %v", h.ContextTokens, h.LastActivity)
	}
	var got []string
	for _, e := range h.Tail {
		got = append(got, e.Role+":"+e.Content)
	}
	want := []string{
		"user:<@1468663018463301632> 你们看看",
		// the commentary is dropped; the steered message splits the turn
		"user:【橘】打就打",
		"assistant:看完了，蓝的是我",
		"user:（可能相关的 wiki 页，需要时去读）\n- ~/wiki/x.md\n\n看图",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("tail = %q", got)
	}
}

func TestParseHandoffCompaction(t *testing.T) {
	lines := append(append([]string{}, handoffFixture[:16]...),
		`{"timestamp":"2026-10-09T00:58:29.375Z","type":"token_usage_record","payload":{"usage":{"input_tokens":207602}}}`,
		`{"timestamp":"2026-10-09T00:58:29.376Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":207602}}}}`,
		`{"timestamp":"2026-10-09T00:58:29.379Z","type":"compacted","payload":{"message":"Another language model started to solve this problem and produced a summary of its thinking process. You also have access to the state of the tools that were used by that language model. Use this to build on the work that has already been done and avoid duplicating work. Here is the summary produced by the other language model, use the information in this summary to assist with your own analysis:\n## Active request\n\n在修 gate","replacement_history":[{"type":"message","role":"user","content":[{"type":"input_text","text":"你们看看"}]}]}}`,
		`{"timestamp":"2026-10-09T00:58:29.379Z","type":"turn_context","payload":{"turn_id":"t3"}}`,
		`{"timestamp":"2026-10-09T00:58:29.381Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":0}}}}`,
	)
	h, _ := parseHandoff(strings.NewReader(strings.Join(lines, "\n")))
	if h.CompactSummary != "## Active request\n\n在修 gate" {
		t.Fatalf("summary = %q", h.CompactSummary)
	}
	if h.CompactedAt.Format("15:04:05") != "00:58:29" || len(h.Tail) != 0 {
		t.Fatalf("compacted at %v, tail %v", h.CompactedAt, h.Tail)
	}
	if h.ContextTokens != 0 || h.LastActivity.Format("15:04:05.000") != "00:58:29.376" {
		t.Fatalf("after compaction: %d tokens, last reply %v", h.ContextTokens, h.LastActivity)
	}

	lines = append(lines,
		`{"timestamp":"2026-10-09T00:58:29.386Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"继续"}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["user.text"]}}}`,
		`{"timestamp":"2026-10-09T00:58:33.901Z","type":"response_item","payload":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"好"}]}}`,
		`{"timestamp":"2026-10-09T00:58:34.000Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":12000}}}}`,
	)
	h, _ = parseHandoff(strings.NewReader(strings.Join(lines, "\n")))
	if h.ContextTokens != 12000 || len(h.Tail) != 2 || h.Tail[1].Content != "好" {
		t.Fatalf("after next reply: %d tokens, tail %v", h.ContextTokens, h.Tail)
	}
}

func TestReadSessionHandoffFindsRollout(t *testing.T) {
	home := t.TempDir()
	id := "01a1366c-ac29-7030-b3cb-b2dc0f6d9188"
	dir := filepath.Join(home, "sessions", "2026", "10", "14")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-2026-10-14T03-00-54-"+id+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(handoffFixture, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &Agent{codexHome: home}
	h, err := a.ReadSessionHandoff(id)
	if err != nil {
		t.Fatal(err)
	}
	if h.Path != path || h.ContextTokens != 31000 {
		t.Fatalf("path %q tokens %d", h.Path, h.ContextTokens)
	}
	if _, err := a.ReadSessionHandoff("missing"); err == nil {
		t.Fatal("missing thread should fail")
	}
}

func TestParseHandoffCountsImageBytes(t *testing.T) {
	img := strings.Repeat("iVBO", 250) // 1000 base64 chars
	lines := append([]string{}, handoffFixture...) // the fixture's user image: 4 chars
	lines = append(lines,
		`{"timestamp":"2026-10-13T18:21:00.000Z","ordinal":70,"type":"response_item","payload":{"type":"function_call_output","call_id":"c2","output":[{"type":"input_image","image_url":"data:image/png;base64,`+img+`"}]}}`,
		`{"timestamp":"2026-10-13T18:21:01.000Z","ordinal":71,"type":"response_item","payload":{"type":"function_call_output","call_id":"c3","output":"no image here; base64,abcd is plain text"}}`,
	)
	h, err := parseHandoff(strings.NewReader(strings.Join(lines, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	if h.ImageBytes != 1004 {
		t.Fatalf("image bytes = %d, want 1004", h.ImageBytes)
	}
	// A compaction replaces the history with text: its images stop counting.
	lines = append(lines, `{"timestamp":"2026-10-13T18:30:00.000Z","ordinal":80,"type":"compacted","payload":{"message":"摘要","replacement_history":[]}}`)
	h, err = parseHandoff(strings.NewReader(strings.Join(lines, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	if h.ImageBytes != 0 {
		t.Fatalf("after compaction image bytes = %d, want 0", h.ImageBytes)
	}
}
