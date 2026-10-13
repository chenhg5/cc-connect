package feishu

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestPrivateDecisionReminderReturnsToOriginalCardAndIsDurable(t *testing.T) {
	dir := t.TempDir()
	p, f := newSplitCardFixture(t, false, map[string]any{"cc_data_dir": dir})
	p.chatNameCache.Store("oc_group", "Project group")
	c := &queryCardStream{p: p.Platform, native: true, rc: replyContext{chatID: "oc_group", messageID: "source", sessionKey: "feishu:oc_group:root:source", forceChatReply: true}}
	c.blocks = []core.ExecutionBlock{{Text: "Check build", Done: true}, {Tool: "Bash", Command: "secret command", Output: "private log"}}
	if err := c.writeLocked(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.ConfigureDecisionNotification("ou_owner", "Project · Build", p.SendPrivateDecisionNotification)
	prompt := core.NewCard().Title("Approve deployment", "orange").Markdown("ORIGINAL_SCOPE_DETAILS").Buttons(core.PrimaryBtn("Approve", "perm:allow")).Build()
	if err := c.SetPrompt(context.Background(), prompt); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 2 {
		t.Fatalf("expected process card plus one DM, got %d", len(f.requests))
	}
	dm := f.requests[1]
	if dm["receive_id"] != "ou_owner" || dm["path"] != "/open-apis/im/v1/messages" {
		t.Fatal("reminder not delivered privately")
	}
	raw := dm["content"].(string)
	for _, forbidden := range []string{"ORIGINAL_SCOPE_DETAILS", "secret command", "private log", "perm:allow"} {
		if strings.Contains(raw, forbidden) {
			t.Fatal("reminder leaked request controls/details")
		}
	}
	if !strings.Contains(raw, "position=42") || !strings.Contains(f.shown("om_1"), "ORIGINAL_SCOPE_DETAILS") {
		t.Fatal("source link or original request missing")
	}
	if err := c.SetPrompt(context.Background(), prompt); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 2 {
		t.Fatal("same pending request sent a duplicate DM")
	}
	n := *c.cards[0].notification
	restarted, again := newSplitCardFixture(t, false, map[string]any{"cc_data_dir": dir})
	if err := restarted.SendPrivateDecisionNotification(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	if len(again.requests) != 0 {
		t.Fatal("restart lost notification deduplication")
	}
}

func TestPrivateReminderFailureDoesNotDenyPromptOrFallbackToGroup(t *testing.T) {
	p, f := newSplitCardFixture(t, false, map[string]any{"cc_data_dir": t.TempDir()})
	p.chatNameCache.Store("oc_group", "Project")
	c := &queryCardStream{p: p.Platform, native: true, rc: replyContext{chatID: "oc_group", sessionKey: "feishu:oc_group:root:source"}}
	c.blocks = []core.ExecutionBlock{{Text: "Progress", Done: true}}
	if err := c.writeLocked(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.ConfigureDecisionNotification("ou_owner", "Project", p.SendPrivateDecisionNotification)
	f.failCreate = true
	prompt := core.NewCard().Title("Question", "orange").Markdown("Which target?").Build()
	prompt.DecisionToken = "current-token"
	if err := c.SetPrompt(context.Background(), prompt); err != nil {
		t.Fatalf("notification failure failed the original question: %v", err)
	}
	shown := f.shown("om_1")
	if !strings.Contains(shown, "Which target?") || !strings.Contains(shown, "私聊提醒未送达") {
		t.Fatal("failure or pending question hidden")
	}
	for _, req := range f.requests[1:] {
		if req["receive_id"] != "ou_owner" {
			t.Fatal("private reminder fell back to group")
		}
	}
	before := len(f.requests)
	f.failCreate = false
	part := c.cards[0]
	executionClick(t, part, "remind", "")
	if len(f.requests) != before+1 || strings.Contains(f.shown("om_1"), "私聊提醒未送达") {
		t.Fatal("retry failed")
	}
	if f.requests[1]["uuid"] != f.requests[len(f.requests)-1]["uuid"] {
		t.Fatal("retry lost idempotency key")
	}
}

func TestExecutionCallbacksRejectCrossMessageAndStaleRevision(t *testing.T) {
	p, _ := newSplitCardFixture(t, true)
	c := &queryCardStream{p: p.Platform, native: true, rc: replyContext{chatID: "oc_private"}, blocks: []core.ExecutionBlock{{Tool: "Bash", Command: "echo ok", Done: true}}}
	if err := c.writeLocked(context.Background()); err != nil {
		t.Fatal(err)
	}
	part := c.cards[0]
	action := fmt.Sprintf("exec:%s:open:b0:0", part.id)
	if err := p.navigateExecution(action, "another-chat", "om_1", "ou_owner", nil); err == nil {
		t.Fatal("cross chat callback accepted")
	}
	if err := p.navigateExecution(action, "oc_private", "another-message", "ou_owner", nil); err == nil {
		t.Fatal("cross message callback accepted")
	}
	click := panelClick("om_1", action)
	if _, err := p.onCardAction(click); err != nil {
		t.Fatal(err)
	}
	if part.view.Key != "b0" {
		t.Fatal("real callback not routed to details")
	}
	if err := p.navigateExecution(action, "oc_private", "om_1", "ou_owner", nil); err == nil {
		t.Fatal("stale revision overwrote view")
	}
}

func TestFinalResultQuotesIncomingMessageWithRealMention(t *testing.T) {
	p, f := newSplitCardFixture(t, false)
	if _, err := p.SendQuotedResultMessage(context.Background(), "oc_group", "feishu:oc_group:root:source", "ou_manager", "Final result", "result-1"); err != nil {
		t.Fatal(err)
	}
	req := f.requests[0]
	if req["path"] != "/open-apis/im/v1/messages/source/reply" || req["reply_in_thread"] != false || !strings.Contains(req["content"].(string), "ou_manager") {
		t.Fatal("result lost source, ordinary reply or mention")
	}
}
