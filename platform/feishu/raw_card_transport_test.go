package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestRawCardTransportPreservesCreateReplyAndUpdate(t *testing.T) {
	ctx := context.Background()
	card := json.RawMessage(`{"schema":"2.0","body":{"elements":[]}}`)
	update := json.RawMessage(`{"schema":"2.0","body":{"elements":[{"tag":"markdown","content":"Updated"}]}}`)
	const uuid = "card-job-42"
	created, replied, patched := 0, 0, 0
	idCreate, idReply := "om_created_card", "om_replied_card"
	p := &Platform{messageTransport: &feishuMessageTransport{
		create: func(_ context.Context, receiveType string, body *larkim.CreateMessageReqBody) (*larkim.CreateMessageResp, error) {
			created++
			if receiveType != larkim.CreateMessageV1ReceiveIDTypeOpenId ||
				body == nil || body.ReceiveId == nil || *body.ReceiveId != "ou_card_recipient" ||
				body.MsgType == nil || *body.MsgType != larkim.MsgTypeInteractive ||
				body.Content == nil || *body.Content != string(card) ||
				body.Uuid == nil || *body.Uuid != uuid {
				t.Fatalf("wrong create destination or request: type=%q body=%+v", receiveType, body)
			}
			return &larkim.CreateMessageResp{Data: &larkim.CreateMessageRespData{MessageId: &idCreate}}, nil
		},
		reply: func(_ context.Context, parent string, body *larkim.ReplyMessageReqBody) (*larkim.ReplyMessageResp, error) {
			replied++
			if parent != "om_parent" || body == nil ||
				body.MsgType == nil || *body.MsgType != larkim.MsgTypeInteractive ||
				body.Content == nil || *body.Content != string(card) ||
				body.Uuid == nil || *body.Uuid != uuid {
				t.Fatalf("wrong reply destination or request: parent=%q body=%+v", parent, body)
			}
			return &larkim.ReplyMessageResp{Data: &larkim.ReplyMessageRespData{MessageId: &idReply}}, nil
		},
		patch: func(_ context.Context, target string, body *larkim.PatchMessageReqBody) (*larkim.PatchMessageResp, error) {
			patched++
			if target != idReply || body == nil || body.Content == nil || *body.Content != string(update) {
				t.Fatalf("wrong patch destination or request: target=%q body=%+v", target, body)
			}
			return &larkim.PatchMessageResp{}, nil
		},
	}}
	if id, err := p.SendRawCard(ctx, replyContext{chatID: "ou_card_recipient", receiveIDType: "open_id"}, card, uuid); id != idCreate || err != nil {
		t.Fatalf("create: id=%q error=%v", id, err)
	}
	if id, err := p.SendRawCard(ctx, replyContext{chatID: "oc_group", messageID: "om_parent"}, card, uuid); id != idReply || err != nil {
		t.Fatalf("reply: id=%q error=%v", id, err)
	}
	if err := p.UpdateRawCard(ctx, idReply, update); err != nil {
		t.Fatal(err)
	}
	if created != 1 || replied != 1 || patched != 1 {
		t.Fatalf("wrong route counts: create=%d reply=%d patch=%d", created, replied, patched)
	}
}

func TestRawCardTransportRefusalsPreserveFeishuCode(t *testing.T) {
	for _, code := range []int{230099, 230025} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			created, replied := 0, 0
			refusal := larkcore.CodeError{Code: code, Msg: "card refused"}
			p := &Platform{messageTransport: &feishuMessageTransport{
				create: func(context.Context, string, *larkim.CreateMessageReqBody) (*larkim.CreateMessageResp, error) {
					created++
					return &larkim.CreateMessageResp{CodeError: refusal}, nil
				},
				reply: func(context.Context, string, *larkim.ReplyMessageReqBody) (*larkim.ReplyMessageResp, error) {
					replied++
					return &larkim.ReplyMessageResp{CodeError: refusal}, nil
				},
			}}
			for _, rc := range []replyContext{{chatID: "oc_group"}, {chatID: "oc_group", messageID: "om_parent"}} {
				id, err := p.SendRawCard(context.Background(), rc, json.RawMessage(`{"body":{}}`), "card-job")
				if id != "" || err == nil || !strings.Contains(err.Error(), fmt.Sprintf("code=%d", code)) {
					t.Fatalf("refusal got id=%q error=%v; want code=%d", id, err, code)
				}
			}
			if created != 1 || replied != 1 {
				t.Fatalf("non-retryable refusal was retried: create=%d reply=%d", created, replied)
			}
		})
	}
}
