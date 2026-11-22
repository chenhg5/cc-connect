package feishu

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

var (
	_ core.RawCardSender = (*Platform)(nil)
	_ core.RawCardUpdater = (*Platform)(nil)
)

// These tests construct SDK data only: no authentication fixtures or HTTP calls.
func TestRawCardReplyUUIDBody(t *testing.T) {
	p := &Platform{}
	body := p.buildReplyMessageReqBodyWithUUID(replyContext{}, larkim.MsgTypeInteractive, `{"body":{}}`, "job-42")
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["uuid"] != "job-42" || got["msg_type"] != larkim.MsgTypeInteractive || got["content"] != `{"body":{}}` {
		t.Fatalf("unexpected reply body: %s", data)
	}
}

func TestRawCardInvalidContextBeforeNetwork(t *testing.T) {
	p := &Platform{}
	if _, err := p.SendRawCard(context.Background(), "foreign context", json.RawMessage(`{}`), ""); err == nil {
		t.Fatal("foreign reply context accepted")
	}
}

func TestRawCardReceiveIDCompatibility(t *testing.T) {
	for input, want := range map[string]string{
		"": larkim.CreateMessageV1ReceiveIDTypeChatId,
		"chat_id": larkim.CreateMessageV1ReceiveIDTypeChatId,
		"open_id": larkim.CreateMessageV1ReceiveIDTypeOpenId,
		"union_id": larkim.CreateMessageV1ReceiveIDTypeUnionId,
	} {
		if got := createMessageReceiveIDType(input); got != want {
			t.Fatalf("receive type %q = %q, want %q", input, got, want)
		}
	}
}
