package daxiangbridge

import (
	"encoding/json"
	"time"
)

func buildFrame(frameType, requestID, sessionID string, payload any) BridgeFrame {
	raw, _ := json.Marshal(payload)
	return BridgeFrame{
		Type:      frameType,
		RequestID: requestID,
		SessionID: sessionID,
		Ts:        time.Now().UnixMilli(),
		Payload:   raw,
	}
}

func buildFinalReplyFrame(requestID, sessionID, text string) BridgeFrame {
	return buildFrame(FrameTypeAgentReplyFinal, requestID, sessionID, AgentReplyPayload{Text: text})
}

func buildStartFrame(requestID, sessionID string) BridgeFrame {
	return buildFrame(FrameTypeAgentReplyStart, requestID, sessionID, struct{}{})
}

func buildDeltaFrame(requestID, sessionID string, seq int, delta string) BridgeFrame {
	return buildFrame(FrameTypeAgentReplyDelta, requestID, sessionID, AgentDeltaPayload{Seq: seq, Delta: delta})
}

func buildEndFrame(requestID, sessionID, finalText string) BridgeFrame {
	return buildFrame(FrameTypeAgentReplyEnd, requestID, sessionID, AgentReplyPayload{Text: finalText})
}

func buildPermissionRequestFrame(requestID, sessionID, permissionID, toolName, action, reason string) BridgeFrame {
	return buildFrame(FrameTypeAgentPermissionRequest, requestID, sessionID, AgentPermissionRequestPayload{
		PermissionID: permissionID,
		ToolName:     toolName,
		Action:       action,
		Reason:       reason,
	})
}

func buildRegisterFrame(clientID string, botID int64) BridgeFrame {
	return buildFrame(FrameTypeClientRegister, "", "", ClientRegisterPayload{
		ClientID:          clientID,
		BotID:             botID,
		ClientVersion:     "0.1.0",
		StreamCapable:     true,
		PermissionCapable: true,
	})
}

func buildPingFrame() BridgeFrame {
	return BridgeFrame{Type: FrameTypePing, Ts: time.Now().UnixMilli()}
}
