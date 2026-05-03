package line

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/line/line-bot-sdk-go/v8/linebot/messaging_api"
)

// lineClient abstracts the LINE Messaging API SDK for testability.
// All methods called on p.bot are declared here.
type lineClient interface {
	ReplyMessage(req *messaging_api.ReplyMessageRequest) (*messaging_api.ReplyMessageResponse, error)
	PushMessage(req *messaging_api.PushMessageRequest, xLineRetryKey string) (*messaging_api.PushMessageResponse, error)
	GetProfile(userId string) (*messaging_api.UserProfileResponse, error)
	GetGroupSummary(groupId string) (*messaging_api.GroupSummaryResponse, error)
	ShowLoadingAnimation(req *messaging_api.ShowLoadingAnimationRequest) (*map[string]interface{}, error)
}

const (
	replyTokenTTL = 50 * time.Second
	replyMaxBatch = 5  // LINE Reply API 一次最多 5 段
	sweepInterval = 50 * time.Second
)

// tokenEntry is cache value: reply token string + insertion time.
type tokenEntry struct {
	token string
	at    time.Time
}

// cacheReplyToken writes or overwrites the token for a targetID.
// Multiple people @bot in the same group with both tokens arriving in time is rare;
// the "latest wins" strategy is reasonable since the most recent token is most likely
// to be used within the 50s window.
func (p *Platform) cacheReplyToken(targetID, token string) {
	if targetID == "" || token == "" {
		return
	}
	p.replyTokens.Store(targetID, tokenEntry{token: token, at: time.Now()})
}

// loadAndDeleteFreshToken retrieves and deletes a token for targetID.
// Returns ok=false if the token does not exist, is expired, or is not a tokenEntry.
// "Load and delete" achieves single-use semantics (streaming second+ segments auto cache miss).
func (p *Platform) loadAndDeleteFreshToken(targetID string) (string, bool) {
	v, ok := p.replyTokens.LoadAndDelete(targetID)
	if !ok {
		return "", false
	}
	entry, ok := v.(tokenEntry)
	if !ok {
		return "", false
	}
	if time.Since(entry.at) >= replyTokenTTL {
		return "", false
	}
	return entry.token, true
}

// pushAll 把 messages 一段一段用 PushMessage 送出。錯誤即終止並回傳。
func (p *Platform) pushAll(rc replyContext, messages []string, reason string) error {
	for _, text := range messages {
		_, err := p.bot.PushMessage(
			&messaging_api.PushMessageRequest{
				To: rc.targetID,
				Messages: []messaging_api.MessageInterface{
					messaging_api.TextMessage{Text: text},
				},
			}, "",
		)
		if err != nil {
			return fmt.Errorf("line: push message: %w", err)
		}
	}
	slog.Debug("line: dispatch", "method", "push", "reason", reason, "target_type", rc.targetType, "segments", len(messages))
	return nil
}

// dispatchReply 把切好的訊息送出去。若有新鮮 reply token，前 5 段走 Reply API
// （免費，不吃 push quota）；其餘段或 token 過期/缺失時走 Push API。
//
// 錯誤策略（Task 3 簡化版；Task 4 會加 token-invalid fallback）：
//   - Reply API 任何錯誤 → 回傳 error，不退回 Push
//   - Push API 任何錯誤 → 回傳 error
func (p *Platform) dispatchReply(rc replyContext, messages []string) error {
	if len(messages) == 0 {
		return nil
	}

	token, ok := p.loadAndDeleteFreshToken(rc.targetID)
	if !ok {
		// 無法分辨「從未進 cache」與「已過期被 sweep 掉」，統一視為 no_token。
		return p.pushAll(rc, messages, "no_token")
	}

	// 取前 N 段（最多 5）走 Reply
	batchEnd := len(messages)
	if batchEnd > replyMaxBatch {
		batchEnd = replyMaxBatch
	}
	msgObjs := make([]messaging_api.MessageInterface, 0, batchEnd)
	for _, text := range messages[:batchEnd] {
		msgObjs = append(msgObjs, messaging_api.TextMessage{Text: text})
	}

	_, err := p.bot.ReplyMessage(&messaging_api.ReplyMessageRequest{
		ReplyToken: token,
		Messages:   msgObjs,
	})
	if err != nil {
		// Task 4 會處理 token-invalid fallback，這裡先簡化：任何錯回傳。
		return fmt.Errorf("line: reply message: %w", err)
	}

	slog.Debug("line: dispatch", "method", "reply", "reason", "fresh_token", "target_type", rc.targetType, "segments", batchEnd)

	// 還有剩餘段（>5）走 Push
	if batchEnd < len(messages) {
		return p.pushAll(rc, messages[batchEnd:], "after_reply_overflow")
	}
	return nil
}
