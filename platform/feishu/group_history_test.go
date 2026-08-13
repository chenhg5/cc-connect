package feishu

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func newGroupHistoryTestPlatform(handler core.MessageHandler) *Platform {
	p := &Platform{
		platformName:          "feishu",
		botOpenID:             "ou_bot",
		groupChatHistoryShare: true,
		dedup:                 &core.MessageDedup{},
		handler:               handler,
	}
	for id, name := range map[string]string{
		"ou_alice": "Alice",
		"ou_bob":   "Bob",
		"ou_john":  "John",
		"ou_other": "Other",
	} {
		p.userNameCache.Store(id, name)
	}
	p.chatNameCache.Store("oc_main", "Main group")
	p.chatNameCache.Store("oc_allowed", "Allowed group")
	p.chatNameCache.Store("oc_forbidden", "Forbidden group")
	return p
}

func makeGroupHistoryEvent(t *testing.T, messageID, userID, msgType, rawContent, chatID, rootID string, mentioned bool) *larkim.P2MessageReceiveV1 {
	t.Helper()
	chatType := "group"
	senderType := "user"
	content := rawContent
	mentions := []*larkim.MentionEvent(nil)
	if mentioned {
		if msgType != "text" {
			t.Fatalf("mentioned test event must be text, got %q", msgType)
		}
		var body struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(rawContent), &body); err != nil {
			t.Fatalf("decode text content: %v", err)
		}
		body.Text = "@_bot " + body.Text
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode text content: %v", err)
		}
		content = string(encoded)
		mentions = []*larkim.MentionEvent{{
			Key:  stringPtr("@_bot"),
			Id:   &larkim.UserId{OpenId: stringPtr("ou_bot")},
			Name: stringPtr("cc-connect"),
		}}
	}
	// The event API uses Unix milliseconds as a decimal string. Keep each test
	// event current so the restart-age guard does not discard it.
	createTime := strconv.FormatInt(time.Now().UnixMilli(), 10)
	return &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId:   &larkim.UserId{OpenId: stringPtr(userID)},
				SenderType: &senderType,
			},
			Message: &larkim.EventMessage{
				MessageId:   stringPtr(messageID),
				RootId:      optionalString(rootID),
				ChatId:      stringPtr(chatID),
				ChatType:    &chatType,
				MessageType: stringPtr(msgType),
				Content:     stringPtr(content),
				Mentions:    mentions,
				CreateTime:  &createTime,
			},
		},
	}
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return stringPtr(value)
}

func sendGroupHistoryText(t *testing.T, p *Platform, messageID, userID, text, chatID, rootID string, mentioned bool) {
	t.Helper()
	content, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		t.Fatalf("marshal text: %v", err)
	}
	if err := p.onMessage(context.Background(), makeGroupHistoryEvent(t, messageID, userID, "text", string(content), chatID, rootID, mentioned)); err != nil {
		t.Fatalf("onMessage(%s): %v", messageID, err)
	}
}

func sendGroupHistoryPost(t *testing.T, p *Platform, messageID, userID, text, chatID, rootID string) {
	t.Helper()
	content := `{"content":[[{"tag":"text","text":"` + text + `"}]]}`
	if err := p.onMessage(context.Background(), makeGroupHistoryEvent(t, messageID, userID, "post", content, chatID, rootID, false)); err != nil {
		t.Fatalf("onMessage(%s): %v", messageID, err)
	}
}

func awaitGroupHistoryMessage(t *testing.T, messages <-chan *core.Message) *core.Message {
	t.Helper()
	select {
	case msg := <-messages:
		return msg
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Feishu handler message")
		return nil
	}
}

func assertNoGroupHistoryMessage(t *testing.T, messages <-chan *core.Message) {
	t.Helper()
	select {
	case msg := <-messages:
		t.Fatalf("unexpected handler message: %#v", msg)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestFeishuGroupHistory_DisabledPreservesMentionFilter(t *testing.T) {
	messages := make(chan *core.Message, 1)
	p := newGroupHistoryTestPlatform(func(_ core.Platform, msg *core.Message) { messages <- msg })
	p.groupChatHistoryShare = false

	sendGroupHistoryText(t, p, "m1", "ou_alice", "ordinary context", "oc_main", "", false)
	assertNoGroupHistoryMessage(t, messages)
}

func TestFeishuGroupHistory_ConfigDefaultsOffAndCanBeEnabled(t *testing.T) {
	defaultPlatform, err := New(map[string]any{"app_id": "cli_history_default", "app_secret": "secret"})
	if err != nil {
		t.Fatalf("New(default) error: %v", err)
	}
	if base := extractBasePlatform(defaultPlatform); base.groupChatHistoryShare {
		t.Fatal("group_chat_history_share should default to false")
	}

	enabledPlatform, err := New(map[string]any{
		"app_id":                   "cli_history_enabled",
		"app_secret":               "secret",
		"group_chat_history_share": true,
	})
	if err != nil {
		t.Fatalf("New(enabled) error: %v", err)
	}
	if base := extractBasePlatform(enabledPlatform); !base.groupChatHistoryShare {
		t.Fatal("group_chat_history_share=true was not applied")
	}
}

func TestFeishuGroupHistory_MainChannelSharesOrderedMessagesAndConsumesOnce(t *testing.T) {
	messages := make(chan *core.Message, 4)
	p := newGroupHistoryTestPlatform(func(_ core.Platform, msg *core.Message) { messages <- msg })

	sendGroupHistoryText(t, p, "m1", "ou_alice", "I think the return data is wrong", "oc_main", "", false)
	sendGroupHistoryPost(t, p, "m2", "ou_bob", "Web store looks especially strange", "oc_main", "")
	sendGroupHistoryText(t, p, "m3", "ou_alice", "SU may have the same issue", "oc_main", "", false)
	assertNoGroupHistoryMessage(t, messages)

	sendGroupHistoryText(t, p, "m4", "ou_john", "check this", "oc_main", "", true)
	first := awaitGroupHistoryMessage(t, messages)
	if first.Content != "check this" {
		t.Fatalf("trigger content = %q, want check this", first.Content)
	}
	for _, want := range []string{"Alice:\nI think the return data is wrong", "Bob:\nWeb store looks especially strange", "Alice:\nSU may have the same issue"} {
		if !strings.Contains(first.ExtraContent, want) {
			t.Fatalf("history missing %q: %q", want, first.ExtraContent)
		}
	}
	if strings.Index(first.ExtraContent, "Alice:\nI think") > strings.Index(first.ExtraContent, "Bob:\nWeb") {
		t.Fatalf("history order was not preserved: %q", first.ExtraContent)
	}
	if first.OnAccepted == nil {
		t.Fatal("trigger should have a delayed history-consumption callback")
	}
	first.OnAccepted()

	sendGroupHistoryText(t, p, "m5", "ou_john", "nothing old should repeat", "oc_main", "", true)
	second := awaitGroupHistoryMessage(t, messages)
	if second.ExtraContent != "" {
		t.Fatalf("consumed history repeated: %q", second.ExtraContent)
	}
}

func TestFeishuGroupHistory_StatusDoesNotConsumePendingContext(t *testing.T) {
	messages := make(chan *core.Message, 2)
	p := newGroupHistoryTestPlatform(func(_ core.Platform, msg *core.Message) { messages <- msg })

	sendGroupHistoryText(t, p, "m1", "ou_alice", "keep this for the next real turn", "oc_main", "", false)
	sendGroupHistoryText(t, p, "m2", "ou_john", "/status", "oc_main", "", true)
	status := awaitGroupHistoryMessage(t, messages)
	if status.Content != "/status" || !strings.Contains(status.ExtraContent, "keep this for the next real turn") {
		t.Fatalf("status message lost pending context: content=%q extra=%q", status.Content, status.ExtraContent)
	}
	if status.OnAccepted == nil {
		t.Fatal("status snapshot should retain a consumption callback for a later real turn")
	}

	sendGroupHistoryText(t, p, "m3", "ou_john", "real turn", "oc_main", "", true)
	realTurn := awaitGroupHistoryMessage(t, messages)
	if !strings.Contains(realTurn.ExtraContent, "keep this for the next real turn") {
		t.Fatalf("pending context was consumed by /status: %q", realTurn.ExtraContent)
	}
	realTurn.OnAccepted()
}

func TestFeishuGroupHistory_NewResetsPendingContext(t *testing.T) {
	messages := make(chan *core.Message, 2)
	p := newGroupHistoryTestPlatform(func(_ core.Platform, msg *core.Message) { messages <- msg })

	sendGroupHistoryText(t, p, "m1", "ou_alice", "old context", "oc_main", "", false)
	sendGroupHistoryText(t, p, "m2", "ou_john", "/new", "oc_main", "", true)
	newMessage := awaitGroupHistoryMessage(t, messages)
	if newMessage.ExtraContent != "" {
		t.Fatalf("/new received stale context: %q", newMessage.ExtraContent)
	}

	sendGroupHistoryText(t, p, "m3", "ou_bob", "new context", "oc_main", "", false)
	sendGroupHistoryText(t, p, "m4", "ou_john", "continue", "oc_main", "", true)
	continueMessage := awaitGroupHistoryMessage(t, messages)
	if strings.Contains(continueMessage.ExtraContent, "old context") || !strings.Contains(continueMessage.ExtraContent, "new context") {
		t.Fatalf("/new did not reset the main history scope: %q", continueMessage.ExtraContent)
	}
}

func TestFeishuGroupHistory_ThreadScopesDoNotLeakAndMainDoesNotFollowFork(t *testing.T) {
	messages := make(chan *core.Message, 8)
	p := newGroupHistoryTestPlatform(func(_ core.Platform, msg *core.Message) { messages <- msg })
	p.threadIsolation = true

	// A main-channel mention forks an agent session, but does not become
	// pending history for the next main-channel mention.
	sendGroupHistoryText(t, p, "main-trigger-a", "ou_john", "task A", "oc_main", "", true)
	mainA := awaitGroupHistoryMessage(t, messages)
	if mainA.ExtraContent != "" {
		t.Fatalf("first main trigger unexpectedly had context: %q", mainA.ExtraContent)
	}
	sendGroupHistoryText(t, p, "main-context-1", "ou_alice", "context B1", "oc_main", "", false)
	sendGroupHistoryText(t, p, "main-context-2", "ou_bob", "context B2", "oc_main", "", false)
	sendGroupHistoryText(t, p, "main-trigger-b", "ou_john", "task B", "oc_main", "", true)
	mainB := awaitGroupHistoryMessage(t, messages)
	if !strings.Contains(mainB.ExtraContent, "context B1") || !strings.Contains(mainB.ExtraContent, "context B2") || strings.Contains(mainB.ExtraContent, "task A") {
		t.Fatalf("main-channel history attached to the forked session: %q", mainB.ExtraContent)
	}
	mainB.OnAccepted()

	// Thread roots each have an independent pending queue.
	// Upstream bootstraps a pre-existing thread's reply chain on first
	// engagement. Mark these synthetic threads active so this scope test does
	// not require a Feishu API client; bootstrap behavior has dedicated tests.
	p.markThreadSessionActive("feishu:oc_main:root:root-a")
	p.markThreadSessionActive("feishu:oc_main:root:root-b")
	sendGroupHistoryText(t, p, "thread-a-context", "ou_alice", "thread A context", "oc_main", "root-a", false)
	sendGroupHistoryText(t, p, "thread-b-context", "ou_bob", "thread B context", "oc_main", "root-b", false)
	sendGroupHistoryText(t, p, "thread-a-trigger", "ou_john", "continue A", "oc_main", "root-a", true)
	threadA := awaitGroupHistoryMessage(t, messages)
	if !strings.Contains(threadA.ExtraContent, "thread A context") || strings.Contains(threadA.ExtraContent, "thread B context") {
		t.Fatalf("thread A received leaked history: %q", threadA.ExtraContent)
	}
	threadA.OnAccepted()

	sendGroupHistoryText(t, p, "thread-b-trigger", "ou_john", "continue B", "oc_main", "root-b", true)
	threadB := awaitGroupHistoryMessage(t, messages)
	if !strings.Contains(threadB.ExtraContent, "thread B context") || strings.Contains(threadB.ExtraContent, "thread A context") {
		t.Fatalf("thread B received leaked history: %q", threadB.ExtraContent)
	}
}

func TestFeishuGroupHistory_PostAndSenderTypeFormatting(t *testing.T) {
	p := newGroupHistoryTestPlatform(nil)
	p.peerBots = map[string]string{"cli_peer": "PeerBot"}
	p.rememberGroupHistory("chat:oc_main", "post-1", "post", `{"title":"Topic","content":[[{"tag":"text","text":"post body"}]]}`, nil, "ou_alice", "user")
	p.rememberGroupHistory("chat:oc_main", "text-1", "text", `{"text":"bot observation"}`, nil, "cli_peer", "app")
	ctx := p.snapshotGroupHistory("chat:oc_main")
	formatted := p.formatGroupHistory(ctx)
	if !strings.Contains(formatted, "Alice:\nTopic\npost body") {
		t.Fatalf("post history was not formatted with sender name: %q", formatted)
	}
	if !strings.Contains(formatted, "PeerBot:\nbot observation") {
		t.Fatalf("app sender was not resolved: %q", formatted)
	}
}

func TestFeishuGroupHistory_ChatAndTriggerPermissionsRemainSeparate(t *testing.T) {
	messages := make(chan *core.Message, 1)
	p := newGroupHistoryTestPlatform(func(_ core.Platform, msg *core.Message) { messages <- msg })
	p.allowChat = "oc_allowed"
	p.allowFrom = "ou_john"

	sendGroupHistoryText(t, p, "forbidden-context", "ou_alice", "must not be observed", "oc_forbidden", "", false)
	sendGroupHistoryText(t, p, "allowed-context", "ou_alice", "allowed chat context", "oc_allowed", "", false)
	sendGroupHistoryText(t, p, "allowed-trigger", "ou_john", "use context", "oc_allowed", "", true)
	msg := awaitGroupHistoryMessage(t, messages)
	if strings.Contains(msg.ExtraContent, "must not be observed") || !strings.Contains(msg.ExtraContent, "allowed chat context") {
		t.Fatalf("chat permission boundary was not respected: %q", msg.ExtraContent)
	}
}

func TestFeishuGroupHistory_GroupReplyAllRemainsImmediate(t *testing.T) {
	messages := make(chan *core.Message, 1)
	p := newGroupHistoryTestPlatform(func(_ core.Platform, msg *core.Message) { messages <- msg })
	p.groupReplyAll = true

	sendGroupHistoryText(t, p, "m1", "ou_alice", "reply immediately", "oc_main", "", false)
	msg := awaitGroupHistoryMessage(t, messages)
	if msg.Content != "reply immediately" || msg.ExtraContent != "" {
		t.Fatalf("group_reply_all behavior changed: content=%q extra=%q", msg.Content, msg.ExtraContent)
	}
}
