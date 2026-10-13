package feishu

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/chenhg5/cc-connect/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

var _ core.GroupMessagePolicyConfigurer = (*Platform)(nil)

func (p *Platform) SetGroupMessagePolicy(fn func(string, string) core.GroupMessagePolicy) {
	p.groupMessagePolicy = fn
}
func (p *Platform) ProjectConversationKey(chatID, userID string) string {
	group := "group"
	return p.makeSessionKey(&larkim.EventMessage{ChatType: &group}, chatID, userID)
}

func (p *Platform) SendPrivateUser(ctx context.Context, userID, text string) error {
	content, _ := json.Marshal(map[string]string{"text": text})
	req := larkim.NewCreateMessageReqBuilder().ReceiveIdType("open_id").Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(userID).MsgType("text").Content(string(content)).Build()).Build()
	resp, err := p.client.Im.Message.Create(ctx, req)
	if err != nil {
		return fmt.Errorf("send private authorization: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("send private authorization code=%d", resp.Code)
	}
	return nil
}
