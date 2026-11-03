package feishu

import (
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// A pre-built Card 2.0 JSON must survive the send path untouched.
//
// Regression: buildReplyContent treated any content as markdown, so a rich
// card was wrapped in another markdown element and ended up nested inside
// itself. The client then rendered the raw card JSON — escaped quotes and
// literal \n included — instead of the card. This happens on every turn that
// produces no intermediate text: the rich card is built at the end and sent
// through p.Send instead of being patched onto an existing preview card.
func TestBuildReplyContent_PassesThroughPrebuiltCard(t *testing.T) {
	card := buildRichCard(core.CardStatusDone, "", nil, "# Title\n\nsome body text", false, "")
	if !isCardJSON(card) {
		t.Fatalf("test setup: buildRichCard did not produce card JSON: %s", card)
	}

	msgType, body := buildReplyContent(card)

	if msgType != larkim.MsgTypeInteractive {
		t.Fatalf("msg_type = %q, want %q", msgType, larkim.MsgTypeInteractive)
	}
	if body != card {
		t.Fatalf("card JSON was rewritten on the way out:\n got: %s\nwant: %s", body, card)
	}
	if n := strings.Count(body, `"schema"`); n != 1 {
		t.Fatalf("card JSON was nested inside itself (%d \"schema\" occurrences): %s", n, body)
	}
}

// The pass-through must not change how plain markdown is delivered.
func TestBuildReplyContent_PlainMarkdownStillRenders(t *testing.T) {
	msgType, body := buildReplyContent("**bold** plain body")

	if msgType != larkim.MsgTypeInteractive {
		t.Fatalf("msg_type = %q, want %q", msgType, larkim.MsgTypeInteractive)
	}
	if !strings.Contains(body, "plain body") {
		t.Fatalf("markdown body missing from card: %s", body)
	}
	if strings.Contains(body, `\"schema\"`) {
		t.Fatalf("markdown was escaped into a JSON string: %s", body)
	}
}

// The status footer has to be merged into an existing card rather than
// wrapping it in a fresh one.
func TestAppendCardFooterElements_MergesIntoExistingCard(t *testing.T) {
	card := buildRichCard(core.CardStatusDone, "", nil, "body text", false, "")

	merged, ok := appendCardFooterElements(card, "elapsed 1m2s")
	if !ok {
		t.Fatalf("appendCardFooterElements reported failure for a valid card")
	}
	if !isCardJSON(merged) {
		t.Fatalf("merged card is no longer card JSON: %s", merged)
	}
	if n := strings.Count(merged, `"schema"`); n != 1 {
		t.Fatalf("merged card was nested inside itself (%d \"schema\" occurrences): %s", n, merged)
	}
	if !strings.Contains(merged, "elapsed 1m2s") {
		t.Fatalf("footer missing from merged card: %s", merged)
	}
	if !strings.Contains(merged, "body text") {
		t.Fatalf("body missing from merged card: %s", merged)
	}
}

// A malformed card must be left alone rather than corrupted.
func TestAppendCardFooterElements_RejectsNonCard(t *testing.T) {
	merged, ok := appendCardFooterElements("not a card", "footer")
	if ok {
		t.Fatalf("appendCardFooterElements accepted a non-card: %s", merged)
	}
	if merged != "not a card" {
		t.Fatalf("non-card content was modified: %s", merged)
	}
}

// A status change must not re-wrap a pre-built card.
//
// Regression: SetPreviewStatus patched the preview message with
// buildCardJSONWithStatus(h.lastContent, status). On the rich card path
// h.lastContent is itself a complete card JSON, so the patch replaced the
// card body with the raw card JSON as markdown text — the client then
// rendered escaped quotes and literal \n instead of the card.
func TestPreviewStatusCardJSON_KeepsPrebuiltCardBody(t *testing.T) {
	card := buildRichCard(core.CardStatusWorking, "", nil, "body text", false, "")

	patched := previewStatusCardJSON(card, core.CardStatusDone)

	if !isCardJSON(patched) {
		t.Fatalf("patched preview is no longer card JSON: %s", patched)
	}
	if n := strings.Count(patched, `"schema"`); n != 1 {
		t.Fatalf("patched card was nested inside itself (%d \"schema\" occurrences): %s", n, patched)
	}
	if !strings.Contains(patched, "body text") {
		t.Fatalf("card body lost on status change: %s", patched)
	}
	if !strings.Contains(patched, `"template":"green"`) {
		t.Fatalf("header template not updated to green: %s", patched)
	}
}

// Plain (non-card) content still gets a fresh card with the status header.
func TestPreviewStatusCardJSON_PlainContentStillWrapped(t *testing.T) {
	patched := previewStatusCardJSON("plain body", core.CardStatusDone)

	if !isCardJSON(patched) {
		t.Fatalf("plain content did not produce card JSON: %s", patched)
	}
	if !strings.Contains(patched, "plain body") {
		t.Fatalf("content missing from card: %s", patched)
	}
}
