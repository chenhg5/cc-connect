package feishu

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func (p *Platform) SetTaskThreadResolver(fn func(string, string) bool) { p.taskThreadResolver = fn }
func (p *Platform) TaskConversationKey(chat, root string) string {
	return p.tag() + ":" + chat + ":root:" + root
}
func (p *Platform) isTaskThread(rc replyContext) bool {
	if p.taskThreadResolver == nil {
		return false
	}
	parts := splitTaskSession(rc.sessionKey)
	return parts != "" && p.taskThreadResolver(rc.chatID, parts)
}
func splitTaskSession(key string) string {
	// Reuse native root/thread parsing without treating other session kinds as tasks.
	for i := 0; i < len(key); i++ {
		if key[i] == ':' {
			for j := i + 1; j < len(key); j++ {
				if key[j] == ':' {
					root, _ := parseThreadRootID(key[j+1:])
					return root
				}
			}
			break
		}
	}
	return ""
}
func (p *Platform) UpsertWorkflowCard(ctx context.Context, chat, id, uuid string, card *core.Card) (string, error) {
	body := renderCard(card, p.ProjectConversationKey(chat, ""))
	// Explicitly update a known message. Never replace on a transient update failure.
	if id != "" {
		req := larkim.NewPatchMessageReqBuilder().MessageId(id).Body(larkim.NewPatchMessageReqBodyBuilder().Content(body).Build()).Build()
		err := p.withTransientRetry(ctx, "update workflow card", func() error {
			return p.withFreshTenantAccessTokenRetry(ctx, "update workflow card", func(c *lark.Client, opts ...larkcore.RequestOptionFunc) error {
				resp, err := c.Im.Message.Patch(ctx, req, opts...)
				if err != nil {
					return err
				}
				if !resp.Success() {
					return fmt.Errorf("update workflow card code=%d msg=%s", resp.Code, resp.Msg)
				}
				return nil
			})
		})
		return id, err
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(uuid)))[:32]
	req := larkim.NewCreateMessageReqBuilder().ReceiveIdType("chat_id").Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(chat).MsgType("interactive").Content(body).Uuid(hash).Build()).Build()
	err := p.withTransientRetry(ctx, "create workflow card", func() error {
		return p.withFreshTenantAccessTokenRetry(ctx, "create workflow card", func(c *lark.Client, opts ...larkcore.RequestOptionFunc) error {
			resp, err := c.Im.Message.Create(ctx, req, opts...)
			if err != nil {
				return err
			}
			if !resp.Success() {
				return fmt.Errorf("create workflow card code=%d msg=%s", resp.Code, resp.Msg)
			}
			if resp.Data == nil || resp.Data.MessageId == nil {
				return fmt.Errorf("workflow card response missing message ID")
			}
			id = *resp.Data.MessageId
			return nil
		})
	})
	return id, err
}
func (p *interactivePlatform) CreateTaskStreamingCard(ctx context.Context, rctx any, key string) (core.StreamingCard, error) {
	rc, ok := rctx.(replyContext)
	if !ok {
		return nil, fmt.Errorf("invalid task reply context")
	}
	rc.forceChatReply = true
	c := &queryCardStream{p: p.Platform, native: true, rc: rc, status: core.CardStatusWorking, taskKey: key}
	if err := c.restoreTaskParts(); err != nil {
		return nil, err
	}
	if err := c.writeLocked(ctx); err != nil {
		return nil, err
	}
	return c, nil
}
func (c *queryCardStream) taskPartID(part int) string {
	return "task-" + fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%t:%d", c.rc.chatID, c.taskKey, c.answer, part))))
}
func (c *queryCardStream) restoreTaskParts() error {
	if c.taskKey == "" {
		return nil
	}
	for i := 1; ; i++ {
		part, err := c.p.loadQueryCard(c.taskPartID(i))
		if err != nil {
			if err == errQueryCardExpired {
				if c.p.queryCardStateDir != "" {
					if _, statErr := os.Stat(filepath.Join(c.p.queryCardStateDir, c.taskPartID(i)+".json")); !os.IsNotExist(statErr) {
						return fmt.Errorf("task card snapshot is unreadable; refusing to create a duplicate")
					}
				}
				break
			}
			return err
		}
		if part.rc.chatID != c.rc.chatID || part.rc.sessionKey != c.rc.sessionKey {
			return fmt.Errorf("task card conversation mismatch")
		}
		c.cards = append(c.cards, part)
	}
	return nil
}

// Resolve positions from the message API. Never fabricate a message-link token.
// AppLink query format matches the official larksuite/cli IM converter.
func (p *Platform) WorkflowMessageLink(ctx context.Context, id string) (string, error) {
	var link string
	req := larkim.NewGetMessageReqBuilder().MessageId(id).Build()
	err := p.withTransientRetry(ctx, "workflow source", func() error {
		return p.withFreshTenantAccessTokenRetry(ctx, "workflow source", func(c *lark.Client, opts ...larkcore.RequestOptionFunc) error {
			resp, err := c.Im.Message.Get(ctx, req, opts...)
			if err != nil {
				return err
			}
			if !resp.Success() {
				return fmt.Errorf("source message unavailable (code %d)", resp.Code)
			}
			if resp.ApiResp == nil {
				return fmt.Errorf("source message response missing")
			}
			link, err = workflowSourceLink(resp.RawBody, p.platformName == "lark")
			return err
		})
	})
	if err != nil {
		return "", fmt.Errorf("feishu: source message: %w", err)
	}
	return link, nil
}
func workflowSourceLink(raw []byte, international bool) (string, error) {
	var response struct {
		Data struct {
			Items []struct {
				ChatID         string      `json:"chat_id"`
				ThreadID       string      `json:"thread_id"`
				Position       json.Number `json:"message_position"`
				ThreadPosition json.Number `json:"thread_message_position"`
				Deleted        bool        `json:"deleted"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return "", fmt.Errorf("invalid source message data")
	}
	if len(response.Data.Items) != 1 || response.Data.Items[0].Deleted {
		return "", fmt.Errorf("source message unavailable")
	}
	m := response.Data.Items[0]
	if m.ChatID == "" {
		return "", fmt.Errorf("source chat unavailable")
	}
	host := "applink.feishu.cn"
	if international {
		host = "applink.larksuite.com"
	}
	u := url.URL{Scheme: "https", Host: host}
	q := url.Values{}
	if n, err := m.ThreadPosition.Int64(); m.ThreadID != "" && err == nil {
		u.Path = "/client/thread/open"
		q.Set("openchatid", m.ChatID)
		q.Set("open_chat_id", m.ChatID)
		q.Set("openthreadid", m.ThreadID)
		q.Set("open_thread_id", m.ThreadID)
		q.Set("thread_position", strconv.FormatInt(n, 10))
	} else if n, err := m.Position.Int64(); err == nil {
		u.Path = "/client/chat/open"
		q.Set("openChatId", m.ChatID)
		q.Set("position", strconv.FormatInt(n, 10))
	} else {
		return "", fmt.Errorf("source position unavailable")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

var _ core.WorkflowMessageLinker = (*Platform)(nil)
