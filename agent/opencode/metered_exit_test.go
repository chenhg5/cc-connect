package opencode

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestMeteredOpencodeExitPreservesTerminalOutcomeAndUsage(t *testing.T) {
	if scenario := os.Getenv("CC_OPENCODE_METER_EXIT_FIXTURE"); scenario != "" {
		const step = `{"type":"step_finish","part":{"id":"step","reason":"length","tokens":{"input":21,"output":5,"reasoning":2,"cache":{"read":3,"write":1}}}}` + "\n"
		_, _ = os.Stdout.WriteString(step + step) // provider can replay the same step
		if scenario == "success" {
			_, _ = os.Stdout.WriteString(`{"type":"text","part":{"text":"answer"}}` + "\n")
			_, _ = os.Stderr.WriteString("harmless warning\n")
		}
		if scenario == "explicit" {
			_, _ = os.Stdout.WriteString(`{"type":"error","error":"provider failed"}` + "\n")
		}
		if scenario == "nonzero" || scenario == "explicit" {
			os.Exit(9)
		}
		os.Exit(0)
	}

	for _, scenario := range []string{"success", "empty", "nonzero", "explicit"} {
		t.Run(scenario, func(t *testing.T) {
			wantKind := core.EventError
			diagnostic := "without a result"
			switch scenario {
			case "success":
				wantKind, diagnostic = core.EventResult, ""
			case "nonzero":
				diagnostic = "exit status 9"
			case "explicit":
				diagnostic = "provider failed"
			}
			want := core.TokenUsage{InputTokens: 21, OutputTokens: 7, ReasoningOutputTokens: 2, CacheReadInputTokens: 3, CacheCreationInputTokens: 1}
			s, err := newOpencodeSession(context.Background(), os.Args[0], []string{"-test.run=^TestMeteredOpencodeExitPreservesTerminalOutcomeAndUsage$", "--"}, t.TempDir(), "", "", "", "", []string{"CC_OPENCODE_METER_EXIT_FIXTURE=" + scenario})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			turns := 1
			if scenario == "success" {
				turns = 2
			}
			for turn := 0; turn < turns; turn++ {
				if err := s.Send("fixture", "", nil, nil); err != nil {
					t.Fatal(err)
				}
				s.wg.Wait()
				if scenario == "success" {
					if len(s.Events()) != 2 {
						t.Fatalf("got %d events, want answer and result", len(s.Events()))
					}
					if answer := <-s.Events(); answer.Type != core.EventText || answer.Content != "answer" {
						t.Fatalf("answer event: %+v", answer)
					}
				}
				if len(s.Events()) != 1 {
					t.Fatalf("got %d terminal events, want one", len(s.Events()))
				}
				event := <-s.Events()
				if event.Type != wantKind || event.BillableUsage() != want {
					t.Fatalf("turn %d outcome lost or usage billed twice: %+v", turn, event)
				}
				if diagnostic != "" && (event.Error == nil || !strings.Contains(event.Error.Error(), diagnostic)) {
					t.Fatalf("wanted %q, got %v", diagnostic, event.Error)
				}
			}
		})
	}
}
