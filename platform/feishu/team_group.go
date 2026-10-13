package feishu

import (
	"context"
	"fmt"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func (p *Platform) CreateProjectGroup(ctx context.Context, name, user, uuid string) (string, error) {
	req := larkim.NewCreateChatReqBuilder().UserIdType("open_id").Uuid(uuid).Body(larkim.NewCreateChatReqBodyBuilder().Name(name).ChatMode("group").ChatType("private").UserIdList([]string{user}).Build()).Build()
	var chatID string
	err := p.withFreshTenantAccessTokenRetry(ctx, "create project group", func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
		resp, err := client.Im.Chat.Create(ctx, req, options...)
		if err != nil {
			return fmt.Errorf("create project group: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("create project group code=%d msg=%s", resp.Code, redactInlineSecrets(resp.Msg))
		}
		if resp.Data == nil || resp.Data.ChatId == nil || *resp.Data.ChatId == "" {
			return fmt.Errorf("create project group returned no chat id")
		}
		chatID = *resp.Data.ChatId
		return nil
	})
	return chatID, err
}

func (p *Platform) EnsureProjectGroupBots(ctx context.Context, chat string, apps []string) error {
	for i := 0; i < len(apps); i += 5 {
		end := i + 5
		if end > len(apps) {
			end = len(apps)
		}
		req := larkim.NewCreateChatMembersReqBuilder().ChatId(chat).MemberIdType("app_id").SucceedType(0).Body(larkim.NewCreateChatMembersReqBodyBuilder().IdList(apps[i:end]).Build()).Build()
		err := p.withFreshTenantAccessTokenRetry(ctx, "add project bots", func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
			resp, err := client.Im.ChatMembers.Create(ctx, req, options...)
			if err != nil {
				return fmt.Errorf("add project bots: %w", err)
			}
			if !resp.Success() {
				return fmt.Errorf("add project bots code=%d msg=%s", resp.Code, redactInlineSecrets(resp.Msg))
			}
			if resp.Data != nil && (len(resp.Data.InvalidIdList)+len(resp.Data.NotExistedIdList)+len(resp.Data.PendingApprovalIdList) > 0) {
				return fmt.Errorf("project bots missing or awaiting approval")
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (p *Platform) ProjectGroupHasBot(ctx context.Context, chat, app string) (bool, error) {
	if app != p.appID {
		return false, fmt.Errorf("membership check must use the bot's own application")
	}
	req := larkim.NewIsInChatChatMembersReqBuilder().ChatId(chat).Build()
	var present bool
	err := p.withFreshTenantAccessTokenRetry(ctx, "check project bot membership", func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
		resp, err := client.Im.ChatMembers.IsInChat(ctx, req, options...)
		if err != nil {
			return fmt.Errorf("check project bot membership: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("check project bot membership code=%d msg=%s", resp.Code, redactInlineSecrets(resp.Msg))
		}
		if resp.Data == nil || resp.Data.IsInChat == nil {
			return fmt.Errorf("missing group membership result")
		}
		present = *resp.Data.IsInChat
		return nil
	})
	return present, err
}

func (p *Platform) ProjectGroupLink(chat string) string {
	domain := "applink.feishu.cn"
	if p.platformName == "lark" {
		domain = "applink.larksuite.com"
	}
	return "https://" + domain + "/client/chat/open?openChatId=" + chat
}
