package slack

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

func rawJSON(t *testing.T, s string) *json.RawMessage {
	t.Helper()
	r := json.RawMessage(s)
	return &r
}

// A native Slack table block: header cells as rich_text (bold), body cells as raw_text.
const tableBlock = `{"type":"table","rows":[
 [{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"text","text":"Item","style":{"bold":true}}]}]},
  {"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"text","text":"Qty","style":{"bold":true}}]}]}],
 [{"type":"raw_text","text":"apple"},{"type":"raw_text","text":"12"}],
 [{"type":"raw_text","text":"a|b"},{"type":"raw_text","text":"3"}]
]}`

// Shape observed from Slack when a message containing a table is shared into a
// thread: the table lives in attachments[].blocks, the message text has none of it.
const sharedTableAttachment = `{"is_share":true,"author_name":"Alice","fallback":"[shared message]",
 "blocks":[` + tableBlock + `]}`

func TestExtractRichContent_SharedMessageTableInAttachmentBlocks(t *testing.T) {
	ev := `{"text":"<@UBOT> see these","blocks":[{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"text","text":"see these"}]}]}],
	 "attachments":[` + sharedTableAttachment + `]}`
	got := extractRichContent(rawJSON(t, ev), "1.1")
	for _, want := range []string{"[Attachment 1 from Alice]", "| Item | Qty |", "| --- | --- |", "| apple | 12 |", `| a\|b | 3 |`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "[shared message]") {
		t.Fatalf("fallback should not be used when blocks render:\n%s", got)
	}
}

func TestExtractRichContent_SharedMessageInMessageBlocks(t *testing.T) {
	ev := `{"text":"x","attachments":[{"is_share":true,"text":"plain fallback",
	 "message_blocks":[{"message":{"blocks":[
	   {"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"text","text":"original body"}]}]},` + tableBlock + `]}}]}]}`
	got := extractRichContent(rawJSON(t, ev), "1.2")
	if !strings.Contains(got, "original body") || !strings.Contains(got, "| apple | 12 |") {
		t.Fatalf("message_blocks not rendered:\n%s", got)
	}
	if strings.Contains(got, "plain fallback") {
		t.Fatalf("should prefer message_blocks over text:\n%s", got)
	}
}

func TestExtractRichContent_DirectTableBlockSkipsOwnRichText(t *testing.T) {
	ev := `{"text":"look","blocks":[{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"text","text":"look"}]}]},` + tableBlock + `]}`
	got := extractRichContent(rawJSON(t, ev), "1.3")
	if strings.Contains(got, "look") {
		t.Fatalf("the message's own rich_text duplicates text and should be skipped:\n%s", got)
	}
	if !strings.Contains(got, "| apple | 12 |") {
		t.Fatalf("table not rendered:\n%s", got)
	}
}

func TestExtractRichContent_PlainTextOnlyReturnsEmpty(t *testing.T) {
	ev := `{"text":"hi","blocks":[{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"text","text":"hi"}]}]}]}`
	if got := extractRichContent(rawJSON(t, ev), "1.4"); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestExtractRichContent_AttachmentTextAndFields(t *testing.T) {
	ev := `{"text":"x","attachments":[{"text":"forwarded body","fields":[{"title":"k","value":"v"}]}]}`
	got := extractRichContent(rawJSON(t, ev), "1.5")
	if !strings.Contains(got, "forwarded body") || !strings.Contains(got, "k: v") {
		t.Fatalf("unexpected:\n%s", got)
	}
}

func TestExtractRichContent_MalformedOrEmptyInputReturnsEmpty(t *testing.T) {
	for name, ev := range map[string]string{
		"nil-equivalent empty":     ``,
		"malformed json":           `{"blocks":[`,
		"no blocks or attachments": `{"text":"hi"}`,
	} {
		var r *json.RawMessage
		if ev != "" {
			r = rawJSON(t, ev)
		}
		if got := extractRichContent(r, "1.6"); got != "" {
			t.Fatalf("%s: expected empty, got %q", name, got)
		}
	}
}

// An attachment with nothing renderable (e.g. a bare link unfurl stub) must not
// add an empty "[Attachment N]" label to the agent's input.
func TestExtractRichContent_EmptyAttachmentAddsNoLabel(t *testing.T) {
	ev := `{"text":"x","attachments":[{"id":1,"color":"ccc"}]}`
	if got := extractRichContent(rawJSON(t, ev), "1.7"); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

// Plain-text mentions must reach the agent byte-for-byte unchanged.
func TestHandleEvent_PlainMentionContentUnchanged(t *testing.T) {
	p, got := newDedupTestPlatform(t)
	ts := freshTS(3)
	raw := `{"type":"app_mention","user":"U1","channel":"C1","ts":"` + ts + `","text":"<@UBOT> hello",
	 "blocks":[{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"text","text":"hello"}]}]}]}`
	p.handleEvent(callbackEvent(&slackevents.AppMentionEvent{
		User: "U1", Channel: "C1", TimeStamp: ts, Text: "<@UBOT> hello",
	}, "app_mention", raw))
	if len(*got) != 1 || (*got)[0].Content != "hello" {
		t.Fatalf("plain mention content changed: %+v", *got)
	}
}

func TestAppendRichContent(t *testing.T) {
	if appendRichContent("a", "") != "a" || appendRichContent("", "b") != "b" || appendRichContent("a", "b") != "a\n\nb" {
		t.Fatal("appendRichContent mismatch")
	}
}

// callbackEvent wraps an inner event together with its raw JSON, the way
// socketmode delivers it, so handleEvent can read blocks/attachments.
func callbackEvent(inner any, innerType, raw string) socketmode.Event {
	r := json.RawMessage(raw)
	return socketmode.Event{
		Type: socketmode.EventTypeEventsAPI,
		Data: slackevents.EventsAPIEvent{
			Type: slackevents.CallbackEvent,
			Data: &slackevents.EventsAPICallbackEvent{Type: "event_callback", InnerEvent: &r},
			InnerEvent: slackevents.EventsAPIInnerEvent{
				Type: innerType,
				Data: inner,
			},
		},
	}
}

// Regression: an @-mention carrying a shared message with a native table was
// dispatched with only "see these" — the table in attachments was dropped, so
// the agent replied that no data had been provided.
func TestHandleEvent_MentionWithSharedTableForwardsTable(t *testing.T) {
	p, got := newDedupTestPlatform(t)
	ts := freshTS(1)
	raw := `{"type":"app_mention","user":"U1","channel":"C1","ts":"` + ts + `","text":"<@UBOT> see these",
	 "attachments":[` + sharedTableAttachment + `]}`
	p.handleEvent(callbackEvent(&slackevents.AppMentionEvent{
		User: "U1", Channel: "C1", TimeStamp: ts, Text: "<@UBOT> see these",
	}, "app_mention", raw))

	if len(*got) != 1 {
		t.Fatalf("dispatched %d messages, want 1", len(*got))
	}
	content := (*got)[0].Content
	if !strings.HasPrefix(content, "see these") || !strings.Contains(content, "| apple | 12 |") {
		t.Fatalf("table not forwarded to agent, content:\n%s", content)
	}
}

func TestHandleEvent_MessageWithTableBlockForwardsTable(t *testing.T) {
	p, got := newDedupTestPlatform(t)
	ts := freshTS(2)
	raw := `{"type":"message","user":"U1","channel":"C1","ts":"` + ts + `","text":"look",
	 "blocks":[` + tableBlock + `]}`
	p.handleEvent(callbackEvent(&slackevents.MessageEvent{
		User: "U1", Channel: "C1", TimeStamp: ts, Text: "look",
	}, "message", raw))

	if len(*got) != 1 {
		t.Fatalf("dispatched %d messages, want 1", len(*got))
	}
	if content := (*got)[0].Content; !strings.Contains(content, "| apple | 12 |") {
		t.Fatalf("table not forwarded to agent, content:\n%s", content)
	}
}

// Covers every non-table block type the renderer handles, so a forwarded
// Block Kit message (e.g. from another app) reaches the agent intact.
func TestRenderBlocks_SectionHeaderContextMarkdown(t *testing.T) {
	ev := `{"text":"x","blocks":[
	 {"type":"header","text":{"type":"plain_text","text":"Daily report"}},
	 {"type":"section","text":{"type":"mrkdwn","text":"*Status*: ok"},"fields":[{"type":"mrkdwn","text":"Owner: bob"}]},
	 {"type":"context","elements":[{"type":"mrkdwn","text":"updated 5m ago"},{"type":"image","image_url":"x"}]},
	 {"type":"markdown","text":"**bold** note"},
	 {"type":"divider"}]}`
	got := extractRichContent(rawJSON(t, ev), "2.1")
	for _, want := range []string{"Daily report", "*Status*: ok", "Owner: bob", "updated 5m ago", "**bold** note"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}

// Rich text inside a shared message: lists, code blocks, quotes and inline
// link / user / channel / emoji / broadcast elements are flattened to text.
func TestRichTextPlain_AllElementKinds(t *testing.T) {
	elements := `[
	 {"type":"rich_text_section","elements":[
	   {"type":"text","text":"hi "},{"type":"user","user_id":"U9"},{"type":"text","text":" in "},
	   {"type":"channel","channel_id":"C9"},{"type":"text","text":" "},{"type":"emoji","name":"wave"},
	   {"type":"text","text":" "},{"type":"broadcast","range":"here"},{"type":"text","text":" see "},
	   {"type":"link","url":"https://example.com","text":"docs"},{"type":"text","text":" or "},
	   {"type":"link","url":"https://example.org"}]},
	 {"type":"rich_text_list","style":"bullet","elements":[
	   {"type":"rich_text_section","elements":[{"type":"text","text":"one"}]},
	   {"type":"rich_text_section","elements":[{"type":"text","text":"two"}]}]},
	 {"type":"rich_text_preformatted","elements":[{"type":"text","text":"go test ./..."}]},
	 {"type":"rich_text_quote","elements":[{"type":"text","text":"quoted"}]}]`
	var v any
	if err := json.Unmarshal([]byte(elements), &v); err != nil {
		t.Fatal(err)
	}
	got := richTextPlain(v)
	for _, want := range []string{
		"hi <@U9> in <#C9> :wave: @here see docs (https://example.com) or https://example.org",
		"- one\n- two", "```\ngo test ./...\n```", "quoted",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}

// Table cells containing newlines or pipes must not break the Markdown table.
func TestRenderTable_EscapesPipesAndNewlines(t *testing.T) {
	var rows any
	if err := json.Unmarshal([]byte(`[[{"type":"raw_text","text":"h"}],[{"type":"raw_text","text":"a|b\nc"}]]`), &rows); err != nil {
		t.Fatal(err)
	}
	if got := renderTable(rows); !strings.Contains(got, `| a\|b c |`) {
		t.Fatalf("unexpected table:\n%s", got)
	}
	if renderTable(nil) != "" || renderTable([]any{}) != "" {
		t.Fatal("empty rows should render nothing")
	}
}
