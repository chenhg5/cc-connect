package line

import (
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
