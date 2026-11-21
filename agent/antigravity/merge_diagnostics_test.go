package antigravity

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestMeteredAntigravityRecoveredIDKeepsPendingChildSources(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := meterSessionForTest(t, "")
	path := filepath.Join(t.TempDir(), "child.jsonl")
	line := `{"id":"child-1","type":"gemini","model":"fixture","tokens":{"input":10,"output":3}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	var info agySubagentInfo
	if err := json.Unmarshal([]byte(`{"subagents":[{"conversation_id":"child","log_uri":`+strconv.Quote(path)+`}]}`), &info); err != nil {
		t.Fatal(err)
	}
	s.trackSubagentLogs("", &info)
	event := s.turnUsageEvent("recovered-parent", agyUsage{InputTokens: 100, OutputTokens: 5})
	if event.Sidechain == nil || event.Sidechain.InputTokens != 10 || event.Sidechain.OutputTokens != 3 {
		t.Fatalf("late parent ID lost its pending child source: %+v", event.Sidechain)
	}
	next := s.turnUsageEvent("recovered-parent", agyUsage{InputTokens: 100, OutputTokens: 5})
	if next.InputTokens != 0 || next.OutputTokens != 0 || next.Sidechain != nil {
		t.Fatalf("recovered parent/child history billed again: %+v", next)
	}
	restarted := meterSessionForTest(t, "recovered-parent")
	reloaded := restarted.turnUsageEvent("recovered-parent", agyUsage{InputTokens: 100, OutputTokens: 5})
	if !reloaded.BillableUsage().Empty() || reloaded.UsageIncomplete {
		t.Fatalf("recovered meter baseline did not survive restart: %+v", reloaded)
	}
	// Carry unresolved sources only across empty -> known identity. A truly
	// different parent must not inherit either those sources or its counters.
	unrelated := s.turnUsageEvent("another-parent", agyUsage{InputTokens: 7, OutputTokens: 2})
	if unrelated.InputTokens != 7 || unrelated.OutputTokens != 2 || unrelated.Sidechain != nil {
		t.Fatalf("unrelated session inherited recovered history: %+v", unrelated)
	}
	if len(s.usageMeter.Sources()) != 0 {
		t.Fatal("child sources leaked into an unrelated parent")
	}
}

func TestMeteredAntigravityMergedHeadlessDiagnostics(t *testing.T) {
	if os.Getenv("CC_AGY_METER_MERGE_HELPER") == "1" {
		_, _ = io.WriteString(os.Stdout, os.Getenv("CC_AGY_METER_OUTPUT"))
		_, _ = io.WriteString(os.Stderr, os.Getenv("CC_AGY_METER_STDERR"))
		if path := os.Getenv("CC_AGY_METER_CHAT"); path != "" {
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				os.Exit(9)
			}
			if err := os.WriteFile(path, []byte(`{"sessionId":"meter-recovered"}`+"\n"), 0o600); err != nil {
				os.Exit(9)
			}
		}
		if os.Getenv("CC_AGY_METER_FAIL") == "1" {
			os.Exit(7)
		}
		os.Exit(0)
	}

	const usage = `,"usage":{"input_tokens":100,"cache_read_tokens":20,"output_tokens":5}`
	for _, tc := range []struct {
		name, output, stderr, wantText, wantError, wantSession string
		fail, writeChat, wantUsage                             bool
	}{
		{name: "empty success retains diagnostic and usage",
			output:    `{"event":"result","result":{"status":"SUCCESS","response":""` + usage + `}}`,
			wantError: "without producing a response", wantUsage: true},
		{name: "empty terminal retains exit code and usage",
			output: `{"event":"result","result":{"status":"SUCCESS","response":""` + usage + `}}`,
			stderr: "late process failure", fail: true, wantError: "exit status 7", wantUsage: true},
		{name: "structured error is not duplicated",
			output: `{"event":"result","result":{"status":"ERROR","response":"","error":"terminal failure"` + usage + `}}`,
			fail:   true, wantError: "terminal failure", wantUsage: true},
		{name: "valid response and late exit retain result usage",
			output: `{"event":"result","result":{"status":"ERROR","response":"answer","error":"historical failure"` + usage + `}}`,
			fail:   true, wantText: "answer", wantUsage: true},
		{name: "missing result ID still recovers after exit",
			output:    `{"event":"result","result":{"status":"SUCCESS","response":"answer"` + usage + `}}`,
			writeChat: true, wantText: "answer", wantSession: "meter-recovered", wantUsage: true},
		{name: "failed result ID recovery does not bill usage twice",
			output:    `{"event":"result","result":{"status":"ERROR","response":"","error":"terminal failure"` + usage + `}}`,
			writeChat: true, fail: true, wantError: "terminal failure", wantSession: "meter-recovered", wantUsage: true},
		{name: "no terminal result still reports failure",
			stderr: "headless denied", wantError: "headless denied"},
		{name: "no result step usage belongs on failure",
			output:    `{"event":"step_update","step_update":{"step_index":1,"step_type":"thinking","state":"DONE","usage":{"input_tokens":100,"cache_read_tokens":20,"output_tokens":5}}}`,
			wantError: "without producing a response", wantUsage: true},
		{name: "blank buffered output is not an answer",
			output:   `{"event":"step_update","step_update":{"step_type":"agent_response","text_delta":"  "}}`,
			wantText: "  ", wantError: "without producing a response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			workDir := t.TempDir()
			env := []string{
				"CC_AGY_METER_MERGE_HELPER=1",
				"CC_AGY_METER_OUTPUT=" + tc.output,
				"CC_AGY_METER_STDERR=" + tc.stderr,
			}
			if tc.fail {
				env = append(env, "CC_AGY_METER_FAIL=1")
			}
			if tc.writeChat {
				chat := filepath.Join(os.Getenv("HOME"), ".gemini", "tmp", antigravityProjectSlug(workDir), "chats", "new.jsonl")
				env = append(env, "CC_AGY_METER_CHAT="+chat)
			}
			s, err := newAntigravitySession(context.Background(), os.Args[0],
				[]string{"-test.run=^TestMeteredAntigravityMergedHeadlessDiagnostics$", "--"},
				workDir, "", "yolo", "", env, 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			if err := s.Send("fixture", "", nil, nil); err != nil {
				t.Fatal(err)
			}
			var text strings.Builder
			var failures []core.Event
			var total core.TokenUsage
			deadline := time.After(10 * time.Second)
			for {
				select {
				case event := <-s.Events():
					switch event.Type {
					case core.EventText:
						text.WriteString(event.Content)
					case core.EventError:
						failures = append(failures, event)
						total.Add(event.BillableUsage())
					case core.EventResult:
						total.Add(event.BillableUsage())
						if text.String() != tc.wantText {
							t.Errorf("text = %q, want %q", text.String(), tc.wantText)
						}
						if tc.wantError == "" {
							if len(failures) != 0 {
								t.Fatalf("unexpected errors: %v", failures)
							}
						} else if len(failures) != 1 || failures[0].Error == nil ||
							!strings.Contains(failures[0].Error.Error(), tc.wantError) {
							t.Fatalf("errors = %v, want exactly one containing %q", failures, tc.wantError)
						}
						if tc.wantUsage {
							if total.InputTokens != 80 || total.CacheReadInputTokens != 20 || total.OutputTokens != 5 {
								t.Fatalf("usage lost or billed twice: %+v", total)
							}
							if tc.wantError != "" && failures[0].InputTokens != 80 {
								t.Fatalf("terminal error lost its usage: %+v", failures[0])
							}
						} else if !total.Empty() {
							t.Fatalf("invented usage: %+v", total)
						}
						if !event.Done || event.SessionID != tc.wantSession || s.CurrentSessionID() != tc.wantSession {
							t.Fatalf("completion/session = %+v / %q, want %q", event, s.CurrentSessionID(), tc.wantSession)
						}
						if len(failures) > 0 && failures[0].SessionID != tc.wantSession {
							t.Fatalf("error attributed to session %q, want %q", failures[0].SessionID, tc.wantSession)
						}
						if tc.wantSession != "" && tc.wantUsage {
							restarted := meterSessionForTest(t, tc.wantSession)
							reloaded := restarted.turnUsageEvent(tc.wantSession, agyUsage{InputTokens: 100, CacheReadTokens: 20, OutputTokens: 5})
							if !reloaded.BillableUsage().Empty() || reloaded.UsageIncomplete {
								t.Fatalf("recovered terminal usage billed after restart: %+v", reloaded)
							}
						}
						return
					}
				case <-deadline:
					t.Fatal("timeout waiting for merged diagnostic result")
				}
			}
		})
	}
}
