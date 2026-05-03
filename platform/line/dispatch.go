package line

import (
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
