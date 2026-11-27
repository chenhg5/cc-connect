package feishu

import (
	"context"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// feishuMessageTransport allows tests to inspect built requests without
// requiring credentials. The nil transport always uses the normal SDK.
type feishuMessageTransport struct {
	create func(context.Context, string, *larkim.CreateMessageReqBody) (*larkim.CreateMessageResp, error)
	reply  func(context.Context, string, *larkim.ReplyMessageReqBody) (*larkim.ReplyMessageResp, error)
	patch  func(context.Context, string, *larkim.PatchMessageReqBody) (*larkim.PatchMessageResp, error)
}

func (p *Platform) callCreateMessage(ctx context.Context, client *lark.Client, receiveIDType string, body *larkim.CreateMessageReqBody, req *larkim.CreateMessageReq, options ...larkcore.RequestOptionFunc) (*larkim.CreateMessageResp, error) {
	if p.messageTransport != nil {
		return p.messageTransport.create(ctx, receiveIDType, body)
	}
	return client.Im.Message.Create(ctx, req, options...)
}

func (p *Platform) callReplyMessage(ctx context.Context, client *lark.Client, messageID string, body *larkim.ReplyMessageReqBody, req *larkim.ReplyMessageReq, options ...larkcore.RequestOptionFunc) (*larkim.ReplyMessageResp, error) {
	if p.messageTransport != nil {
		return p.messageTransport.reply(ctx, messageID, body)
	}
	return client.Im.Message.Reply(ctx, req, options...)
}

func (p *Platform) callPatchMessage(ctx context.Context, client *lark.Client, messageID string, body *larkim.PatchMessageReqBody, req *larkim.PatchMessageReq, options ...larkcore.RequestOptionFunc) (*larkim.PatchMessageResp, error) {
	if p.messageTransport != nil {
		return p.messageTransport.patch(ctx, messageID, body)
	}
	return client.Im.Message.Patch(ctx, req, options...)
}
