package feishu

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"html"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func (p *Platform) BotMentionID() string { return p.getBotOpenID() }
func (p *Platform) MentionText(id string) string {
	return "<at id=\"" + html.EscapeString(id) + "\"></at>"
}

func (p *Platform) SendTaskMessage(ctx context.Context, chat, root, mention, text, key string) (string, error) {
	if chat == "" || mention == "" {
		return "", fmt.Errorf("task message requires chat and mention identity")
	}
	return p.sendWorkflowPost(ctx, chat, root, mention, text, key)
}

func (p *Platform) sendWorkflowPost(ctx context.Context, chat, root, mention, text, key string) (string, error) {
	if chat == "" {
		return "", fmt.Errorf("workflow message requires chat identity")
	}
	var rows [][]map[string]string
	if mention != "" {
		rows = append(rows, []map[string]string{{"tag": "at", "user_id": mention}})
	}
	// The md node must occupy its own row. A text node prints Markdown
	// delimiters verbatim, even in an otherwise rich post message.
	rows = append(rows, []map[string]string{{"tag": "md", "text": sanitizeMarkdownURLs(text)}})
	content, err := json.Marshal(map[string]any{"zh_cn": map[string]any{"title": "", "content": rows}})
	if err != nil {
		return "", err
	}
	uuid := fmt.Sprintf("%x", sha256.Sum256([]byte(chat+":"+key)))[:32]
	var id string
	err = p.withTransientRetry(ctx, "send task message", func() error {
		return p.withFreshTenantAccessTokenRetry(ctx, "send task message", func(c *lark.Client, opts ...larkcore.RequestOptionFunc) error {
			if root != "" {
				req := larkim.NewReplyMessageReqBuilder().MessageId(root).Body(larkim.NewReplyMessageReqBodyBuilder().MsgType("post").Content(string(content)).ReplyInThread(false).Uuid(uuid).Build()).Build()
				resp, err := c.Im.Message.Reply(ctx, req, opts...)
				if err != nil {
					return err
				}
				if !resp.Success() {
					return fmt.Errorf("reply task code=%d msg=%s", resp.Code, resp.Msg)
				}
				if resp.Data == nil || resp.Data.MessageId == nil {
					return fmt.Errorf("reply task missing message ID")
				}
				id = *resp.Data.MessageId
			} else {
				req := larkim.NewCreateMessageReqBuilder().ReceiveIdType("chat_id").Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(chat).MsgType("post").Content(string(content)).Uuid(uuid).Build()).Build()
				resp, err := c.Im.Message.Create(ctx, req, opts...)
				if err != nil {
					return err
				}
				if !resp.Success() {
					return fmt.Errorf("create task code=%d msg=%s", resp.Code, resp.Msg)
				}
				if resp.Data == nil || resp.Data.MessageId == nil {
					return fmt.Errorf("create task missing message ID")
				}
				id = *resp.Data.MessageId
			}
			return nil
		})
	})
	return id, err
}

func (p *Platform) SendResultMessage(ctx context.Context, chat, mention, text, key string) (string, error) {
	return p.sendResultMessage(ctx, chat, "", mention, text, key)
}
func (p *Platform) SendQuotedResultMessage(ctx context.Context, chat, sessionKey, mention, text, key string) (string, error) {
	return p.sendResultMessage(ctx, chat, splitTaskSession(sessionKey), mention, text, key)
}
func (p *Platform) sendResultMessage(ctx context.Context, chat, root, mention, text, key string) (string, error) {
	// Keep a large final response intact instead of splitting the handoff.
	encoded, err := json.Marshal(text)
	if err != nil {
		return "", err
	}
	if len(encoded) > 24000 {
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
		name := "result-" + hash[:12] + ".txt"
		if _, sent := p.resultAttachments.Load(chat + ":" + key); !sent {
			if err := p.SendFile(ctx, replyContext{chatID: chat, messageID: root, forceChatReply: true}, core.FileAttachment{MimeType: "text/plain", FileName: name, Data: []byte(text)}); err != nil {
				return "", fmt.Errorf("deliver complete result: %w", err)
			}
			p.resultAttachments.Store(chat+":"+key, true)
		}
		text = p.cardI18n().Tf(core.MsgWorkflowResultFile, name)
	}
	return p.sendWorkflowPost(ctx, chat, root, mention, text, key)
}

var _ core.TaskMessagePlatform = (*Platform)(nil)
var _ core.ResultMessagePlatform = (*Platform)(nil)
