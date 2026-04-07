package daxiang

import "encoding/json"

const robotSingleChatMessage = "ROBOT_SINGLE_CHAT_MESSAGE"

type replyContext struct {
	chatID         int64
	messageID      int64
	senderID       int64
	conversationID string
	isDirect       bool
}

type callbackEvent struct {
	AppID         string              `json:"appId"`
	BotID         int64               `json:"botId"`
	EventTypeEnum string              `json:"eventTypeEnum"`
	Data          callbackMessageData `json:"data"`
}

type callbackMessageData struct {
	CTS            int64  `json:"cts"`
	FromName       string `json:"fromName"`
	FromUID        int64  `json:"fromUid"`
	MsgID          int64  `json:"msgId"`
	Message        string `json:"message"`
	ChatID         int64  `json:"chatId"`
	ConversationID string `json:"conversationId"`
	Type           int    `json:"type"`
}

type textMessageBody struct {
	Text string `json:"text"`
}

func decodeTextMessage(raw string) (string, error) {
	var body textMessageBody
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		return "", err
	}
	return body.Text, nil
}
