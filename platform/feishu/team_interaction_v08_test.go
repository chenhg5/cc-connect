package feishu

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestNativeTaskDispatchAndReworkUseRealMentionsAndNewGroupMessages(t *testing.T) {
	p, f := newSplitCardFixture(t, false)
	_, err := p.SendTaskMessage(context.Background(), "oc_group", "", "ou_member", "Implement this", "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.SendTaskMessage(context.Background(), "oc_group", "", "ou_member", "T001 · Rework: Correct the edge case", "task-2"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 2 || f.requests[0]["path"] != "/open-apis/im/v1/messages" || f.requests[1]["path"] != "/open-apis/im/v1/messages" || f.requests[1]["reply_in_thread"] == true {
		t.Fatalf("wrong routing: %+v", f.requests)
	}
	for _, req := range f.requests {
		var post map[string]any
		if err := json.Unmarshal([]byte(req["content"].(string)), &post); err != nil {
			t.Fatal(err)
		}
		line := post["zh_cn"].(map[string]any)["content"].([]any)[0].([]any)
		at := line[0].(map[string]any)
		if at["tag"] != "at" || at["user_id"] != "ou_member" {
			t.Fatalf("fake mention: %+v", at)
		}
	}
}
func TestDecisionReminderMainGroupAndLiteralFormCallback(t *testing.T) {
	p, f := newSplitCardFixture(t, false)
	p.userNameCache.Store("ou_owner", "Owner")
	p.chatNameCache.Store("oc_private", "Group")
	sc, err := p.CreateDecisionReminder(context.Background(), "oc_private", "feishu:oc_private:root:task", "ou_owner")
	if err != nil {
		t.Fatal(err)
	}
	prompt := core.NewCard().Title("Original question", "blue").Markdown("Which environment?").Buttons(core.PrimaryBtn("Test", "askq:0:1")).Build()
	prompt.DecisionToken = "request-token"
	if err = sc.SetPrompt(context.Background(), prompt); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	req := f.requests[0]
	f.mu.Unlock()
	if req["path"] != "/open-apis/im/v1/messages/task/reply" || req["reply_in_thread"] != false {
		t.Fatal("reminder hidden in thread")
	}
	shown := f.shown("om_1")
	if !strings.Contains(shown, "ou_owner") {
		t.Fatal("recipient mention folded/absent")
	}
	received := make(chan *core.Message, 1)
	p.handler = func(_ core.Platform, msg *core.Message) { received <- msg }
	click := panelClick("om_1", "question_submit")
	click.Event.Action.Name = "cc_question_submit"
	click.Event.Action.Value["query_card"] = "true"
	click.Event.Action.Value["permission_token"] = "request-token"
	click.Event.Action.Value["session_key"] = "feishu:oc_private:root:task"
	click.Event.Action.FormValue = map[string]any{"answer": " 1\n custom "}
	if _, err = p.onCardAction(click); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-received:
		if msg.Content != " 1\n custom " || !msg.LiteralAnswer || msg.ChannelID != "oc_private" || msg.SessionKey != "feishu:oc_private:root:task" {
			t.Fatalf("lost answer association: %+v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("form submit lost")
	}
	if err = sc.SetNotice(context.Background(), "Answered"); err != nil {
		t.Fatal(err)
	}
	if err = sc.SetPrompt(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	shown = f.shown("om_1")
	if strings.Contains(shown, "cc_question_submit") || strings.Contains(shown, "askq:") || !strings.Contains(shown, "Which environment?") || !strings.Contains(shown, "Answered") {
		t.Fatal("resolved card lost original question or retained controls")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.creates != 1 {
		t.Fatal("resolution posted another reminder")
	}
}

func TestNativeRunningCallKeepsOriginalCardWhenOutputGrows(t *testing.T) {
	p, _ := newSplitCardFixture(t, false)
	c := &queryCardStream{p: p.Platform, native: true, rc: replyContext{chatID: "group"}, blocks: []core.ExecutionBlock{{Text: "Run both calls", Done: true}, {Tool: "A", Command: "short"}, {Tool: "B", Command: "short"}}}
	if err := c.writeLocked(context.Background()); err != nil {
		t.Fatal(err)
	}
	id := c.cards[0].id
	c.blocks[2].Done = true
	c.blocks[2].Output = strings.Repeat("result B\n", 700)
	c.blocks[1].Done = true
	c.blocks[1].Output = strings.Repeat("result A\n", 700)
	if err := c.writeLocked(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(c.cards) != 1 || c.cards[0].id != id || len(c.cards[0].blocks) != 3 {
		t.Fatal("running calls split or moved when only output grew")
	}
}
