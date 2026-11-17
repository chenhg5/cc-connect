package claudecode

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const limitedPrompt = "请看这份\n\n(Files saved locally, please read them: /w/.cc-connect/attachments/m1/a.pdf)"

func rejectedRateLimit(resetsAt float64) map[string]any {
	return map[string]any{"type": "rate_limit_event", "rate_limit_info": map[string]any{
		"status": "rejected", "resetsAt": resetsAt, "rateLimitType": "five_hour"}}
}

// A result the CLI refuses for a usage limit carries the prompt it refused and
// when the limit resets; ordinary replies and other API errors do not.
func TestHandleResult_ReportsUsageLimit(t *testing.T) {
	cases := []struct {
		name string
		raw  map[string]any
		want bool
	}{
		{"usage_limit_reached", map[string]any{"subtype": "success", "is_error": true, "api_error": "usage_limit_reached",
			"api_error_status": float64(429), "result": "You've hit your weekly limit · resets 11am (UTC)"}, true},
		{"status 429 only", map[string]any{"subtype": "success", "is_error": true, "api_error_status": float64(429),
			"result": "You've hit your weekly limit · resets Jul 18, 12pm (UTC)"}, true},
		{"api_error alone, text the pattern does not know", map[string]any{"is_error": true, "api_error": "usage_limit_reached",
			"result": "Usage limit reached for this plan."}, true},
		{"status 429 alone, text the pattern does not know", map[string]any{"is_error": true, "api_error_status": float64(429),
			"result": "API Error: 429 rate_limit_error"}, true},
		{"older CLI, refusal text only", map[string]any{"is_error": true,
			"result": "You've hit your session limit · resets 7:30am (Asia/Shanghai)"}, true},
		{"ordinary reply quoting the text", map[string]any{"is_error": false,
			"result": "You said \"you've hit your session limit\"? I can't see that here."}, false},
		{"another API error", map[string]any{"is_error": true, "api_error_status": float64(500),
			"result": "API Error: 500 Internal server error"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := newWindowTestSession(t, "")
			cs.lastPrompt.Store(limitedPrompt)
			cs.handleRateLimitEvent(rejectedRateLimit(1792494000))
			raw := map[string]any{"type": "result"}
			for k, v := range tc.raw {
				raw[k] = v
			}
			cs.handleResult(raw)
			ev := <-cs.events
			if ev.Type != core.EventResult || ev.Content != tc.raw["result"] {
				t.Fatalf("event = %+v, want the result with its text", ev)
			}
			if !tc.want {
				if ev.UsageLimit != nil {
					t.Fatalf("UsageLimit = %+v, want nil", ev.UsageLimit)
				}
				return
			}
			if ev.UsageLimit == nil {
				t.Fatal("UsageLimit = nil, want the refusal reported")
			}
			if ev.UsageLimit.Prompt != limitedPrompt || !ev.UsageLimit.ResetsAt.Equal(time.Unix(1792494000, 0)) {
				t.Fatalf("UsageLimit = %+v", ev.UsageLimit)
			}
		})
	}
}

// Only a rejected rate limit sets the reset time; warnings do not.
func TestHandleRateLimitEvent_OnlyRejectedSetsReset(t *testing.T) {
	cs := newWindowTestSession(t, "")
	cs.handleRateLimitEvent(map[string]any{"type": "rate_limit_event", "rate_limit_info": map[string]any{
		"status": "allowed_warning", "resetsAt": float64(1792400000)}})
	if got := cs.limitResetsAt.Load(); got != 0 {
		t.Fatalf("reset after a warning = %d, want 0", got)
	}
	cs.handleRateLimitEvent(rejectedRateLimit(1792494000))
	if got := cs.limitResetsAt.Load(); got != 1792494000 {
		t.Fatalf("reset after a rejection = %d, want 1792494000", got)
	}
}

type nopWriteCloser struct{ *bytes.Buffer }

func (nopWriteCloser) Close() error { return nil }

// Send remembers the prompt as the CLI got it, attachment paths included.
func TestSend_RemembersPromptWithAttachmentPaths(t *testing.T) {
	cs := newWindowTestSession(t, "")
	cs.workDir = t.TempDir()
	cs.stdin = nopWriteCloser{&bytes.Buffer{}}

	if err := cs.Send("plain question", "m0", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := cs.lastPrompt.Load(); got != "plain question" {
		t.Fatalf("lastPrompt = %q", got)
	}
	if err := cs.Send("", "m1", nil, []core.FileAttachment{{FileName: "a.pdf", MimeType: "application/pdf", Data: []byte("%PDF")}}); err != nil {
		t.Fatalf("Send with file: %v", err)
	}
	got, _ := cs.lastPrompt.Load().(string)
	if !strings.HasPrefix(got, "Please analyze the attached file(s).") || !strings.Contains(got, "(Files saved locally, please read them: ") || !strings.Contains(got, "a.pdf") {
		t.Fatalf("lastPrompt = %q, want the file note with its path", got)
	}
}
