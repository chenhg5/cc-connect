package daxiangbridge

import "encoding/json"

const (
	FrameTypeClientRegister           = "client.register"
	FrameTypeClientRegistered         = "client.registered"
	FrameTypeClientAck                = "client.ack"
	FrameTypeBridgeEventMessage       = "bridge.event.message"
	FrameTypeAgentReplyFinal          = "agent.reply.final"
	FrameTypeAgentReplyStart          = "agent.reply.start"
	FrameTypeAgentReplyDelta          = "agent.reply.delta"
	FrameTypeAgentReplyEnd            = "agent.reply.end"
	FrameTypeAgentPermissionRequest   = "agent.permission.request"
	FrameTypeBridgePermissionResponse = "bridge.permission.response"
	FrameTypePing                     = "ping"
	FrameTypePong                     = "pong"
)

type BridgeFrame struct {
	Type      string          `json:"type"`
	RequestID string          `json:"requestId,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Ts        int64           `json:"ts,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

type BridgeEventPayload struct {
	Platform       string `json:"platform"`
	ChatType       string `json:"chatType"`
	ConversationID string `json:"conversationId"`
	MessageID      string `json:"messageId"`
	FromUserID     string `json:"fromUserId"`
	FromUserName   string `json:"fromUserName"`
	Text           string `json:"text"`
}

type ClientRegisterPayload struct {
	ClientID          string `json:"clientId"`
	BotID             int64  `json:"botId"`
	ClientVersion     string `json:"clientVersion"`
	StreamCapable     bool   `json:"streamCapable"`
	PermissionCapable bool   `json:"permissionCapable"`
	Credential        string `json:"credential"`  // AES-GCM encrypted "clientId:timestamp"
	Timestamp         int64  `json:"timestamp"`   // epoch millis, must match credential plaintext
}

type AgentReplyPayload struct {
	Text string `json:"text"`
}

type AgentDeltaPayload struct {
	Seq   int    `json:"seq"`
	Delta string `json:"delta"`
}

type AgentPermissionRequestPayload struct {
	PermissionID string `json:"permissionId"`
	ToolName     string `json:"toolName"`
	Action       string `json:"action"`
	Reason       string `json:"reason"`
}

type PermissionResponsePayload struct {
	PermissionID string `json:"permissionId"`
	Decision     string `json:"decision"`
}
