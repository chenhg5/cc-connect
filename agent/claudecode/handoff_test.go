package claudecode

import (
	"strings"
	"testing"
)

func TestParseHandoff(t *testing.T) {
	lines := []string{
		`{"type":"user","uuid":"u0","timestamp":"2026-10-14T01:00:00Z","message":{"content":"很早的话"}}`,
		`{"type":"assistant","uuid":"a0","timestamp":"2026-10-14T01:00:05Z","message":{"content":[{"type":"text","text":"很早的回答"}],"usage":{"input_tokens":2,"cache_read_input_tokens":1000,"cache_creation_input_tokens":10}}}`,
		`{"type":"user","uuid":"u1","timestamp":"2026-10-14T01:30:00Z","message":{"content":"压缩时保留的话"}}`,
		`{"type":"system","subtype":"compact_boundary","timestamp":"2026-10-14T02:00:00Z","compactMetadata":{"postTokens":3600,"preservedMessages":{"uuids":["u1"]}}}`,
		`{"type":"user","isCompactSummary":true,"timestamp":"2026-10-14T02:00:01Z","message":{"content":"This session is being continued from a previous conversation that ran out of context. The summary below covers the earlier portion of the conversation.\n\nSummary:\n1. 在修 gate\n\nIf you need specific details from before compaction, read the full transcript at: /x.jsonl\nContinue the conversation from where it left off without asking the user any further questions."}}`,
		`{"type":"user","isMeta":true,"message":{"content":"meta"}}`,
		`{"type":"user","uuid":"u2","timestamp":"2026-10-14T02:01:00Z","message":{"content":"<@1468663018463301632> 你看看"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-10-14T02:01:03Z","message":{"content":[{"type":"text","text":"Now I'll check."}],"usage":{"input_tokens":2,"cache_read_input_tokens":50000,"cache_creation_input_tokens":200}}}`,
		`{"type":"assistant","uuid":"a2","timestamp":"2026-10-14T02:01:05Z","message":{"content":[{"type":"tool_use","name":"Bash","input":{}}],"usage":{"input_tokens":2,"cache_read_input_tokens":50000,"cache_creation_input_tokens":300}}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}`,
		`{"type":"attachment","uuid":"q1","timestamp":"2026-10-14T02:01:30Z","attachment":{"type":"queued_command","prompt":"别自己干，派出去","commandMode":"prompt"}}`,
		`{"type":"attachment","uuid":"q2","timestamp":"2026-10-14T02:01:40Z","attachment":{"type":"queued_command","prompt":[{"type":"text","text":"看这张图"},{"type":"image","source":{}}],"commandMode":"prompt"}}`,
		`{"type":"assistant","uuid":"a3","timestamp":"2026-10-14T02:02:00Z","message":{"content":[{"type":"text","text":"查到了，原因是锁"}],"usage":{"input_tokens":3,"cache_read_input_tokens":60000,"cache_creation_input_tokens":500}}}`,
		`{"type":"user","message":{"content":"<task-notification>\n<task-id>x</task-id>"}}`,
		`{"type":"assistant","uuid":"a4","timestamp":"2026-10-14T02:03:00Z","message":{"content":[{"type":"text","text":"子任务也做完了"}],"usage":{"input_tokens":3,"cache_read_input_tokens":60100,"cache_creation_input_tokens":420}}}`,
		`{"type":"assistant","isSidechain":true,"timestamp":"2026-10-14T03:00:00Z","message":{"content":[{"type":"text","text":"子 agent"}],"usage":{"input_tokens":1,"cache_read_input_tokens":999999}}}`,
		`{"type":"assistant","timestamp":"2026-10-14T02:04:00Z","message":{"model":"<synthetic>","content":[{"type":"text","text":"API Error"}],"usage":{"input_tokens":0,"cache_read_input_tokens":0}}}`,
	}
	h, err := parseHandoff(strings.NewReader(strings.Join(lines, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	if h.CompactSummary != "Summary:\n1. 在修 gate" {
		t.Fatalf("summary = %q", h.CompactSummary)
	}
	if h.CompactedAt.Format("15:04") != "02:00" {
		t.Fatalf("compacted at %v", h.CompactedAt)
	}
	if h.ContextTokens != 60523 || h.LastActivity.Format("15:04:05") != "02:03:00" {
		t.Fatalf("usage = %d at %v", h.ContextTokens, h.LastActivity)
	}
	var got []string
	for _, e := range h.Tail {
		got = append(got, e.Role+":"+e.Content)
	}
	want := []string{
		"user:压缩时保留的话",
		"user:<@1468663018463301632> 你看看",
		"assistant:Now I'll check.", // an inserted message splits the turn; order stays true
		"user:别自己干，派出去",
		"user:看这张图",
		"assistant:查到了，原因是锁",
		"assistant:子任务也做完了",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("tail = %q", got)
	}
}

func TestParseHandoffCompactionWithoutReply(t *testing.T) {
	lines := []string{
		`{"type":"assistant","uuid":"a0","timestamp":"2026-10-14T01:00:05Z","message":{"content":[{"type":"text","text":"x"}],"usage":{"input_tokens":2,"cache_read_input_tokens":195000}}}`,
		`{"type":"system","subtype":"compact_boundary","timestamp":"2026-10-14T02:00:00Z","compactMetadata":{"postTokens":3600}}`,
	}
	h, _ := parseHandoff(strings.NewReader(strings.Join(lines, "\n")))
	if h.ContextTokens != 3600 {
		t.Fatalf("context after compaction = %d", h.ContextTokens)
	}
}
