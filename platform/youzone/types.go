package youzone

import "time"

const (
	defaultBaseURL          = "https://c2.yonyoucloud.com"
	defaultAPIPrefix        = "/yonbip-ec-link"
	defaultHTTPTimeout      = 30 * time.Second
	defaultPingInterval     = 25 * time.Second
	defaultRobotExplain     = "cc-connect"
	defaultReconnectDelays  = "1s,3s,10s,30s"
	heartbeatXMPPWhitespace = "xmpp-whitespace"
	heartbeatWSPing         = "ws-ping"
)

type config struct {
	baseURL                   string
	apiPrefix                 string
	robotID                   string
	accessToken               string
	tenantID                  string
	machineCode               string
	autoCreateRobot           bool
	robotExplain              string
	allowFrom                 string
	websocketProtocols        []string
	heartbeatMode             string
	pingInterval              time.Duration
	reconnectDelays           []time.Duration
	httpTimeout               time.Duration
	enableTokenHeaderFallback bool
	logInboundRaw             bool
}

type robotRecord struct {
	ID          string
	Name        string
	MachineCode string
	RobotUserID string
}

type sendResult struct {
	Success      bool
	Status       int
	BusinessCode *int
	PacketID     string
	ResponseText string
}

type inboundMessage struct {
	MessageID         string
	SenderID          string
	SenderName        string
	ConversationID    string
	Text              string
	ContentType       int
	MessageVersion    *int   // YOUZONE sessionVersion/messageVersion/version; nil when absent — used for the outbound reply-quote header
	MessageVersionRaw string // same key as MessageVersion, kept as the original string form for logs / future replay cursor; "" when absent
	Type              string
	Raw               []byte
}

type replyContext struct {
	robotID           string
	conversationID    string
	senderID          string
	messageID         string
	messageVersion    *int   // carried for the outbound reply-quote header (see outbound.go)
	messageVersionRaw string // same value as messageVersion in string form; logs only
	replyText         string // inbound text, shown as the reply-quote preview
}

// inboundDropReason names why an inbound frame was discarded before reaching
// the engine. inboundDropNone means the frame was accepted.
type inboundDropReason string

const (
	inboundDropNone             inboundDropReason = ""
	inboundDropJSONInvalid      inboundDropReason = "json_invalid"
	inboundDropHeartbeat        inboundDropReason = "heartbeat"
	inboundDropEmptyFrame       inboundDropReason = "empty_frame"
	inboundDropEmptyText        inboundDropReason = "empty_text"
	inboundDropDuplicate        inboundDropReason = "duplicate_message"
	inboundDropUnauthorizedUser inboundDropReason = "unauthorized_user"
	inboundDropMissingHandler   inboundDropReason = "missing_handler"
)
