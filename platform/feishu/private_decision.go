package feishu

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func (c *queryCardStream) ConfigureDecisionNotification(user, title string, send func(context.Context, core.DecisionNotification) error) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.decisionUser, c.decisionTitle, c.decisionNotify = user, title, send
	c.notificationKey = rand.Text()
	return true
}
func (c *queryCardStream) sendDecisionNotification(ctx context.Context) {
	if c.prompt == nil || c.decisionOwner >= len(c.cards) || c.decisionUser == "" {
		return
	}
	part := c.cards[c.decisionOwner]
	part.mu.Lock()
	defer part.mu.Unlock()
	part.decisionUser = c.decisionUser
	part.notify = c.decisionNotify
	summary := c.p.cardI18n().T(core.MsgExecNeedsApproval)
	if c.prompt.DecisionToken != "" {
		summary = c.p.cardI18n().T(core.MsgExecNeedsInput)
	}
	if part.notification == nil || part.notification.Key != c.notificationKey {
		part.notification = &core.DecisionNotification{Key: c.notificationKey, UserID: c.decisionUser, ChatID: c.rc.chatID, Title: c.decisionTitle, Summary: summary}
	}
	// Route textual quoted answers on the original card, never the private DM.
	(&decisionReminder{p: c.p, card: part, user: c.decisionUser, token: decisionPromptToken(c.prompt)}).rememberReply()
	part.notifyDecisionLocked(ctx)
	if err := part.writeLocked(ctx); err != nil {
		slog.Warn("show private reminder state", "error", err)
	}
}
func decisionPromptToken(prompt *core.Card) string {
	if prompt == nil {
		return ""
	}
	if prompt.DecisionToken != "" {
		return prompt.DecisionToken
	}
	for _, e := range prompt.Elements {
		if row, ok := e.(core.CardActions); ok {
			for _, b := range row.Buttons {
				if token := b.Extra["permission_token"]; token != "" {
					return token
				}
			}
		}
	}
	return ""
}
func (c *queryCard) notifyDecisionLocked(ctx context.Context) {
	if c.notification == nil {
		return
	}
	var err error
	if c.notification.SourceLink == "" {
		h, ok := c.handle.(*feishuPreviewHandle)
		if !ok {
			err = errQueryCardExpired
		} else {
			c.notification.SourceLink, err = c.p.WorkflowMessageLink(ctx, h.messageID)
		}
	}
	if err == nil {
		if c.notify == nil {
			err = core.ErrNotSupported
		} else {
			err = c.notify(ctx, *c.notification)
		}
	}
	c.decisionNotice = ""
	if err != nil {
		c.decisionNotice = c.p.cardI18n().T(core.MsgExecReminderFailed)
		slog.Warn("private decision reminder delivery failed", "error", err)
	}
}

type privateReminderReceipt struct {
	Created   time.Time `json:"created"`
	MessageID string    `json:"message_id,omitempty"`
}

// Persist the intent before delivery and reuse a deterministic Feishu UUID.
// A retry after an ambiguous response uses the same UUID. Never silently post
// again outside the platform's deduplication window.
func (p *Platform) SendPrivateDecisionNotification(ctx context.Context, n core.DecisionNotification) error {
	if n.Key == "" || n.UserID == "" || n.ChatID == "" || n.SourceLink == "" {
		return fmt.Errorf("private reminder missing identity or source")
	}
	if p.queryCardStateDir == "" {
		return fmt.Errorf("private reminder requires persistent delivery state")
	}
	p.privateReminderMu.Lock()
	defer p.privateReminderMu.Unlock()
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(n.Key+"\x00"+n.UserID+"\x00"+n.ChatID)))
	file := filepath.Join(p.queryCardStateDir, "notifications", key+".json")
	state := privateReminderReceipt{Created: time.Now()}
	if data, err := os.ReadFile(file); err == nil {
		if err = json.Unmarshal(data, &state); err != nil {
			return fmt.Errorf("read reminder receipt: %w", err)
		}
		if state.MessageID != "" {
			return nil
		}
		if time.Since(state.Created) > 23*time.Hour {
			return fmt.Errorf("reminder delivery uncertain beyond retry window")
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read reminder receipt: %w", err)
	}
	if err := writePrivateJSON(file, state); err != nil {
		return err
	}
	// Native post links open the group card; no callback can authorize from a DM.
	text := executionClip(n.Title, 160) + "\n" + executionClip(p.resolveChatName(n.ChatID), 100) + "\n" + n.Summary + "\n"
	body, _ := json.Marshal(map[string]any{"zh_cn": map[string]any{"title": "", "content": [][]map[string]string{{{"tag": "text", "text": text}}, {{"tag": "a", "text": p.cardI18n().T(core.MsgExecOpenOriginal), "href": n.SourceLink}}}}})
	req := larkim.NewCreateMessageReqBuilder().ReceiveIdType("open_id").Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(n.UserID).MsgType("post").Content(string(body)).Uuid(key[:32]).Build()).Build()
	err := p.withTransientRetry(ctx, "private decision reminder", func() error {
		return p.withFreshTenantAccessTokenRetry(ctx, "private decision reminder", func(client *lark.Client, opts ...larkcore.RequestOptionFunc) error {
			resp, err := client.Im.Message.Create(ctx, req, opts...)
			if err != nil {
				return err
			}
			if !resp.Success() {
				return fmt.Errorf("private reminder code=%d", resp.Code)
			}
			if resp.Data == nil || resp.Data.MessageId == nil {
				return fmt.Errorf("private reminder missing message ID")
			}
			state.MessageID = *resp.Data.MessageId
			return nil
		})
	})
	if err != nil {
		return err
	}
	return writePrivateJSON(file, state)
}
func writePrivateJSON(file string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(file), ".state-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), file)
}

var _ core.PrivateDecisionNotifier = (*Platform)(nil)

// Save the submitting user's draft before the agent attempts to accept it.
// Reject forged chat/message/session references at the platform boundary too.
func (p *Platform) captureExecutionDecision(id, chat, message, user, token string, form map[string]any) (string, error) {
	c, err := p.loadQueryCard(id)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	h, ok := c.handle.(*feishuPreviewHandle)
	if !ok || !c.native || c.rc.chatID != chat || h.messageID != message || c.prompt == nil || decisionPromptToken(c.prompt) != token || (c.decisionUser != "" && c.decisionUser != user) {
		return "", errQueryCardExpired
	}
	if draft, ok := form["answer"].(string); ok {
		c.decisionDraft = draft
	}
	return c.rc.sessionKey, nil
}

func (c *queryCardStream) SetDecisionNotice(ctx context.Context, text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notice = text
	c.forceDecisionUpdate = true
	defer func() { c.forceDecisionUpdate = false }()
	return c.writeLocked(ctx)
}
