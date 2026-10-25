package claudecode

import (
	"encoding/json"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// refusalFallbackLine builds a model_refusal_fallback event the way Claude Code
// writes it to stream-json (field names as in the CLI's SDK message schema).
// The values are synthetic; the shape matches what a 2.1.29x CLI emitted when
// claude-opus-5-5's cyber classifier declined a request.
func refusalFallbackLine(t *testing.T, edit func(map[string]any)) string {
	t.Helper()
	raw := map[string]any{
		"type":                      "system",
		"subtype":                   "model_refusal_fallback",
		"uuid":                      "6f1d2c3b-0000-4000-8000-000000000001",
		"trigger":                   "refusal",
		"direction":                 "retry",
		"scope":                     "session",
		"original_model":            "claude-opus-5-5",
		"fallback_model":            "claude-opus-4-8",
		"request_id":                "req_test_fallback_1",
		"api_refusal_category":      "cyber",
		"api_refusal_explanation":   "This request triggered restrictions on violative cyber content and was blocked under Anthropic's Usage Policy.",
		"retracted_message_uuids":   []any{"6f1d2c3b-0000-4000-8000-000000000002"},
		"refused_user_message_uuid": "6f1d2c3b-0000-4000-8000-000000000003",
		"content":                   "Opus 5.5's safeguards flagged this session. Opus 4.8 is answering instead, or you can edit and retry with Opus 5.5.\n\nDetails: `[cyber]`",
		"session_id":                "fallback-sess",
	}
	if edit != nil {
		edit(raw)
	}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// capturedRefusalFallbackLine is the event Claude Code 2.1.302 printed on
// stream-json when a claude-opus-5-5 request ended with stop_reason "refusal"
// (category cyber) and the CLI re-ran it on claude-opus-4-8. request_id is null
// because the API response carried no request-id header.
const capturedRefusalFallbackLine = "{\"type\":\"system\",\"subtype\":\"model_refusal_fallback\",\"trigger\":\"refusal\",\"direction\":\"retry\",\"scope\":\"session\",\"original_model\":\"claude-opus-5-5\",\"fallback_model\":\"claude-opus-4-8\",\"request_id\":null,\"api_refusal_category\":\"cyber\",\"api_refusal_explanation\":\"This request was declined because it could enable cyber harm.\",\"retracted_message_uuids\":[],\"refused_user_message_uuid\":\"785b9f0e-200d-4fa4-af06-ebfefed29158\",\"content\":\"Opus 5.5's safeguards flagged this session. You may be seeing this for the first time: Opus 5.5 is more capable and has stronger safeguards as a result, which can sometimes flag non-cybersecurity work. We're improving these safeguards to reduce the amount of incorrectly flagged messages. Opus 4.8 is answering instead, or you can edit and retry with Opus 5.5. You can learn more: https://support.claude.com/en/articles/8106465\\n\\nDetails: `[cyber]`\",\"session_id\":\"b5eacd75-be3f-458e-88d8-3bbc17e6dbb5\",\"uuid\":\"3961fb82-b002-49af-9159-65a4e012e713\"}"

// drainModelSwitches returns the EventModelSwitch events queued on cs.events.
// handleSystem also queues an EventText carrying the session ID, which is
// skipped here.
func drainModelSwitches(cs *claudeSession) []*core.ModelSwitch {
	var out []*core.ModelSwitch
	for {
		select {
		case evt := <-cs.events:
			if evt.Type == core.EventModelSwitch {
				out = append(out, evt.ModelSwitch)
			}
		default:
			return out
		}
	}
}

func TestRefusalFallbackSwitchesSessionModelAndNotifies(t *testing.T) {
	cs := newWindowTestSession(t, "claude-opus-5-5")
	cs.reportedCtxWindow.Store(1000000) // learned from claude-opus-5-5's modelUsage

	cs.handleReadLoopLine(refusalFallbackLine(t, nil))

	switches := drainModelSwitches(cs)
	if len(switches) != 1 {
		t.Fatalf("model switch events = %d, want 1", len(switches))
	}
	want := core.ModelSwitch{From: "claude-opus-5-5", To: "claude-opus-4-8", Reason: "refusal", Category: "cyber", SessionWide: true}
	if *switches[0] != want {
		t.Fatalf("model switch = %+v, want %+v", *switches[0], want)
	}
	if got := cs.GetModel(); got != "claude-opus-4-8" {
		t.Fatalf("GetModel() after session fallback = %q, want claude-opus-4-8 (the footer must name the model that answers)", got)
	}
	if got := cs.reportedCtxWindow.Load(); got != 0 {
		t.Fatalf("reportedCtxWindow = %d, want 0 so the fallback model's own window is learned", got)
	}
}

func TestRefusalFallbackCapturedCLIEvent(t *testing.T) {
	cs := newWindowTestSession(t, "claude-opus-5-5")

	cs.handleReadLoopLine(capturedRefusalFallbackLine)

	switches := drainModelSwitches(cs)
	want := core.ModelSwitch{From: "claude-opus-5-5", To: "claude-opus-4-8", Reason: "refusal", Category: "cyber", SessionWide: true}
	if len(switches) != 1 || *switches[0] != want {
		t.Fatalf("model switches = %+v, want one %+v", switches, want)
	}
	if got := cs.GetModel(); got != "claude-opus-4-8" {
		t.Fatalf("GetModel() = %q, want claude-opus-4-8", got)
	}
}

// The next result event reports a modelUsage entry per model the turn used.
// After the fallback, the context window must come from the fallback model's
// entry, not the refused model's.
func TestRefusalFallbackContextWindowFollowsFallbackModel(t *testing.T) {
	cs := newWindowTestSession(t, "claude-opus-5-5")
	cs.reportedCtxWindow.Store(1000000)

	cs.handleReadLoopLine(refusalFallbackLine(t, nil))
	cs.handleResult(windowResultEvent(map[string]any{
		"claude-opus-5-5": map[string]any{"contextWindow": float64(1000000)},
		"claude-opus-4-8": map[string]any{"contextWindow": float64(200000)},
	}))

	if got := cs.reportedCtxWindow.Load(); got != 200000 {
		t.Fatalf("reportedCtxWindow = %d, want 200000 from the claude-opus-4-8 entry", got)
	}
}

// The CLI can announce the same fallback twice: a provisional notice mid-turn
// and the final one at the end of the turn, each with its own uuid. The user
// should be told once.
func TestRefusalFallbackProvisionalAndFinalNotifyOnce(t *testing.T) {
	cs := newWindowTestSession(t, "claude-opus-5-5")

	cs.handleReadLoopLine(refusalFallbackLine(t, func(m map[string]any) {
		m["uuid"] = "6f1d2c3b-0000-4000-8000-0000000000a1"
		m["provisional"] = true
		delete(m, "retracted_message_uuids")
	}))
	cs.handleReadLoopLine(refusalFallbackLine(t, func(m map[string]any) {
		m["uuid"] = "6f1d2c3b-0000-4000-8000-0000000000a2"
	}))

	if got := len(drainModelSwitches(cs)); got != 1 {
		t.Fatalf("model switch events = %d, want 1 for one fallback announced twice", got)
	}
}

// A cascade that moves on to another fallback target is a new switch.
func TestRefusalFallbackNewTargetNotifiesAgain(t *testing.T) {
	cs := newWindowTestSession(t, "claude-opus-5-5")

	cs.handleReadLoopLine(refusalFallbackLine(t, nil))
	cs.handleReadLoopLine(refusalFallbackLine(t, func(m map[string]any) {
		m["fallback_model"] = "claude-sonnet-5"
	}))

	switches := drainModelSwitches(cs)
	if len(switches) != 2 {
		t.Fatalf("model switch events = %d, want 2", len(switches))
	}
	if got := cs.GetModel(); got != "claude-sonnet-5" {
		t.Fatalf("GetModel() = %q, want the latest fallback target", got)
	}
}

// Older CLIs omit scope; the schema says to treat that as "session".
func TestRefusalFallbackMissingScopeIsSessionWide(t *testing.T) {
	cs := newWindowTestSession(t, "claude-opus-5-5")

	cs.handleReadLoopLine(refusalFallbackLine(t, func(m map[string]any) {
		delete(m, "scope")
		delete(m, "api_refusal_category")
		m["request_id"] = nil
	}))

	switches := drainModelSwitches(cs)
	if len(switches) != 1 || !switches[0].SessionWide || switches[0].Category != "" {
		t.Fatalf("model switch = %+v, want one session-wide switch without a category", switches)
	}
	if got := cs.GetModel(); got != "claude-opus-4-8" {
		t.Fatalf("GetModel() = %q, want claude-opus-4-8", got)
	}
}

// scope "local": a subagent or side question fell back; the session model is
// unchanged, but the user is still told that part of the turn came from
// another model.
func TestRefusalFallbackLocalScopeKeepsSessionModel(t *testing.T) {
	cs := newWindowTestSession(t, "claude-opus-5-5")
	cs.reportedCtxWindow.Store(1000000)

	cs.handleReadLoopLine(refusalFallbackLine(t, func(m map[string]any) {
		m["scope"] = "local"
	}))

	switches := drainModelSwitches(cs)
	if len(switches) != 1 || switches[0].SessionWide {
		t.Fatalf("model switch = %+v, want one switch with SessionWide=false", switches)
	}
	if got := cs.GetModel(); got != "claude-opus-5-5" {
		t.Fatalf("GetModel() = %q, want the session model unchanged", got)
	}
	if got := cs.reportedCtxWindow.Load(); got != 1000000 {
		t.Fatalf("reportedCtxWindow = %d, want it kept for a local fallback", got)
	}
}

// "revert" and "sticky" are retained in the CLI's enum but no longer emitted;
// they do not describe a move to the fallback model.
func TestRefusalFallbackIgnoresNonRetryDirections(t *testing.T) {
	for _, direction := range []string{"revert", "sticky"} {
		t.Run(direction, func(t *testing.T) {
			cs := newWindowTestSession(t, "claude-opus-5-5")
			cs.handleReadLoopLine(refusalFallbackLine(t, func(m map[string]any) {
				m["direction"] = direction
			}))
			if got := len(drainModelSwitches(cs)); got != 0 {
				t.Fatalf("model switch events = %d, want 0", got)
			}
			if got := cs.GetModel(); got != "claude-opus-5-5" {
				t.Fatalf("GetModel() = %q, want unchanged", got)
			}
		})
	}
}
